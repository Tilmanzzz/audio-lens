# Backend - Übersicht

> Diese Doku-Reihe beschreibt das **Backend** im `media-lens`-Projekt.
> Es liegt im Code unter [`src/backend/`](../../src/backend/).

## Was ist das Backend?

Das Backend ist die HTTP-API-Schicht der Media-Lens-Plattform. Es nimmt die von der
Processing-Pipeline (Silver Enriched) angereicherten Daten aus PostgreSQL und stellt sie dem
Frontend als REST-API bereit. Zusätzlich bietet es:

- Cursor-basierte Paginierung und Volltextsuche für Episoden
- Semantische Suche über Vektor-Embeddings (pgvector)
- LLM-gestützten Chat über Episoden-Transkripte (Gemini)
- Audio-Streaming mit Range-Request-Support (Proxy zu MinIO)
- Server-Sent Events für Playback-Synchronisation
- Health-Checks für alle angebundenen Dienste

## Wo befinden wir uns in der Gesamt-Pipeline?

```mermaid
flowchart LR
    subgraph S1["01_ingestion"]
        ING[Download / Aufnahme von Episoden]
    end
    subgraph S2["02_processing"]
        TR[transcription<br/>Audio → Text]
        SEC[sectioning<br/>Text → Kapitel]
        SE[silver_enriched<br/>Anreicherung]
    end
    subgraph S3["03_backend"]
        BE["Go REST API<br/>(Gin + Swagger)"]
    end
    subgraph S4["04_frontend"]
        FE["Next.js Web-UI"]
    end

    ING --> TR --> SEC --> SE --> BE --> FE
```

- **Silver Enriched** schreibt Zusammenfassungen, Fakten-Checks, Embeddings und Emotionsdaten
  in PostgreSQL / pgvector.
- **Das Backend** liest diese Daten und stellt sie über eine typisierte REST-API bereit.
  Es schreibt **nicht** in die Datenbank (rein lesend), führt aber zur Laufzeit eigene
  Embedding- und LLM-Aufrufe durch (für semantische Suche und Chat).
- **Das Frontend** konsumiert ausschließlich die Backend-API.

## Inhalt dieser Doku-Reihe

| Datei                                        | Inhalt                                                          |
| -------------------------------------------- | --------------------------------------------------------------- |
| [01_architecture.md](01_architecture.md)     | Projektstruktur, Schichten-Architektur, Startup & Shutdown      |
| [02_api_endpoints.md](02_api_endpoints.md)   | Alle API-Routen im Detail: Parameter, Responses, Status-Codes   |
| [03_database.md](03_database.md)             | Datenmodelle, Repositories, SQL-Queries, Connection-Pooling     |
| [04_services.md](04_services.md)             | Externe Dienste: Embedder, LLM, VectorStore, MinIO              |
| [05_configuration.md](05_configuration.md)   | Umgebungsvariablen, Middleware, Swagger, Docker                  |

## Tech Stack

| Komponente       | Technologie                                          |
| ---------------- | ---------------------------------------------------- |
| Sprache          | Go 1.26                                              |
| HTTP-Framework   | Gin v1.12                                            |
| API-Doku         | Swagger (swaggo/swag + gin-swagger)                  |
| Datenbank        | PostgreSQL 16 (lib/pq)                               |
| Vektor-Suche     | pgvector (pgvector-go)                               |
| Object Storage   | MinIO (minio-go/v7)                                  |
| Embedding        | Ollama (HTTP) oder Gemini (google.golang.org/genai)  |
| LLM (Chat)       | Gemini (gemini-2.5-flash-lite)                       |
| CORS             | gin-contrib/cors                                     |

## High-Level-Architektur

```mermaid
flowchart LR
    FE[Frontend] -->|HTTP| GIN[Gin Router]
    GIN --> H[Handler]
    H --> R[(Repository<br/>PostgreSQL)]
    H --> VS[(VectorStore<br/>pgvector)]
    H --> EMB[Embedder<br/>Ollama / Gemini]
    H --> LLM[LLM Client<br/>Gemini]
    H --> S3[MinIO<br/>Object Storage]
```

---

> Hinweis: Diese Dokumentation wurde mit der Unterstützung von KI (Claude Sonnet 4.6) geschrieben.
