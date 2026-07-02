FROM golang:1.26.1-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY 01_ingestion/ ./01_ingestion/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=linux go build -o ingestion 01_ingestion/cmd/ingestion/main.go
RUN CGO_ENABLED=0 GOOS=linux go build -o insertion 01_ingestion/cmd/insertion/main.go

FROM alpine:3.20.9 AS ingestion
WORKDIR /app
COPY  --from=builder /app/ingestion .
COPY --from=builder /app/01_ingestion/assets/ ./assets/
CMD ["./ingestion"]

FROM alpine:3.20.9 AS insertion
WORKDIR /app
COPY  --from=builder /app/insertion .
ENTRYPOINT ["./insertion"]
