# Overview 

The `src/internal/python/` directory contains the shared database and schema layer for the Python-based pre-processing modules (Silver). It handles asynchronous PostgreSQL connections, complex transactional state handoffs, and strict data validation.

> Note: This Documentation was written partially using AI (Gemini 3.1 Pro).

**Stack:**
- Python 3
- `asyncpg` (for high-performance async PostgreSQL connections)
- `pydantic` (for data validation and schema definition)

---

## Directory Structure 

```text
src/internal/python/
└── db/
    ├── models.py      # Pydantic schemas and Enums
    └── store.py       # asyncpg connection pool & state progression

```

---

## Data Models (`models.py`)

All database entities are strictly typed using Pydantic models and Enums. This guarantees that AI outputs (like Gemini JSON responses) can be safely parsed before hitting the database.

| Type | Description |
| --- | --- |
| `Episode` | Holds MinIO keys and granular `updated_at` timestamps to track processing progress. |
| `Chapter` / `TranscriptLine` | AI-generated sections and word/sentence-level timing data, complete with `EmotionLabel`. |
| `FactCheckedClaim` | AI-verified statements linked to chapters, including a `FactVerdict` (e.g., `MISLEADING`). |
| `PipelineBatch` | Tracks execution state (`PipelineStage`, `LoadMode`, `BatchStatus`). |
| `Embedding` | Defines vector storage levels (`chapter`, `episode`, `podcast`). |

---

## Core Logic and Data Flow

### `db/store.py` – Database Store

Abstracts connection pooling and implements the complex transactional logic required to safely pass data between distributed AI workers.

| Function | Description |
| --- | --- |
| `claim_batch_episodes` | **Critical:** Atomically re-points episodes from an upstream batch (e.g., Ingestion) to a new batch (e.g., Transcription) and marks the old batch `consumed`. Prevents multiple workers from processing the same files. |
| `complete_pipeline_batch` | Finalizes a batch and fires dynamic `pg_notify` events to wake up downstream workers (passing the `load_mode` forward). |
| `set_preprocessing_updated_at` | Uses CTEs (Common Table Expressions) to update timestamps on both the Episode and its parent Podcast in a single transaction. |
| `get_episodes_for_full_*` | Helper queries used when a manual `load_mode: full` override is triggered, fetching all applicable episodes bypassing batch IDs. |

---

## Pipeline Batch Handoff Architecture

To ensure files don't get lost between processing stages (Transcription -> Sectioning -> Fact Checking), the Python store implements a strict transactional handoff:

1. A downstream Python worker wakes up (via `pg_notify`).
2. It creates its own new `pending` batch (`create_pipeline_batch`).
3. It calls `claim_batch_episodes(source_batch_id, new_batch_id)`.
4. The database atomically updates the foreign keys of the target episodes to the new batch and marks the source batch as `consumed`.

If the Python worker crashes during AI inference, its batch remains stuck in `pending` or fails, but the data is securely locked to that batch, making it easy to identify and retry failed jobs.
