# Overview

The ingestion pipeline is the central entry point of our podcast platform. Written in Go, it handles discovering new podcasts, regularly polling for updates, and downloading the actual audio and image assets into our data lake.

The pipeline is entirely event-driven and consists of two asynchronous modules that communicate with each other via PostgreSQL channels (`LISTEN`/`NOTIFY`).

> Note: This Documentation was written partially using AI (Gemini 3.1 Pro).

**Stack:**

* Go (Golang)
* PostgreSQL (via `pgx/v5`)
* MinIO (S3-compatible Object Storage)
* `gofeed` (RSS Parsing)

---

## Directory Structure

```text
src/01_ingestion/
├── cmd/
│   ├── ingestion/
│   │   └── main.go       # Worker for media downloads & episode tracking
│   └── insertion/
│       └── main.go       # Crawler for discovery & metadata polling
├── Dockerfile            # Container definition for both modules
└── assets/
    └── fallback_cover.png # Default cover if remote images fail

```

---

## Module 1: Insertion (Discovery & Polling)

The `insertion` module acts as the radar of our platform. It looks for new podcast feeds, registers them in the database, and checks known feeds for updates. As soon as it detects new content, it wakes up the ingestion worker.

### Core Processes

This module runs three parallel goroutines:

1. **Notification Listener:**
* Listens on the PostgreSQL channel `podcast_insert_request`.
* Accepts JSON payloads like `{"url": "...", "max_episodes": X}` (e.g., triggered by the frontend or admins).
* Fetches the feed metadata, saves it to the database, and immediately triggers an ingestion cycle.


2. **Discovery Loop (Currently every 3 minutes):**
* Iterates through a static seed list of known podcasts (or CLI arguments).
* Compares the feeds against the database and inserts missing podcasts.


3. **Polling Loop (Currently every minute):**
* Fetches all active podcasts from the database.
* Parses the remote RSS feed and checks the latest update timestamps (`UpdatedParsed` / `PublishedParsed`).
* Syncs podcast metadata (title, description, hosts) if the publisher made changes.



### Data & Message Flow

* **Input:** RSS URLs (via CLI, hardcoded seeds, or PG-NOTIFY).
* **Processing:** Parses the XML structure using `gofeed`, strips HTML tags, and extracts hosts.
* **Output:** Executes `INSERT` / `UPDATE` in the database. Finally, it fires a `pg_notify('ingestion_ready', '{"load_mode": "delta"}')` event to wake up the downloader.

---

## Module 2: Ingestion (Media Processing)

The `ingestion` module is the heavy lifter. It downloads the actual files, secures them in our own storage, and extracts detailed episode data.

### How It Works

The worker sleeps until it receives a ping via the `ingestion_ready` channel. Once awakened, it starts an ingestion cycle:

1. **Batching:** Creates a new pipeline batch in the database for error tracking and observability.
2. **XML Backup:** Fetches the RSS feed and immediately uploads the raw XML file to the MinIO `bronze` bucket.
3. **Episode Diffing:** Compares the feed items with already stored episodes (matching by `GUID`, `EnclosureURL`, and timestamps). Unchanged episodes are skipped.
4. **Media Download:**
* Generates a safe SHA256 hash from the episode GUID to use as the storage folder path.
* Downloads the audio file (`.mp3`, `.m4a`) and stores it in MinIO.
* Downloads the episode cover. If the download fails, it uses a pre-uploaded fallback image.


5. **Database Flush:** Batches all new and updated episodes and writes them to the database efficiently via a bulk upsert.

### Edge Cases & Reliability

* **Network Timeouts:** The HTTP client uses a hard 15-minute timeout to handle large audio files gracefully.
* **Missing Images:** If a remote cover cannot be fetched, the `fallback_cover.png` stored in MinIO is automatically linked.
* **Crash Recovery:** If a batch fails (e.g., due to a dropped database connection), the pipeline batch record in Postgres is marked as `failed`.

---

## Message Flow Architecture

The two modules are completely decoupled using PostgreSQL as a message broker:

| Component | Action | PostgreSQL Channel | Payload | Target |
| --- | --- | --- | --- | --- |
| Frontend / API | Submits new feed URL | `podcast_insert_request` | `{"url": "...", "max_episodes": 5}` | Insertion Module |
| Insertion Module | Signals new data is ready | `ingestion_ready` | `{"load_mode": "delta"}` | Ingestion Module |

> **Note:** Because of this decoupled architecture, the ingestion workers can be horizontally scaled as needed. The insertion module can continuously crawl feeds without being blocked by large, time-consuming media downloads.

### Manual Full Load Mode for the Ingestion Module



By default, the insertion module sends a `"delta"` load payload, which tells the ingestion worker to only process new or updated episodes. However, the ingestion worker also supports a `"full"` load mode (`{"load_mode": "full"}`). This forces the worker to bypass the timestamp/URL caching checks and re-ingest all episodes



Currently, triggering a full load must be done manually. You can signal the worker in two ways:

**Option 1: Using an SQL Tool (DBeaver, pgAdmin, DataGrip, etc.)**  

Run the following SQL command:
```
SELECT pg_notify('ingestion_ready', '{"load_mode": "full"}');
```

**Option 2: Using Docker Exec**
Run this command directly in your terminal (replace the placeholders with your actual container name, database user, and database name):
```bash
docker exec -it <postgres_container_name> psql -U <postgres_user> -d <postgres_db> -c "SELECT pg_notify('ingestion_ready', '{\"load_mode\": \"full\"}');"
```
---

## Environment Variables

Both modules require database access. The ingestion module additionally needs credentials for the MinIO object storage.

| Variable | Required By | Description |
| --- | --- | --- |
| `POSTGRES_URL` | Both | Connection string for the PostgreSQL database |
| `MINIO_ENDPOINT` | Ingestion | URL of the MinIO/S3 server |
| `MINIO_USER` | Ingestion | Access Key / Username for MinIO |
| `MINIO_PASS` | Ingestion | Secret Key / Password for MinIO |
