import logging
import json
import asyncpg
from typing import List, Optional

from .models import Episode

logger = logging.getLogger(__name__)


class Store:
    def __init__(self, conn_str: str):
        self.conn_str = conn_str
        self.pool: Optional[asyncpg.Pool] = None

    async def connect(self) -> None:
        try:
            self.pool = await asyncpg.create_pool(
                dsn=self.conn_str, min_size=2, max_size=10
            )
            logger.info("established asyncpg PostgreSQL connection pool")
        except Exception as e:
            logger.error(f"failed to connect to PostgreSQL via asyncpg: {e}")
            raise

    async def close(self) -> None:
        if self.pool:
            await self.pool.close()

    async def get_episode_by_id(self, episode_id: str) -> Episode:
        query = """
        SELECT
            id, podcast_id, guid, title, published_at, duration_seconds,
            audio_key, transcript_key, cover_key, enclosure_url, summary, batch_id,
            ingested_at, source_system_updated_at, ingestion_updated_at
        FROM episodes
        WHERE id = $1
        """
        row = await self.pool.fetchrow(query, episode_id)
        if not row:
            raise Exception(f"Episode {episode_id} not found")

        return Episode(**row)

    async def get_episodes_for_full_transcription(self) -> List[str]:
        """fetches all episode IDs eligible for a full transcription run"""
        query = (
            "SELECT id FROM episodes WHERE audio_key IS NOT NULL AND audio_key != ''"
        )
        rows = await self.pool.fetch(query)
        return [str(row["id"]) for row in rows]

    async def get_episodes_for_full_sectioning(self) -> List[str]:
        """fetches all episode IDs eligible for a full sectioning run"""
        query = "SELECT id FROM episodes WHERE transcript_key IS NOT NULL AND transcript_key != ''"
        rows = await self.pool.fetch(query)
        return [str(row["id"]) for row in rows]

    async def set_transcript_key(self, episode_id: str, transcript_key: str) -> None:
        query = "UPDATE episodes SET transcript_key = $1 WHERE id = $2"
        status = await self.pool.execute(query, transcript_key, episode_id)

        if status == "UPDATE 0":
            raise Exception(f"No episode found with id: {episode_id}")

    async def create_pipeline_batch(self, stage: str, mode: str) -> str:
        query = """
            INSERT INTO pipeline_batches (stage, load_mode, status, start_ts, fin_ts)
            VALUES ($1::pipeline_stage, $2::load_mode, 'pending'::batch_status, NOW(), NOW())
            RETURNING id
        """
        batch_id = await self.pool.fetchval(query, stage, mode)
        return str(batch_id)

    async def complete_pipeline_batch(
        self,
        batch_id: str,
        status: str,
        load_mode: str = "delta",
        notify_channel: Optional[str] = None,
    ) -> None:
        """finalizes a batch and, on success, notifies downstream services"""
        query = """
            UPDATE pipeline_batches
            SET status = $1::batch_status, fin_ts = NOW()
            WHERE id = $2
        """
        status_tag = await self.pool.execute(query, status, batch_id)
        if status_tag == "UPDATE 0":
            raise Exception(
                f"Failed to update pipeline batch status: batch_id {batch_id} not found"
            )

        if status == "success" and notify_channel:
            payload = json.dumps({"batch_id": batch_id, "load_mode": load_mode})
            await self.pool.execute("SELECT pg_notify($1, $2)", notify_channel, payload)
            logger.info(
                f"broadcast '{notify_channel}' event for batch {batch_id} (mode: {load_mode})"
            )

    async def claim_batch_episodes(
        self,
        source_batch_id: str,
        new_batch_id: str,
    ) -> List[str]:
        """
        atomically re-points episodes from source_batch to new_batch and
        marks source_batch as 'consumed'
        """
        async with self.pool.acquire() as conn:
            async with conn.transaction():
                rows = await conn.fetch(
                    """
                    UPDATE episodes
                    SET batch_id = $1
                    WHERE batch_id = $2
                    RETURNING id
                    """,
                    new_batch_id,
                    source_batch_id,
                )
                if rows:
                    await conn.execute(
                        """
                        UPDATE pipeline_batches
                        SET status = 'consumed'::batch_status
                        WHERE id = $1
                        """,
                        source_batch_id,
                    )
                return [str(row["id"]) for row in rows]

    async def set_processing_updated_at(self, episode_id: str) -> None:
        query = "UPDATE episodes SET processing_updated_at = NOW() WHERE id = $1"
        status = await self.pool.execute(query, episode_id)

        if status == "UPDATE 0":
            raise Exception(f"No episode found with id: {episode_id}")

    async def set_preprocessing_updated_at(self, episode_id: str) -> None:
        # bump the episode timestamp and mirror it onto the parent podcast
        query = """
            WITH updated_episode AS (
                UPDATE episodes
                SET preprocessing_updated_at = NOW()
                WHERE id = $1
                RETURNING podcast_id, preprocessing_updated_at
            )
            UPDATE podcasts
            SET preprocessing_updated_at = (SELECT preprocessing_updated_at FROM updated_episode)
            WHERE id = (SELECT podcast_id FROM updated_episode);
        """
        status = await self.pool.execute(query, episode_id)

        if status == "UPDATE 0":
            raise Exception(f"No episode found with id: {episode_id}")

    async def set_ingestion_updated_at(self, episode_id: str) -> None:
        query = "UPDATE episodes SET ingestion_updated_at = NOW() WHERE id = $1"
        status = await self.pool.execute(query, episode_id)

        if status == "UPDATE 0":
            raise Exception(f"No episode found with id: {episode_id}")
