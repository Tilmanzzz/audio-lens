# Overview

The `src/internal/go/` directory contains the shared data access layer for the Go-based Ingestion modules. It abstracts away the complexities of PostgreSQL connection pooling, database row mapping, and MinIO object storage routing, providing a clean API for the worker services.

> Note: This Documentation was written partially using AI (Gemini 3.1 Pro).

**Stack:**
- Go (Golang)
- `pgx/v5` & `scany` (for fast database row-to-struct mapping)
- `minio-go/v7` (for object storage)

---

## Directory Structure

```text
src/internal/go/
├── blob/
│   └── minio.go       # MinIO object storage wrapper
└── db/
    ├── episodes.go    # Episode read/write & bulk upserts
    ├── models.go      # Go structs for DB mapping
    ├── podcasts.go    # Podcast sync & pipeline batch management
    └── store.go       # pgxpool connection wrapper

```

---

## Data Models (`models.go`)

The Go layer relies on standard structs mapped to SQL rows using the `scany` library.

| Type | Description |
| --- | --- |
| `Podcast` | Show-level metadata (`Title`, `FeedURL`, `MaxEpisodes`) and remote timestamps. |
| `Episode` | The actual media item. Tracks object keys (`AudioKey`, `CoverKey`) and pipeline batch assignments. |

---

## Core Logic and Data Flow

### `blob/minio.go` – Object Storage

Abstracts the MinIO client and enforces deterministic, entity-first storage paths. It ensures that files are stored predictably based on their parent entities rather than random UUIDs.

| Function | Description |
| --- | --- |
| `UploadPodcastMetadata` | Saves show-level XML feeds to `{podcast_id}/metadata/feed.xml`. |
| `UploadAsset` | Saves episode media grouped by hashed GUID to `{podcast_id}/{episode_guid}/audio/{filename}.mp3`. |
| `UploadSystemAsset` | Uploads fixed platform assets (like fallback covers) to a deterministic key. |
| `extensionFromContentType` | Automatically detects `.mp3`, `.m4a`, `.jpg`, etc., based on HTTP headers. |

### `db/*.go` – Database Store

A highly optimized wrapper around a `pgxpool.Pool`, designed to handle the heavy throughput of the ingestion phase.

| Function | Description |
| --- | --- |
| `GetEpisodeMap` | Pre-fetches all episodes for a podcast into a fast memory map to diff remote RSS feeds efficiently. |
| `BulkUpsertEpisodes` | Uses `ON CONFLICT (guid) DO UPDATE` to safely insert or update dozens of episodes in a single query. |
| `UpdateMaxEpisodesIfHigher` | Ensures the `max_episodes` limit is only ever increased, preventing accidental downgrades. |
| `CreatePipelineBatch` | Initializes a new workload tracking row in the `pipeline_batches` table. |
| `CompletePipelineBatch` | Marks a batch as `success` and fires a `pg_notify('transcription_ready', ...)` trigger to wake up downstream Python workers. |

---

## Implementation Details

**Idempotency:** The database layer heavily relies on `ON CONFLICT DO UPDATE` (Upserts). If the ingestion worker crashes halfway through a download, restarting it will safely overwrite the existing DB rows without creating duplicates.

**Event Broadcasting:** The Go code never calls the Python microservices directly. Instead, it writes to the database and uses PostgreSQL's `SELECT pg_notify(...)` to publish events, keeping the microservices completely decoupled.

