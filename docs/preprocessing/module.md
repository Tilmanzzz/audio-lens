# Overview

The preprocessing pipeline (the "Silver" layer) sits right behind the ingestion phase. Written in Python, it transforms raw media files into structured text and divides them into logical chapters.

This stage is entirely asynchronous and event-driven, leveraging `asyncio` and PostgreSQL notifications (`LISTEN`/`NOTIFY`) to process workloads dynamically. It relies on external AI models—a Whisper API for speech-to-text and Google's Gemini for semantic text analysis.


> Note: This Documentation was written partially using AI (Gemini 3.1 Pro).

**Stack:**

* Python 3
* PostgreSQL (via `asyncpg`)
* MinIO (S3-compatible Object Storage)
* External APIs: Whisper API (Audio) & Google Gemini (LLM)
* `nltk` (Natural Language Toolkit)

---

## Folder Structure

```text
src/02_processing/
├── common/
│   ├── app_logger.py       # Standardized structured logging
│   └── db_connector.py     # Shared database connection utilities
├── silver/
│   ├── pyproject.toml      # Shared Python dependencies
│   ├── sectioning/
│   │   ├── Dockerfile
│   │   └── main.py         # Worker for AI-driven chapter generation
│   └── transcription/
│       ├── Dockerfile
│       └── main.py         # Worker for audio-to-text conversion

```

---

## Module 1: Transcription

The `transcription` module acts as the bridge between raw audio and text. It listens for new media arrivals, downloads the audio, and delegates the heavy lifting to a Whisper API instance.

### Core Processes

1. **Event Listening & Backlog:** Listens on the `transcription_ready` PG channel. On startup, it also sweeps the database for any successful ingestion batches that were missed while the worker was offline.
2. **Audio Fetching:** Streams the raw audio file (`.mp3` / `.m4a`) from the MinIO `bronze` bucket into a temporary local file.
3. **API Integration:** Sends the audio file to a remote Whisper API via an HTTP multipart form request, requesting a verbose JSON response to get word-level timestamps.
4. **Data Standardization:** Parses the Whisper output and normalizes it into a standard JSON schema containing metadata, full text, and word-level segment data.
5. **Storage:** Uploads the standardized JSON document directly to the MinIO `silver` bucket and links the path (`transcript_key`) to the episode in the database.

> **Note:** Audio processing can take time. The worker uses a robust 1-hour HTTP timeout (`aiohttp.ClientTimeout`) to prevent dropped connections during long podcast transcriptions.

---

## Module 2: Sectioning

The `sectioning` module takes the raw transcript and turns it into an interactive, readable format. It breaks the text down into precise sentences and uses LLMs to group them into logical, thematic chapters.

### Core Processes

1. **Transcript Parsing:** Downloads the newly created transcript JSON from the MinIO `silver` bucket.
2. **Sentence Tokenization:** Uses `nltk.sent_tokenize` to split the text into grammatically correct sentences. It calculates exact start and end timestamps for each sentence by mathematically interpolating character positions from the Whisper segment times.
3. **LLM Chapter Generation:**
* Feeds the formatted, ID-tagged sentences into **Gemini 3.5 Flash** (via the `google.genai` SDK).
* Prompts the LLM to identify distinct topic shifts and generate concise (1-3 word) chapter titles.
* Enforces structured JSON output (`ChapterList` Pydantic schema) to ensure reliable application parsing.


4. **Database Write:** Uses a high-performance, transactional bulk insert (`executemany`) to save both the generated chapters and the individual sentence lines into the database.
5. **Concurrency:** Limits active processing to 5 simultaneous episodes using an `asyncio.Semaphore` to prevent overwhelming the LLM API or local memory.

---

## Message Flow Architecture

Just like the ingestion layer, the processing modules are loosely coupled using PostgreSQL as a message broker:

| Component | Action | PostgreSQL Channel | Payload | Target |
| --- | --- | --- | --- | --- |
| Ingestion Module | Signals media is downloaded | `transcription_ready` | `{"load_mode": "delta", "batch_id": "..."}` | Transcription Worker |
| Transcription Worker | Signals JSON is ready | `segmenting_ready` | `{"load_mode": "delta", "batch_id": "..."}` | Sectioning Worker |
| Sectioning Worker | Signals chapters are ready | `processing_ready` | `{"load_mode": "delta", "batch_id": "..."}` | Downstream (Gold Layer) |

### Manual Overrides (Full Load Mode)

By default, the pipeline sends a `"delta"` load payload, which tells the worker to only process the specific batch of newly arrived episodes. However, both workers also support a `"full"` load mode (`{"load_mode": "full"}`). This forces the worker to bypass the batch IDs, query the database for all applicable episodes, and re-process the entire workload

Currently, triggering a full load must be done manually. You can signal the workers using either an SQL tool or directly via Docker:

**Option 1: Using an SQL Tool (DBeaver, pgAdmin, DataGrip, etc.)**

Run the following SQL command (swap the channel name to target the specific worker):

```sql
-- To trigger Transcription for all missing audio:
SELECT pg_notify('transcription_ready', '{"load_mode": "full"}');

-- To trigger Sectioning for all un-chaptered transcripts:
SELECT pg_notify('segmenting_ready', '{"load_mode": "full"}');

```

**Option 2: Using Docker Exec**

Run this command directly in your terminal (replace the placeholders with your actual container name, database user, and database name):

```bash
docker exec -it <postgres_container_name> psql -U <postgres_user> -d <postgres_db> -c "SELECT pg_notify('transcription_ready', '{\"load_mode\": \"full\"}');"

```

---

## Environment Variables

Both modules require database and MinIO credentials. Additionally, external API keys and endpoint URLs are necessary for the AI integrations.

| Variable | Required By | Description |
| --- | --- | --- |
| `POSTGRES_URL` | Both | Connection string for the PostgreSQL database (`asyncpg` format) |
| `MINIO_ENDPOINT` | Both | URL of the MinIO/S3 server (e.g., `localhost:9000`) |
| `MINIO_USER` | Both | Access Key / Username for MinIO |
| `MINIO_PASS` | Both | Secret Key / Password for MinIO |
| `WHISPER_API_URL` | Transcription | Full HTTP endpoint to the Whisper transcription service |
| `GEMINI_API_KEY` | Sectioning | Google API key for Gemini inference (implicitly read by the GenAI SDK) |
| `LOG_LEVEL` | Both | Minimum logging severity (e.g., `INFO`, `DEBUG`) |

