# Konfiguration, Middleware & Swagger

## Umgebungsvariablen

Die gesamte Konfiguration wird über Umgebungsvariablen gesteuert (`config/config.go`).
Es gibt keine Konfigurations-Dateien (kein YAML, kein JSON). Die `.env`-Datei im Projekt-Root
wird von Docker Compose geladen.

### Pflicht-Variablen

| Variable        | Beispiel                                                  | Bedeutung                                |
| --------------- | --------------------------------------------------------- | ---------------------------------------- |
| `POSTGRES_URL`  | `postgres://user:pass@postgres:5432/audiolens?sslmode=disable` | PostgreSQL Connection-String        |
| `MINIO_USER`    | `minioadmin`                                              | MinIO Access Key                         |
| `MINIO_PASS`    | `minioadmin`                                              | MinIO Secret Key                         |
| `GEMINI_API_KEY`| `AIza...`                                                 | Google Gemini API Key (für Chat + ggf. Embedding) |

Fehlt eine dieser Variablen, bricht der Prozess beim Start mit `log.Fatalf` ab.

### Optionale Variablen

| Variable              | Default                   | Bedeutung                                                     |
| --------------------- | ------------------------- | ------------------------------------------------------------- |
| `BACKEND_PORT`        | `:8080`                   | Server-Listen-Adresse (Format: `:port`)                       |
| `MINIO_ENDPOINT`      | `minio:9000`              | MinIO Host:Port                                               |
| `MINIO_USE_SSL`       | `false`                   | TLS für MinIO-Verbindung (`"true"` = aktiviert)               |
| `MINIO_BUCKET`        | `bronze`                  | MinIO-Bucket für Audio + Cover                                |
| `CORS_ORIGINS`        | `http://localhost:3000`   | Erlaubte Origins (kommasepariert)                              |
| `EMBEDDING_PROVIDER`  | `ollama`                  | Embedding-Provider: `ollama` oder `gemini`                    |
| `OLLAMA_URL`          | `http://ollama:11434`     | Ollama-Server-URL (nur bei `EMBEDDING_PROVIDER=ollama`)       |
| `EMBEDDING_MODEL`     | `qwen3-embedding:4b`     | Modellname für Embeddings                                     |
| `EMBEDDING_DIMENSION` | `2560`                    | Erwartete Vektor-Dimension (muss zur DB-Spalte passen)        |

### Config-Struct

```go
type Config struct {
    ServerPort         string      // ":8080"
    PostgresURL        string      // Pflicht
    MinioEndpoint      string      // "minio:9000"
    MinioUser          string      // Pflicht
    MinioPass          string      // Pflicht
    MinioUseSSL        bool        // false
    MinioBucket        string      // "bronze"
    CORSOrigins        []string    // ["http://localhost:3000"]
    EmbeddingProvider  string      // "ollama"
    OllamaURL          string      // "http://ollama:11434"
    EmbeddingModel     string      // "qwen3-embedding:4b"
    EmbeddingDimension int         // 2560
    GeminiAPIKey       string      // Pflicht
}
```

Hilfsfunktionen:
- `getEnvOrDefault(key, fallback)` — String mit Fallback.
- `getIntOrDefault(key, fallback)` — Int mit Fallback (bei Parse-Fehler wird der Fallback verwendet).

---

## Middleware

Das Backend verwendet drei Middleware-Funktionen, die in `main.go` registriert werden.

### 1) MaxBodySize

```go
r.Use(handlers.MaxBodySize(1 << 20))  // 1 MB
```

Begrenzt die maximale Request-Body-Größe auf **1 MB**. Schützt vor überdimensionierten Requests
(z. B. extrem lange Chat-Historien). Wird über `http.MaxBytesReader` implementiert — bei
Überschreitung gibt Go automatisch einen Fehler zurück.

### 2) CORS

```go
r.Use(cors.New(cors.Config{
    AllowOrigins:     cfg.CORSOrigins,
    AllowMethods:     []string{"GET", "POST", "OPTIONS"},
    AllowHeaders:     []string{"Origin", "Content-Type", "Accept"},
    ExposeHeaders:    []string{"Content-Length"},
    AllowCredentials: true,
    MaxAge:           12 * time.Hour,
}))
```

| Einstellung        | Wert                           | Bedeutung                                        |
| ------------------ | ------------------------------ | ------------------------------------------------ |
| `AllowOrigins`     | Aus `CORS_ORIGINS`             | Nur diese Origins dürfen Requests senden          |
| `AllowMethods`     | `GET, POST, OPTIONS`           | Kein PUT, DELETE, PATCH (rein lesende API + Chat) |
| `AllowCredentials` | `true`                         | Cookies/Auth-Header erlaubt                       |
| `MaxAge`           | `12h`                          | Preflight-Cache-Dauer (OPTIONS wird gecacht)      |

### 3) UUID-Validierung

```go
v1.GET("/episodes/:id", handlers.ValidateUUID("id"), h.GetEpisode)
```

Wird als **Middleware pro Route** eingesetzt, nicht global. Prüft mit `google/uuid.Parse`,
ob der `:id`-Parameter ein gültiges UUID-Format hat. Bei ungültigem Format:

```json
{
  "error": "invalid_id",
  "message": "Ungültiges UUID-Format.",
  "status": 400
}
```

Der Handler wird in diesem Fall **nicht** aufgerufen (`c.Abort()`).

```mermaid
flowchart TD
    REQ["Request mit :id"] --> VAL{"uuid.Parse(id) OK?"}
    VAL -->|ja| NEXT["c.Next() → Handler"]
    VAL -->|nein| ERR["400 invalid_id + c.Abort()"]
```

---

## Fehler-Handling

Das Backend verwendet zwei zentrale Fehler-Funktionen in `podcasts.go`:

### `respondError(c, status, errCode, message)`

Für erwartete Fehler (Validierung, nicht gefunden, Service down):

```go
respondError(c, http.StatusNotFound, "episode_not_found",
    "Episode mit dieser ID existiert nicht.")
```

### `respondInternalError(c, err)`

Für unerwartete Fehler (DB-Fehler, Scan-Fehler):

```go
respondInternalError(c, err)
// → loggt den Fehler mit Methode + Pfad
// → gibt 500 + "internal_error" + generische Fehlermeldung zurück
```

- Der tatsächliche Fehler wird **geloggt** (`log.Printf`), aber **nicht** an den Client
  zurückgegeben (kein Leaking von internen Details).

### `getEpisodeOrAbort(c, episodeID)`

Kombinations-Helper, der von fast jedem Handler verwendet wird:

```mermaid
flowchart TD
    A["getEpisodeOrAbort(c, id)"] --> B["Episodes.GetByID(ctx, id)"]
    B --> C{Fehler?}
    C -->|ja| D["respondInternalError → return nil"]
    C -->|nein| E{Episode nil?}
    E -->|ja| F["respondError 404 → return nil"]
    E -->|nein| G["return *Episode"]
```

---

## Swagger / API-Dokumentation

Die API-Dokumentation wird mit [swaggo/swag](https://github.com/swaggo/swag) generiert.

### Generierung

```bash
swag init -g cmd/server/main.go
```

Liest die Swagger-Annotationen aus den Handler-Funktionen (z. B. `// @Summary`, `// @Param`,
`// @Success`, `// @Router`) und generiert:

| Datei               | Inhalt                              |
| ------------------- | ----------------------------------- |
| `docs/docs.go`      | Go-Embedding der Swagger-Spec       |
| `docs/swagger.json`  | OpenAPI 2.0 Spec (JSON)             |
| `docs/swagger.yaml`  | OpenAPI 2.0 Spec (YAML)             |

### UI-Zugriff

```
http://localhost:8080/swagger/index.html
```

Registriert über:
```go
r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
```

### API-Metadaten (aus `main.go`)

```go
// @title           Audiolens API
// @version         1.0
// @description     Podcast analysis backend — episodes, chapters, transcripts, fact-checks, chat.
// @host            localhost:8080
// @BasePath        /api/v1
```

### Beispiel einer Swagger-Annotation

```go
// @Summary      Episode-Liste
// @Description  Cursor-based paginated episode list with optional free-text search.
// @Tags         episodes
// @Produce      json
// @Param        q       query     string  false  "Free-text search on title and podcast name"
// @Param        cursor  query     string  false  "Cursor from previous response"
// @Param        limit   query     int     false  "Page size (default 20, max 100)"
// @Success      200  {object}  model.EpisodeListResponse
// @Failure      400  {object}  model.ApiError
// @Router       /episodes [get]
```

---

## Docker

### Dockerfile (Multi-Stage)

```dockerfile
# Stage 1: Build
FROM golang:1.26.1-alpine AS builder
WORKDIR /app
RUN go install github.com/swaggo/swag/cmd/swag@v1.16.6
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN swag init -g cmd/server/main.go
RUN CGO_ENABLED=0 GOOS=linux go build -o server ./cmd/server/main.go

# Stage 2: Runtime
FROM alpine:3.20.9
WORKDIR /root/
COPY --from=builder /app/server .
EXPOSE 8080
CMD ["./server"]
```

| Aspekt                | Detail                                                      |
| --------------------- | ----------------------------------------------------------- |
| Builder-Image         | `golang:1.26.1-alpine`                                     |
| Runtime-Image         | `alpine:3.20.9` (minimal, ~5 MB)                           |
| Swagger im Build      | `swag` wird im Builder installiert und dort ausgeführt       |
| Static Binary         | `CGO_ENABLED=0` — kein C-Compiler im Runtime-Image nötig    |
| Finales Image         | Enthält nur das Go-Binary (~15-20 MB)                        |
| Port                  | 8080 (EXPOSE, tatsächlicher Port via `BACKEND_PORT`)         |

### Makefile

```makefile
GOPATH=$(shell go env GOPATH)
SWAG=$(GOPATH)/bin/swag

swag:           # Swagger-Docs generieren
    $(SWAG) init -g cmd/server/main.go

build: swag     # Swagger + Go-Binary bauen
    go build -o bin/server cmd/server/main.go

run: swag       # Swagger + direkt starten
    go run cmd/server/main.go
```

### Lokale Entwicklung

```bash
# Via Docker Compose (aus dem Projekt-Root)
docker compose up -d postgres minio backend

# Oder lokal (Go muss installiert sein)
cd src/backend
make run
```

Voraussetzung für lokale Entwicklung: `.env`-Datei im Projekt-Root mit allen Pflicht-Variablen
(siehe oben).
