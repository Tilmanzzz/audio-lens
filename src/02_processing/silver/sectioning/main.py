import os
import sys
import json
import asyncio
import signal
import nltk
from typing import Any, Dict, List

import asyncpg
from minio import Minio
from pydantic import BaseModel, Field
from google import genai
from google.genai import types

sys.path.append(os.path.abspath(os.path.join(os.path.dirname(__file__), "../../")))

from internal.python.db.store import Store
from common.app_logger import AppLogger

logger_instance = AppLogger(
    module_name="sectioning_worker",
    enabled=True,
    level=os.getenv("LOG_LEVEL", "INFO"),
    log_dir=os.getenv("LOG_DIR", "/app/logs"),
)
logger = logger_instance.build()

shutdown_event = asyncio.Event()
internal_task_queue: asyncio.Queue[Dict[str, Any]] = asyncio.Queue()

gemini_client = genai.Client()


class ChapterMetadata(BaseModel):
    start_sentence_id: int = Field(
        description="The exact ID of the first sentence in this chapter."
    )
    end_sentence_id: int = Field(
        description="The exact ID of the last sentence in this chapter."
    )
    topic: str = Field(
        description="A concise, free-form topic for the chapter (1 to maximum 3 words, Title Case)."
    )


class ChapterList(BaseModel):
    chapters: list[ChapterMetadata]


def handle_exit_signals() -> None:
    logger.info("Received termination signal. Shutting down...")
    shutdown_event.set()


def init_minio_client() -> Minio:
    endpoint = os.getenv("MINIO_ENDPOINT", "localhost:9000")
    return Minio(
        endpoint,
        access_key=os.getenv("MINIO_USER", "minioadmin"),
        secret_key=os.getenv("MINIO_PASS", "minioadmin"),
        secure=False,
    )


def download_transcript(minio: Minio, transcript_key: str) -> Dict[str, Any]:
    logger.info(f"Downloading transcript: {transcript_key}")
    response = minio.get_object("silver", transcript_key)
    try:
        raw = b"".join(response.stream(32 * 1024))
        return json.loads(raw.decode("utf-8"))
    finally:
        response.close()
        response.release_conn()


def create_sentence_lines(
    whisper_segments: List[Dict[str, Any]],
) -> List[Dict[str, Any]]:
    if not whisper_segments:
        return []

    full_text = ""
    char_to_time = []

    for seg in whisper_segments:
        start_time = seg["start"]
        end_time = seg["end"]
        text = seg["text"].strip() + " "

        start_idx = len(full_text)
        full_text += text
        end_idx = len(full_text)

        chars_in_seg = end_idx - start_idx
        duration = end_time - start_time

        for i in range(chars_in_seg):
            time_at_char = start_time + (i / chars_in_seg) * duration
            char_to_time.append(time_at_char)

    sentences = nltk.sent_tokenize(full_text)

    sentence_lines = []
    search_start_idx = 0

    for idx, sentence in enumerate(sentences):
        match_idx = full_text.find(sentence, search_start_idx)
        if match_idx == -1:
            continue

        match_end_idx = match_idx + len(sentence)

        sent_start_time = char_to_time[match_idx]
        sent_end_time = char_to_time[match_end_idx - 1]

        sentence_lines.append(
            {
                "id": idx,
                "start": round(sent_start_time, 3),
                "end": round(sent_end_time, 3),
                "text": sentence,
            }
        )

        search_start_idx = match_end_idx

    return sentence_lines


async def extract_chapters_gemini(
    sentence_lines: List[Dict[str, Any]],
) -> Dict[str, Any]:
    if not sentence_lines:
        return {"chapters": []}

    formatted_transcript = "\n".join(
        f"[{line['id']}] {line['text']}" for line in sentence_lines
    )

    prompt = f"""
    You are an expert audio producer. Analyze the following podcast transcript.
    Segment it into logical chapters based on distinct topic shifts. 

    Rules:
    1. Define the boundary using the exact sentence IDs provided in brackets [ID].
    2. Chapters must be continuous (the next chapter must start where the previous one ended).
    3. Generate a concise, free-form topic (1 to maximum 3 words) that accurately describes the core theme.

    Transcript:
    {formatted_transcript}
    """

    try:
        response = await gemini_client.aio.models.generate_content(
            model="gemini-3.5-flash",
            contents=prompt,
            config=types.GenerateContentConfig(
                response_mime_type="application/json",
                response_schema=ChapterList,
                temperature=0.1,
            ),
        )
        return json.loads(response.text)
    except Exception as e:
        logger.error(f"Gemini API inference failed: {e}")
        raise RuntimeError(f"Failed to extract chapters: {e}")


async def process_episode(
    episode_id: str, batch_id: str, store: Store, minio: Minio
) -> None:
    row = await store.pool.fetchrow(
        "SELECT transcript_key, podcast_id FROM episodes WHERE id = $1", episode_id
    )

    if not row or not row["transcript_key"]:
        raise ValueError(f"Episode {episode_id} has no transcript_key")

    transcript_key = row["transcript_key"]
    podcast_id = str(row["podcast_id"])

    transcript = await asyncio.to_thread(download_transcript, minio, transcript_key)
    whisper_segments: List[Dict[str, Any]] = transcript.get("segments", [])

    if not whisper_segments:
        logger.warning(f"Episode {episode_id}: empty segments. Skipping.")
        return

    sentence_lines = create_sentence_lines(whisper_segments)

    llm_response = await extract_chapters_gemini(sentence_lines)
    chapters_metadata = llm_response.get("chapters", [])

    if not chapters_metadata:
        return

    total_lines = 0
    max_idx = len(sentence_lines) - 1

    async with store.pool.acquire() as conn:
        async with conn.transaction():
            for section_idx, chapter_data in enumerate(chapters_metadata):
                start_id = max(0, min(chapter_data["start_sentence_id"], max_idx))
                end_id = max(0, min(chapter_data["end_sentence_id"], max_idx))

                chapter_sentences = sentence_lines[start_id : end_id + 1]
                if not chapter_sentences:
                    continue

                chapter_start_time = chapter_sentences[0]["start"]
                chapter_end_time = chapter_sentences[-1]["end"]
                chapter_transcript = " ".join(s["text"] for s in chapter_sentences)
                chapter_topic = chapter_data["topic"]

                chapter_db_id: str = await conn.fetchval(
                    """
                    INSERT INTO chapters
                        (episode_id, batch_id, chapter_idx, start_time, end_time, title, transcript)
                    VALUES ($1, $2, $3, $4, $5, $6, $7)
                    ON CONFLICT (episode_id, chapter_idx) DO UPDATE
                        SET title      = EXCLUDED.title,
                            transcript = EXCLUDED.transcript,
                            start_time = EXCLUDED.start_time,
                            end_time   = EXCLUDED.end_time,
                            batch_id   = EXCLUDED.batch_id
                    RETURNING id
                    """,
                    episode_id,
                    batch_id,
                    section_idx,
                    chapter_start_time,
                    chapter_end_time,
                    chapter_topic,
                    chapter_transcript,
                )

                line_records = [
                    (
                        str(chapter_db_id),
                        batch_id,
                        line_idx,
                        line["start"],
                        line["end"],
                        line["text"],
                    )
                    for line_idx, line in enumerate(chapter_sentences)
                ]

                await conn.executemany(
                    """
                    INSERT INTO transcript_lines
                        (chapter_id, batch_id, line_idx, start_time, end_time, text)
                    VALUES ($1, $2, $3, $4, $5, $6)
                    ON CONFLICT (chapter_id, line_idx) DO UPDATE
                        SET text       = EXCLUDED.text,
                            start_time = EXCLUDED.start_time,
                            end_time   = EXCLUDED.end_time,
                            batch_id   = EXCLUDED.batch_id
                    """,
                    line_records,
                )

                total_lines += len(chapter_sentences)

    await store.set_preprocessing_updated_at(episode_id)

    logger.info(
        f"Episode {episode_id} (Podcast {podcast_id}): {len(chapters_metadata)} chapters generated ({total_lines} lines)."
    )


async def handle_pipeline_task(
    task: Dict[str, Any], store: Store, minio: Minio
) -> None:
    mode = task.get("mode", "delta")
    segmenting_batch_id = await store.create_pipeline_batch("segmenting", mode)

    if mode == "full":
        logger.info(
            f"processing full database workload for batch: {segmenting_batch_id}"
        )
        episode_ids = await store.get_episodes_for_full_sectioning()
    else:
        transcription_batch_id = task["batch_id"]
        logger.info(
            f"processing delta workload from transcription batch: {transcription_batch_id}"
        )
        episode_ids = await store.claim_batch_episodes(
            transcription_batch_id, segmenting_batch_id
        )

    if not episode_ids:
        logger.info(f"workload empty. discarding batch {segmenting_batch_id}.")
        await store.pool.execute(
            "DELETE FROM pipeline_batches WHERE id = $1", segmenting_batch_id
        )
        return

    logger.info(
        f"processing {len(episode_ids)} episodes for batch {segmenting_batch_id} (mode: {mode})"
    )

    for episode_id in episode_ids:
        await process_episode(episode_id, segmenting_batch_id, store, minio)

    await store.complete_pipeline_batch(
        batch_id=segmenting_batch_id,
        status="success",
        load_mode=mode,
        notify_channel="processing_ready",
    )


async def postgres_listener_task(pg_url: str) -> None:
    while not shutdown_event.is_set():
        conn = await asyncpg.connect(pg_url)
        try:

            def on_notification(connection, pid, channel, payload):
                event = json.loads(payload)
                mode = event.get("load_mode", "delta")
                if mode == "full":
                    internal_task_queue.put_nowait({"mode": "full"})
                elif batch_id := event.get("batch_id"):
                    internal_task_queue.put_nowait(
                        {"mode": "delta", "batch_id": batch_id}
                    )

            await conn.add_listener("segmenting_ready", on_notification)
            while not shutdown_event.is_set():
                await asyncio.sleep(30)
                await conn.execute("SELECT 1")
        except Exception as e:
            logger.error(f"Postgres listener error: {e}")
            await asyncio.sleep(5)
        finally:
            await conn.close()


async def queue_backlog_batches(store: Store) -> None:
    rows = await store.pool.fetch(
        "SELECT id FROM pipeline_batches WHERE stage = 'transcription' AND status = 'success' ORDER BY start_ts ASC"
    )
    for row in rows:
        internal_task_queue.put_nowait({"mode": "delta", "batch_id": str(row["id"])})


async def main() -> None:
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGINT, signal.SIGTERM):
        loop.add_signal_handler(sig, handle_exit_signals)

    pg_url = os.getenv("POSTGRES_URL")
    if not pg_url:
        raise ValueError("POSTGRES_URL environment variable is required")

    store = Store(pg_url)
    await store.connect()
    minio = init_minio_client()

    listener = asyncio.create_task(postgres_listener_task(pg_url))
    await queue_backlog_batches(store)

    sem = asyncio.Semaphore(5)

    async def worker(task: Dict[str, Any]):
        async with sem:
            try:
                await handle_pipeline_task(task, store, minio)
            except Exception as e:
                logger.error(f"Failed to process task {task}: {e}")

    while not shutdown_event.is_set():
        try:
            task = await asyncio.wait_for(internal_task_queue.get(), timeout=1.0)
            asyncio.create_task(worker(task))
            internal_task_queue.task_done()
        except asyncio.TimeoutError:
            continue
        except asyncio.CancelledError:
            break

    listener.cancel()
    await store.close()


if __name__ == "__main__":
    nltk.download("punkt", quiet=True)
    nltk.download("punkt_tab", quiet=True)
    asyncio.run(main())
