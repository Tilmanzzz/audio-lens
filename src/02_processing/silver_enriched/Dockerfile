FROM python:3.13-slim-bookworm AS builder
WORKDIR /app
COPY --from=ghcr.io/astral-sh/uv:latest /uv /uvx /bin/
ENV UV_PROJECT_ENVIRONMENT=/app/.venv
ENV UV_COMPILE_BYTECODE=1
ENV UV_PYTHON=python3.13

# Copy workspace root, lockfile and member manifests
COPY pyproject.toml uv.lock ./
COPY 02_processing/silver/pyproject.toml 02_processing/silver/pyproject.toml
COPY 02_processing/silver_enriched/pyproject.toml 02_processing/silver_enriched/pyproject.toml

# Sync only the processing member's dependencies
RUN --mount=type=cache,target=/root/.cache/uv \
	uv sync --frozen --no-install-project --no-dev --package processing

FROM python:3.13-slim-bookworm AS runtime
WORKDIR /app

# Install ffmpeg for emotion_scoring
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg \
	&& rm -rf /var/lib/apt/lists/*

COPY --from=builder /app/.venv /app/.venv
ENV PATH="/app/.venv/bin:$PATH"

# Copy internal utils and the pipeline source
COPY internal/python/ /app/internal/python/
COPY 02_processing/common/ /app/02_processing/common/
COPY 02_processing/silver_enriched/ /app/02_processing/silver_enriched/
COPY 02_processing/*.py /app/02_processing/
COPY 02_processing/*.json /app/02_processing/

# Set working directory to the pipeline scripts location
WORKDIR /app/02_processing

# Environment variables
ENV PYTHONDONTWRITEBYTECODE=1
ENV PYTHONUNBUFFERED=1

ENTRYPOINT ["python", "silver_enriched/processing_pipeline/00_pipeline_processing_runner.py"]
