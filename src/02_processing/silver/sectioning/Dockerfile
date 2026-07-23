FROM python:3.13-slim-bookworm AS builder
WORKDIR /app
COPY --from=ghcr.io/astral-sh/uv:latest /uv /uvx /bin/
ENV UV_PROJECT_ENVIRONMENT=/app/.venv
ENV UV_COMPILE_BYTECODE=1
ENV UV_PYTHON=python3.13

# lockfile and member manifests needed to resolve the workspace
COPY pyproject.toml uv.lock ./
COPY 02_processing/silver/pyproject.toml 02_processing/silver/pyproject.toml
COPY 02_processing/silver_enriched/pyproject.toml 02_processing/silver_enriched/pyproject.toml
RUN --mount=type=cache,target=/root/.cache/uv \
	uv sync --frozen --no-install-project --no-dev --package preprocessing

FROM python:3.13-slim-bookworm AS runtime
WORKDIR /app
RUN groupadd -r appuser && useradd -r -g appuser appuser
COPY --from=builder /app/.venv /app/.venv
ENV PATH="/app/.venv/bin:$PATH"
COPY internal/python/ /app/internal/python/
COPY 02_processing/common/ /app/common/
COPY 02_processing/silver/sectioning/main.py /app/main.py
ENV PYTHONDONTWRITEBYTECODE=1
ENV PYTHONUNBUFFERED=1
CMD ["python", "main.py"]
