package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mmcdole/gofeed"
	"github.com/tilmanzzz/media-lens/internal/go/blob"
	"github.com/tilmanzzz/media-lens/internal/go/db"
)

type worker struct {
	store            *db.Store
	bronze           *blob.Bucket
	httpClient       *http.Client
	feedParser       *gofeed.Parser
	fallbackImageURL string
}

type triggerPayload struct {
	LoadMode string `json:"load_mode"`
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := setupWorker(ctx)
	defer w.store.Close()

	pgURL := os.Getenv("POSTGRES_URL")
	if pgURL == "" {
		log.Fatal("POSTGRES_URL environment variable is required")
	}

	go listenForTriggers(ctx, w, pgURL)

	log.Println("Ingestion worker started, actively listening for triggers...")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("Termination signal received. Shutting down ingestion worker...")
}

func listenForTriggers(ctx context.Context, w *worker, pgURL string) {
	for {
		err := listenLoop(ctx, w, pgURL)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("Listener error: %v. Reconnecting in 5s...", err)
			time.Sleep(5 * time.Second)
		}
	}
}

func listenLoop(ctx context.Context, w *worker, pgURL string) error {
	conn, err := pgx.Connect(ctx, pgURL)
	if err != nil {
		return fmt.Errorf("failed to connect for listening: %w", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, "LISTEN ingestion_ready")
	if err != nil {
		return fmt.Errorf("failed to execute LISTEN: %w", err)
	}

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = conn.Ping(ctx)
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			notification, err := conn.WaitForNotification(ctx)
			if err != nil {
				return err
			}

			var payload triggerPayload
			if err := json.Unmarshal([]byte(notification.Payload), &payload); err != nil {
				log.Printf("Invalid payload received: %s", notification.Payload)
				continue
			}

			mode := payload.LoadMode
			if mode != "full" && mode != "delta" {
				mode = "delta"
			}

			log.Printf("Received trigger. Starting ingestion cycle [mode: %s]", mode)
			runIngestionCycle(ctx, w, mode)
		}
	}
}

func runIngestionCycle(ctx context.Context, w *worker, loadMode string) {
	batchID, err := w.store.CreatePipelineBatch(ctx, "ingestion", loadMode)
	if err != nil {
		log.Printf("Failed to start batch: %v", err)
		return
	}
	fmt.Printf("Started pipeline batch [%s] mode: %s\n", batchID, loadMode)

	podcasts, err := w.store.GetPodcastsForIngestion(ctx, loadMode)
	if err != nil {
		log.Printf("Failed to fetch target podcasts: %v", err)
		_ = w.store.CompletePipelineBatch(ctx, batchID, "failed")
		return
	}

	var allEpisodes []db.Episode

	for _, p := range podcasts {
		fmt.Printf("Processing feed episodes: %s\n", p.FeedURL)

		eps, err := w.processPodcast(ctx, p, loadMode, batchID)
		if err != nil {
			log.Printf("Skipping podcast %s: %v", p.ID, err)
			continue
		}
		allEpisodes = append(allEpisodes, eps...)
	}

	if len(allEpisodes) > 0 {
		fmt.Printf("Flushing global batch: writing %d episodes...\n", len(allEpisodes))
		if err := w.flushEpisodes(ctx, allEpisodes); err != nil {
			log.Printf("Critical database flush failed: %v", err)
			_ = w.store.CompletePipelineBatch(ctx, batchID, "failed")
			return
		}
	} else {
		fmt.Println("No episodes required ingestion")
	}

	if err := w.store.CompletePipelineBatch(ctx, batchID, "success"); err != nil {
		log.Printf("Failed to close batch: %v", err)
		return
	}
	fmt.Printf("Successfully completed batch %s\n", batchID)
}

func setupWorker(ctx context.Context) *worker {
	store, err := db.NewStore(ctx, os.Getenv("POSTGRES_URL"))
	if err != nil {
		log.Fatalf("db connection failed: %v", err)
	}

	bronze, err := blob.NewBucket(
		os.Getenv("MINIO_ENDPOINT"),
		os.Getenv("MINIO_USER"),
		os.Getenv("MINIO_PASS"),
		"bronze",
	)
	if err != nil {
		log.Fatalf("minio connection failed: %v", err)
	}

	data, err := os.ReadFile("assets/fallback_cover.png")
	if err != nil {
		log.Fatalf("failed to read fallback cover: %v", err)
	}
	fallbackKey, err := bronze.UploadSystemAsset(ctx, "_system/cover/fallback-cover.png", "image/png", data)
	if err != nil {
		log.Fatalf("failed to upload fallback cover: %v", err)
	}

	return &worker{
		store:            store,
		bronze:           bronze,
		httpClient:       &http.Client{Timeout: 15 * time.Minute},
		feedParser:       gofeed.NewParser(),
		fallbackImageURL: fallbackKey,
	}
}

func (w *worker) processPodcast(ctx context.Context, p db.Podcast, loadMode, batchID string) ([]db.Episode, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", p.FeedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "AppleCoreMedia/1.0.0.19E266 (iPhone; U; CPU OS 15_4_1 like Mac OS X; en_us)")
	req.Header.Set("Accept", "application/rss+xml, application/xml, text/xml;q=0.9, */*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch feed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad http status: %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	xmlKey, err := w.bronze.UploadPodcastMetadata(
		ctx,
		p.ID,
		"metadata",
		"feed",
		"application/xml",
		bytes.NewReader(bodyBytes),
		int64(len(bodyBytes)),
		p.FeedURL,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to upload xml to minio: %w", err)
	}

	feed, err := w.feedParser.Parse(bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("feed parse error: %w", err)
	}

	if err := w.store.MarkPodcastIngested(ctx, p.ID, batchID, xmlKey); err != nil {
		return nil, fmt.Errorf("failed to link batch to podcast record: %w", err)
	}

	existingEps, err := w.store.GetEpisodeMap(ctx, p.ID)
	if err != nil {
		return nil, fmt.Errorf("failed fetching state map: %w", err)
	}

	items := feed.Items
	if p.MaxEpisodes != nil && len(items) > *p.MaxEpisodes {
		items = items[:*p.MaxEpisodes]
	}

	var processed []db.Episode
	for _, item := range items {
		ep, err := w.processEpisode(ctx, p, item, existingEps, loadMode, batchID)
		if err != nil {
			log.Printf("skipping item %s: %v", item.GUID, err)
			continue
		}
		if ep != nil {
			processed = append(processed, *ep)
		}
	}

	return processed, nil
}

func (w *worker) processEpisode(
	ctx context.Context,
	p db.Podcast,
	item *gofeed.Item,
	existingEps map[string]db.Episode,
	loadMode, batchID string,
) (*db.Episode, error) {
	if len(item.Enclosures) == 0 {
		return nil, nil
	}
	enclosureURL := item.Enclosures[0].URL

	episodeUpdated := extractEpisodeTimestamp(item)

	existingEp, exists := existingEps[item.GUID]
	isChanged := loadMode == "full" || !exists

	if !isChanged {
		if existingEp.EnclosureURL != enclosureURL {
			isChanged = true
		}
		if existingEp.SourceSystemUpdatedAt.Before(episodeUpdated) {
			isChanged = true
		}
	}

	if !isChanged {
		return nil, nil
	}

	if exists && existingEp.BatchID != "" {
		_ = w.store.StopPreviousBatchIfNeeded(ctx, existingEp.BatchID)
	}

	// Generate safe MinIO folder name
	storageFolderID := storageID(item.GUID)

	audioKey, err := w.uploadMedia(ctx, p.ID, storageFolderID, "audio", "original", enclosureURL, "audio/mpeg, audio/*;q=0.9, */*;q=0.8")
	if err != nil {
		return nil, fmt.Errorf("audio upload failed: %w", err)
	}

	var (
		coverKey string
		key      string
	)
	imageURL := extractImageURL(item)

	switch {
	case imageURL == "":
		coverKey = w.fallbackImageURL

	default:
		key, err = w.uploadMedia(
			ctx,
			p.ID,
			storageFolderID,
			"cover",
			"image",
			imageURL,
			"image/webp,image/apng,image/*,*/*;q=0.8",
		)

		if err != nil {
			log.Printf("warning: remote cover upload failed for %s, using fallback: %v", item.GUID, err)
			coverKey = w.fallbackImageURL
		} else {
			coverKey = key
		}
	}

	var duration *int
	if itunes, ok := item.Extensions["itunes"]; ok && len(itunes["duration"]) > 0 {
		duration = parseDuration(itunes["duration"][0].Value)
	}

	return &db.Episode{
		PodcastID:             p.ID,
		GUID:                  item.GUID,
		Title:                 item.Title,
		AudioKey:              audioKey,
		CoverKey:              coverKey,
		PublishedAt:           item.PublishedParsed,
		DurationSeconds:       duration,
		EnclosureURL:          enclosureURL,
		BatchID:               batchID,
		SourceSystemUpdatedAt: &episodeUpdated,
	}, nil
}

func (w *worker) uploadMedia(
	ctx context.Context,
	podcastID, episodeGUID, assetType, filename, url, acceptHeader string,
) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("User-Agent", "AppleCoreMedia/1.0.0.19E266 (iPhone; U; CPU OS 15_4_1 like Mac OS X; en_us)")
	req.Header.Set("Accept", acceptHeader)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bad http status: %d", resp.StatusCode)
	}

	return w.bronze.UploadAsset(
		ctx,
		podcastID,
		episodeGUID,
		assetType,
		filename,
		resp.Header.Get("Content-Type"),
		resp.Body,
		resp.ContentLength,
		url,
	)
}

func (w *worker) flushEpisodes(ctx context.Context, eps []db.Episode) error {
	_, err := w.store.BulkUpsertEpisodes(ctx, eps)
	return err
}

func extractEpisodeTimestamp(item *gofeed.Item) time.Time {
	if item.UpdatedParsed != nil {
		return item.UpdatedParsed.Truncate(time.Second)
	}
	if item.PublishedParsed != nil {
		return item.PublishedParsed.Truncate(time.Second)
	}

	rawDate := item.Updated
	if rawDate == "" {
		rawDate = item.Published
	}

	if rawDate != "" {
		formats := []string{
			time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822,
			time.RFC3339, time.RFC3339Nano,
			"Mon, 2 Jan 2006 15:04:05 -0700",
			"2006-01-02T15:04:05-0700",
		}

		for _, format := range formats {
			if parsed, err := time.Parse(format, strings.TrimSpace(rawDate)); err == nil {
				return parsed.Truncate(time.Second)
			}
		}
		log.Printf("[Warning] Failed to parse custom date format: %s", rawDate)
	}

	return time.Unix(0, 0)
}

func extractImageURL(item *gofeed.Item) string {
	if item.Image != nil {
		return item.Image.URL
	}
	if itunes, ok := item.Extensions["itunes"]; ok {
		if img, ok := itunes["image"]; ok && len(img) > 0 {
			return img[0].Attrs["href"]
		}
	}
	return ""
}

func parseDuration(val string) *int {
	if val == "" {
		return nil
	}
	parts := strings.Split(val, ":")
	var total int
	if len(parts) == 3 {
		h, _ := strconv.Atoi(parts[0])
		m, _ := strconv.Atoi(parts[1])
		s, _ := strconv.Atoi(parts[2])
		total = h*3600 + m*60 + s
	} else if len(parts) == 2 {
		m, _ := strconv.Atoi(parts[0])
		s, _ := strconv.Atoi(parts[1])
		total = m*60 + s
	} else {
		total, _ = strconv.Atoi(val)
	}

	if total == 0 {
		return nil
	}
	return &total
}

// converts any arbitrary string (like a URL-based GUID)
// into a clean, 64-character hex string safe for MinIO/S3 object keys.
func storageID(guid string) string {
	hash := sha256.Sum256([]byte(guid))
	return hex.EncodeToString(hash[:])
}
