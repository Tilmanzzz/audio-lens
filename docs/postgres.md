# Overview

The PostgreSQL database acts as the central source of truth and the asynchronous message broker for the entire Media Lens platform. It stores show metadata, AI-generated insights, vector embeddings for semantic search, and orchestrates the distributed worker pipeline.

The schema is defined in `storage/db/init_v2.sql` and uses strict relational integrity, custom ENUM types, and the `pgvector` extension to power AI features.

> Note: This Documentation was written partially using AI (Gemini 3.1 Pro).

**Stack:**

* PostgreSQL
* `pgvector` (for `hnsw` vector indexing)

---

## Core Schema & Entity Hierarchy

The database follows a strict top-down hierarchy. Deleting a parent entity automatically cleans up all child records via `ON DELETE CASCADE`.

| Table | Description |
| --- | --- |
| `podcasts` | Show-level metadata (RSS feed URL, hosts, categories). Tracks the remote `source_system_updated_at`. |
| `episodes` | Individual media items. Holds duration, timestamps, and references to MinIO object keys (`audio_key`, `transcript_key`, `cover_key`). |
| `chapters` | Thematic segments of an episode, bounded by exact `start_time` and `end_time`. |
| `transcript_lines` | Sentence-level text with timestamps and AI-generated `emotion` labels (happy, angry, sad, neutral). Powers the frontend player. |
| `fact_checked_claims` | AI-verified statements linked to specific chapters, including a `verdict` (e.g., `FALSE`, `MISLEADING`) and source URLs. |
| `embeddings` | Vector storage for semantic search. Links to exactly one entity level (podcast, episode, or chapter). |

---

## Pipeline Batch Architecture

To ensure safe, restartable, and distributed data processing, the database implements a stage-independent batch control system. This replaces the need for an external message queue like RabbitMQ or Kafka.

### The `pipeline_batches` Table

This is the command center of the backend. Every time a worker starts a job, it creates a row in this table.

**Enums driving the pipeline:**

* **`pipeline_stage`**: Represents the worker type (`ingestion`, `transcription`, `segmenting`, `embedder`, etc.).
* **`load_mode`**: Represents the workload (`full` for everything, `delta` for only new/changed data).
* **`batch_status`**: Tracks execution state (`pending`, `success`, `failed`, `consumed`, `stopped`).

### State Flow and Traceability

1. **Creation:** A worker creates a batch in `pending` status.
2. **Execution:** The worker processes the assets. Every entity modified (Episode, Chapter, Transcript Line) is stamped with this `batch_id` via a Foreign Key constraint. This guarantees complete traceability of which worker/run created which piece of data.
3. **Success:** The worker marks the batch as `success` and fires a PostgreSQL `NOTIFY` event to wake up the downstream worker.
4. **Handoff:** The downstream worker wakes up, creates its own batch, and atomically re-points the episodes to its new `batch_id`, marking the previous batch as `consumed`.

> **Note:** Because every table includes a `batch_id` reference and `updated_at` timestamps, it is trivially easy to query the database to see exactly where a specific podcast episode is stuck in the processing pipeline.

---

## Vector Search (`pgvector`)

The `embeddings` table is designed to support scalable Semantic Search across the platform.

**Key Features:**

* **Polymorphic Design:** Strict `CHECK` constraints ensure an embedding belongs to exactly *one* level: a Podcast, an Episode, or a Chapter.
* **Storage Efficiency:** Uses `halfvec(2560)` to halve the storage footprint and memory requirements of the vectors without significantly impacting AI retrieval accuracy.
* **Fast Retrieval:** Indexed using Hierarchical Navigable Small World (`hnsw`) with cosine distance (`halfvec_cosine_ops`), enabling millisecond nearest-neighbor lookups even with millions of vectors.

---

## Data Integrity & Constraints

The schema heavily utilizes PostgreSQL's native constraints to prevent bad data from entering the system:

* **Timestamps:** Strict `CHECK (fin_ts >= start_ts)` and `CHECK (end_time >= start_time)` prevent temporal logic bugs.
* **Uniqueness:** `UNIQUE (podcast_id, guid)` prevents duplicate episodes, and `UNIQUE (chapter_id, line_idx)` guarantees transcript lines remain perfectly ordered.
* **Validation:** Scores and times are bounded (e.g., `CHECK (emotion_score >= 0 AND emotion_score <= 1)`).
