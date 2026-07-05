# Deployment Architecture & Guide

This documentation details the containerization and deployment architecture of the `media-lens` platform. It also features a deployment guide. The infrastructure uses a fully containerized microservice approach across both development (`docker-compose.dev.yml`) and production (`docker-compose.prod.yml`) environments.

## Table of Contents

* [Deployment Strategies](#deployment-strategies)
* [Service Matrix](#service-matrix)
* [Container Blueprints](#container-blueprints)
* [Dev vs Prod Strategy](#dev-vs-prod-strategy)
* [Implementation Details](#implementation-details)
* [CI/CD Architecture](#ci-cd-architecture)
* [Deployment Guide](#deployment-guide)
* [Initial Server Setup](#initial-server-setup)
* [Environment Configuration](#environment-configuration)
* [Starting the Platform](#starting-the-platform)
* [Applying Updates](#applying-updates)



---

## Deployment Strategies

The architecture relies on four main containerization patterns:

* **Multi-Stage Builds:** Minimizes final production image footprints by isolating compiler toolchains, package managers, and temporary caches (`uv`, `go build`, `npm run build`) strictly within build stages.
* **UV Workspace Dependency Sync:** Python containers pull the global workspace lockfiles and selectively build target project packages (e.g., `--package preprocessing` or `--package processing`) utilizing high-speed Docker layer caching (`--mount=type=cache`).
* **Semantic Healthchecks:** Orchestration order is maintained using explicit health conditions (`condition: service_healthy`). Downstream services block initialization until PostgreSQL and MinIO are fully operational.
* **Targeted CLI Packaging:** The Go Dockerfile multi-targets separate binaries (`ingestion` vs `insertion`) out of the same build context using multi-stage multi-target options.

---

## Service Matrix

The deployment landscape maps platform requirements directly to dedicated network components:

| Service Name | Image Source / Context | Port / Volumes | Purpose |
| --- | --- | --- | --- |
| **`postgres`** | `pgvector/pgvector:pg16` | `5432:5432` / `postgres_data` | Core database with vector capabilities. Mounts and executes `init_v2.sql` on first startup. |
| **`minio`** | `minio/minio` | `9000:9000` (API) / `9001:9001` (Console) | S3 asset storage. Configures CORS rules for platform client boundaries. |
| **`pgadmin`** | `dpage/pgadmin4` | `5050:80` | Web-based database management interface. |
| **`ingestion`** | `src/01_ingestion/Dockerfile` (Target: `ingestion`) | None | Persistent Go worker downloading media assets and caching raw XML feeds. |
| **`insertion`** | `src/01_ingestion/Dockerfile` (Target: `insertion`) | None (Profile: `tools`) | Go CLI crawler syncing discovery seeds. |
| **`transcription`** | `src/02_processing/silver/transcription/Dockerfile` | `./logs/transcription` | Python microservice dispatching jobs to Whisper. |
| **`sectioning`** | `src/02_processing/silver/sectioning/Dockerfile` | `./logs/sectioning` | Python microservice orchestrating NLTK tokenization and Gemini LLM analysis. |
| **`processing`** | `src/02_processing/silver_enriched/Dockerfile` | `./logs/processing` | Long-running Python worker handling pipeline aggregation on a `--poll` interval. |
| **`backend`** | `src/backend` | `8080:8080` | Core Go REST/WebSocket server serving client traffic. |
| **`frontend`** | `src/frontend` | `3000:3000` | Next.js server handling user interaction. |

---

## Container Blueprints

### Go Applications (`ingestion`, `insertion`, `backend`)

* **Build Engine:** Compiles inside `golang:1.26.1-alpine` using explicit system flags (`CGO_ENABLED=0 GOOS=linux`) to guarantee statically linked, zero-overhead binaries.
* **Runtime:** Moves exclusively into thin `alpine:3.20.9` environments. The backend injection stage automatically bootstraps documentation dependencies using `swag init`.

### Python Microservices (`transcription`, `sectioning`, `processing`)

* **Build Engine:** Uses a specialized builder stage fetching Astral `uv` directly from its official registry image to synchronize locks smoothly (`uv sync --frozen --no-dev`).
* **Runtime Constraints:** Both `transcription` and `processing` load custom runtime layers based on Debian `slim-bookworm` to install system-level multimedia utilities (`ffmpeg`) required for media file decoding. Python files enforce unbuffered execution streams via `PYTHONUNBUFFERED=1`.

### Next.js Frontend (`frontend`)

* **Development Strategy (`Dockerfile.dev`):** Maps package manifests and executes standard runtime managers directly over the context (`npm run dev`) to shorten reload feedback loops.
* **Production Strategy (`Dockerfile`):** Implements a strict 3-stage flow (`deps` $\rightarrow$ `builder` $\rightarrow$ `runner`), copying layout artifacts into standalone spaces and dropping privileges to a safe system account (`nextjs:nodejs`).

---

## Dev vs Prod Strategy

### `docker-compose.dev.yml`

* Built directly on-demand using local contextual source files (`build: context: ...`).
* Mounts host logging folders directly (`./logs:/app/logs`) to allow local inspection.
* Exposes direct system configuration overrides using explicit internal environment references.

### `docker-compose.prod.yml`

* Relies on immutable, pre-compiled remote images fetched securely from the GitHub Container Registry (`ghcr.io/tilmanzzz/media-lens-*`).
* Removes diagnostic volume mappings and system tools to protect application scopes.
* Activates production process monitors via persistent recovery guidelines (`restart: unless-stopped`).

---

## CI/CD Architecture

**Image Build & Registry**: Triggered on pushes to the main branch. This workflow leverages Docker Buildx to compile the microservices and build optimized, multi-stage Docker images. The resulting images are tagged and pushed to the GitHub Container Registry (GHCR).

* **Note on caching:** We use the GitHub Actions cache (type=gha, mode=max) to speed up builds by storing intermediate layers. Each service is assigned a unique scope key so the parallel builds don't overwrite each other's cache data.

**Deployment Payload Updates**: Triggered only when infrastructure or configuration files change (e.g., `docker-compose.prod.yml`, storage directories). This workflow aggregates the production compose file, database initialization scripts, and provisions necessary volume directories. It then force-pushes this payload as a single commit to the orphan `deploy` branch, ensuring the production server pulls only the necessary configuration state.

## AudioLens Deployment Guide

### Initial Server Setup

For the first deployment, create a dedicated directory on your server and pull this orphan branch directly:

```bash
mkdir audiolens-deploy
cd audiolens-deploy
git init
git remote add origin https://github.com/tilmanzzz/media-lens.git
git fetch origin deploy
git checkout -t origin/deploy

```

### Environment Configuration

Create a `.env` file in the root of the `audiolens-deploy` directory. The services require your database credentials, MinIO credentials, Gemini API key, and frontend public URL to start successfully. Ensure your `.gitignore` includes the `.env` file so it is never committed.

### Starting the Platform

Once the environment variables are set, pull the latest images and start the container stack:

```bash
docker compose pull
docker compose up -d

```

### Applying Updates

When new code is merged into the main branch, the GitHub Actions workflow automatically builds new images and updates the configurations on this branch. To apply these updates to the running server, execute:

```bash
git fetch origin
git reset --hard origin/deploy
docker compose pull
docker compose up -d

```
