import os
import io
import sys
import json
import asyncio
import signal
import tempfile
import asyncpg
import aiohttp
from typing import Tuple, Dict, Any
from minio import Minio
from minio.error import S3Error

sys.path.append(os.path.abspath(os.path.join(os.path.dirname(__file__), "../../")))

from internal.python.db.store import Store
from common.app_logger import AppLogger

logger_instance = AppLogger(
    module_name="transcription_worker",
    enabled=True,
    level=os.getenv("LOG_LEVEL", "INFO"),
)
logger = logger_instance.build()

shutdown_event = asyncio.Event()
internal_task_queue: asyncio.Queue[Dict[str, Any]] = asyncio.Queue()


def handle_exit_signals():
    logger.info("received termination signal, shutting down...")
    shutdown_event.set()


def init_minio_clients() -> Tuple[Minio, Minio]:
    minio_endpoint = (
        os.getenv("MINIO_ENDPOINT", "localhost:9000")
        .replace("http://", "")
        .replace("https://", "")
    )
    user = os.getenv("MINIO_USER", "minioadmin")
    password = os.getenv("MINIO_PASS", "minioadmin")

    bronze = Minio(minio_endpoint, access_key=user, secret_key=password, secure=False)
    silver = Minio(minio_endpoint, access_key=user, secret_key=password, secure=False)

    for bucket_name in ["bronze", "silver"]:
        try:
            if not bronze.bucket_exists(bucket_name):
                logger.info(f"creating missing bucket: {bucket_name}")
                bronze.make_bucket(bucket_name)
        except S3Error as e:
            # If the Go worker created the bucket at the same time Minio will throw an S3Error
            if e.code in ["BucketAlreadyExists", "BucketAlreadyOwnedByYou"]:
                logger.info(
                    f"bucket {bucket_name} was already created by another worker."
                )
            else:
                raise e

    return bronze, silver


async def postgres_listener_task(pg_url: str):
    while not shutdown_event.is_set():
        conn = None
        try:
            conn = await asyncpg.connect(pg_url)

            def display_notification(connection, pid, channel, payload):
                try:
                    event_data = json.loads(payload)
                    mode = event_data.get("load_mode", "delta")

                    if mode == "full":
                        internal_task_queue.put_nowait({"mode": "full"})
                    elif batch_id := event_data.get("batch_id"):
                        internal_task_queue.put_nowait(
                            {"mode": "delta", "batch_id": batch_id}
                        )
                except Exception as e:
                    logger.error(f"failed to parse notification: {e}")

            await conn.add_listener("transcription_ready", display_notification)
            logger.info("listening on 'transcription_ready'")

            while not shutdown_event.is_set():
                await asyncio.sleep(30)
                await conn.execute("SELECT 1")

        except Exception as e:
            if not shutdown_event.is_set():
                logger.warning(
                    f"db listener lost connection: {e}. reconnecting in 5s..."
                )
                await asyncio.sleep(5)
        finally:
            if conn:
                try:
                    await conn.close()
                except Exception:
                    pass


async def queue_backlog_batches(store: Store):
    try:
        missed_batches = await store.pool.fetch(
            """
            SELECT id FROM pipeline_batches 
            WHERE stage = 'ingestion' AND status = 'success'
            ORDER BY start_ts ASC;
            """
        )
        for row in missed_batches:
            internal_task_queue.put_nowait(
                {"mode": "delta", "batch_id": str(row["id"])}
            )

        if missed_batches:
            logger.info(f"queued {len(missed_batches)} backlog batches")
    except Exception as e:
        logger.error(f"failed to fetch backlog: {e}")


def download_audio(minio_client: Minio, audio_key: str, target_file) -> None:
    logger.info(f"downloading {audio_key}")
    response = minio_client.get_object("bronze", audio_key)
    try:
        for chunk in response.stream(32 * 1024):
            target_file.write(chunk)
        target_file.flush()
    finally:
        response.close()
        response.release_conn()


def upload_transcript(
    minio_client: Minio, transcript_key: str, raw_output: dict
) -> None:
    raw_json_bytes = json.dumps(raw_output).encode("utf-8")
    data_stream = io.BytesIO(raw_json_bytes)

    logger.info(f"uploading transcript to {transcript_key}")
    minio_client.put_object(
        bucket_name="silver",
        object_name=transcript_key,
        data=data_stream,
        length=len(raw_json_bytes),
        content_type="application/json",
    )


async def process_episode(
    episode_id: str, store: Store, bronze: Minio, silver: Minio
) -> None:
    ep = await store.get_episode_by_id(episode_id)
    if not ep:
        raise ValueError(f"episode not found: {episode_id}")

    api_url = os.environ.get("WHISPER_API_URL")
    if not api_url:
        raise RuntimeError(
            "WHISPER_API_URL environment variable is missing. Please configure it."
        )

    with tempfile.NamedTemporaryFile(suffix=".mp3", delete=True) as tmp_file:
        download_audio(bronze, ep.audio_key, tmp_file)

        logger.info(f"transcribing {ep.title} via remote API at {api_url}")

        async with aiohttp.ClientSession() as session:
            data = aiohttp.FormData()
            data.add_field(
                "file",
                open(tmp_file.name, "rb"),
                filename=os.path.basename(tmp_file.name),
            )
            data.add_field("language", "en")
            data.add_field("response_format", "verbose_json")

            timeout = aiohttp.ClientTimeout(total=3600)

            async with session.post(api_url, data=data, timeout=timeout) as resp:
                if resp.status != 200:
                    error_text = await resp.text()
                    raise RuntimeError(
                        f"api request failed with status {resp.status}: {error_text}"
                    )

                api_response = await resp.json()

        raw_output = {
            "metadata": {
                "language": api_response.get("language", "en"),
                "language_probability": 1.0,
                "duration": api_response.get("duration", 0.0),
            },
            "text": api_response.get("text", "").strip(),
            "segments": [],
        }

        for segment in api_response.get("segments", []):
            mapped_segment = {
                "start": segment.get("start"),
                "end": segment.get("end"),
                "text": segment.get("text", "").strip(),
                "words": [],
            }

            for w in segment.get("words", []):
                mapped_segment["words"].append(
                    {
                        "word": w.get("word", "").strip(),
                        "start": w.get("start"),
                        "end": w.get("end"),
                        "probability": w.get("probability", 1.0),
                    }
                )

            raw_output["segments"].append(mapped_segment)

    base_dir = os.path.dirname(ep.audio_key)
    transcript_key = f"{base_dir}/transcript.json"

    upload_transcript(silver, transcript_key, raw_output)

    logger.info(f"updating db ref for {transcript_key}")
    await store.set_transcript_key(ep.id, transcript_key)
    await store.set_ingestion_updated_at(ep.id)


async def handle_pipeline_task(
    task: Dict[str, Any],
    store: Store,
    bronze: Minio,
    silver: Minio,
):
    mode = task.get("mode", "delta")
    transcription_batch_id = await store.create_pipeline_batch("transcription", mode)

    if mode == "full":
        logger.info(
            f"processing full database workload for batch: {transcription_batch_id}"
        )
        episode_ids = await store.get_episodes_for_full_transcription()
    else:
        ingestion_batch_id = task["batch_id"]
        logger.info(
            f"processing delta workload from ingestion batch: {ingestion_batch_id}"
        )
        episode_ids = await store.claim_batch_episodes(
            ingestion_batch_id, transcription_batch_id
        )

    if not episode_ids:
        logger.info(f"workload empty. discarding batch {transcription_batch_id}.")
        await store.pool.execute(
            "DELETE FROM pipeline_batches WHERE id = $1", transcription_batch_id
        )
        return

    logger.info(
        f"processing {len(episode_ids)} episodes for batch {transcription_batch_id} (mode: {mode})"
    )

    for episode_id in episode_ids:
        try:
            await process_episode(episode_id, store, bronze, silver)
        except Exception as e:
            logger.error(f"failed processing episode {episode_id}: {e}")

    await store.complete_pipeline_batch(
        batch_id=transcription_batch_id,
        status="success",
        load_mode=mode,
        notify_channel="segmenting_ready",
    )


async def main():
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGINT, signal.SIGTERM):
        loop.add_signal_handler(sig, handle_exit_signals)

    pg_url = os.getenv("POSTGRES_URL", "postgresql://user:pass@localhost:5432/db")
    store = Store(pg_url)
    await store.connect()

    minio_bronze, minio_silver = init_minio_clients()

    listener_task = asyncio.create_task(postgres_listener_task(pg_url))
    await queue_backlog_batches(store)

    logger.info("worker ready.")

    while not shutdown_event.is_set():
        try:
            try:
                task = await asyncio.wait_for(internal_task_queue.get(), timeout=1.0)
            except asyncio.TimeoutError:
                continue

            await handle_pipeline_task(task, store, minio_bronze, minio_silver)
            internal_task_queue.task_done()

        except asyncio.CancelledError:
            break
        except Exception as e:
            logger.error(f"error during batch processing: {e}", exc_info=True)
            await asyncio.sleep(2)

    logger.info("cleaning up...")
    listener_task.cancel()
    await store.close()
    logger.info("worker stopped.")


if __name__ == "__main__":
    asyncio.run(main())
