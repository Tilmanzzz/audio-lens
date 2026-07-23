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

// worker bundles the shared dependencies every ingestion cycle needs:
// db access, the bronze bucket, an http client, a feed parser, and the
// key of the fallback cover used when an episode has no usable image.
type worker struct {
	store            *db.Store
	bronze           *blob.Bucket
	httpClient       *http.Client
	feedParser       *gofeed.Parser
	fallbackImageURL string
}

// triggerPayload is the json shape carried by an "ingestion_ready" notification.
type triggerPayload struct {
	LoadMode string `json:"load_mode"`
}

// main boots the worker, starts the notification listener, and blocks
// until an interrupt or termination signal arrives.
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

// listenForTriggers keeps the notification listener alive, reconnecting
// with a fixed backoff whenever the underlying connection drops.
func listenForTriggers(ctx context.Context, w *worker, pgURL string) {
	for {
		if err := listenLoop(ctx, w, pgURL); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("Listener error: %v. Reconnecting in 5s...", err)
			time.Sleep(5 * time.Second)
		}
	}
}

// listenLoop opens a dedicated connection, subscribes to "ingestion_ready",
// and runs an ingestion cycle for every valid notification. It returns on
// context cancellation (nil) or any connection error (so the caller can retry).
func listenLoop(ctx context.Context, w *worker, pgURL string) error {
	conn, err := pgx.Connect(ctx, pgURL)
	if err != nil {
		return fmt.Errorf("failed to connect for listening: %w", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, "LISTEN ingestion_ready"); err != nil {
		return fmt.Errorf("failed to execute LISTEN: %w", err)
	}

	// keep the connection alive while idle
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

			// default to a delta run for anything that isn't explicitly "full"
			mode := payload.LoadMode
			if mode != "full" && mode != "delta" {
				mode = "delta"
			}

			log.Printf("Received trigger. Starting ingestion cycle [mode: %s]", mode)
			runIngestionCycle(ctx, w, mode)
		}
	}
}

// runIngestionCycle processes every target podcast for one batch: it opens a
// pipeline batch, fetches and ingests each feed, flushes the collected episodes
// in one write, then marks the batch success or failed.
func runIngestionCycle(ctx context.Context, w *worker, loadMode string) {
	batchID, err := w.store.CreatePipelineBatch(ctx, "ingestion", loadMode)
	if err != nil {
		log.Printf("Failed to start batch: %v", err)
		return
	}
	log.Printf("Started pipeline batch [%s] mode: %s", batchID, loadMode)

	podcasts, err := w.store.GetPodcastsForIngestion(ctx, loadMode)
	if err != nil {
		log.Printf("Failed to fetch target podcasts: %v", err)
		_ = w.store.CompletePipelineBatch(ctx, batchID, "failed")
		return
	}

	// gather episodes across all feeds first, then flush once at the end
	var allEpisodes []db.Episode
	for _, p := range podcasts {
		log.Printf("Processing feed episodes: %s", p.FeedURL)

		eps, err := w.processPodcast(ctx, p, loadMode, batchID)
		if err != nil {
			log.Printf("Skipping podcast %s: %v", p.ID, err)
			continue
		}
		allEpisodes = append(allEpisodes, eps...)
	}

	if len(allEpisodes) == 0 {
		log.Println("No episodes required ingestion")
	} else {
		log.Printf("Flushing global batch: writing %d episodes...", len(allEpisodes))
		if err := w.flushEpisodes(ctx, allEpisodes); err != nil {
			log.Printf("Critical database flush failed: %v", err)
			_ = w.store.CompletePipelineBatch(ctx, batchID, "failed")
			return
		}
	}

	if err := w.store.CompletePipelineBatch(ctx, batchID, "success"); err != nil {
		log.Printf("Failed to close batch: %v", err)
		return
	}
	log.Printf("Successfully completed batch %s", batchID)
}

// setupWorker wires up all dependencies and uploads the fallback cover image.
// Any failure here is fatal since the worker can't run without them.
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

	// stage the shared fallback cover once so episodes can reference it by key
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

// processPodcast fetches a single feed, archives the raw xml to bronze, parses
// it, and returns the episodes that are new or changed since the last run.
func (w *worker) processPodcast(ctx context.Context, p db.Podcast, loadMode, batchID string) ([]db.Episode, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", p.FeedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// mimic a podcast app so feeds that block generic clients still respond
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

	// honor a per-podcast episode cap when one is configured
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

// processEpisode decides whether a feed item needs ingesting and, if so,
// uploads its audio and cover to bronze and returns the assembled Episode.
// A nil episode (with nil error) means the item was unchanged and skipped.
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

	// a full run reprocesses everything; a delta run only touches new or
	// changed items (different enclosure url or a newer source timestamp)
	existingEp, exists := existingEps[item.GUID]
	isChanged := loadMode == "full" || !exists
	if !isChanged {
		if existingEp.EnclosureURL != enclosureURL || existingEp.SourceSystemUpdatedAt.Before(episodeUpdated) {
			isChanged = true
		}
	}
	if !isChanged {
		return nil, nil
	}

	// this episode is being reprocessed, so retire its previous batch
	if exists && existingEp.BatchID != "" {
		_ = w.store.StopPreviousBatchIfNeeded(ctx, existingEp.BatchID)
	}

	storageFolderID := storageID(item.GUID)

	audioKey, err := w.uploadMedia(ctx, p.ID, storageFolderID, "audio", "original", enclosureURL, "audio/mpeg, audio/*;q=0.9, */*;q=0.8")
	if err != nil {
		return nil, fmt.Errorf("audio upload failed: %w", err)
	}

	// cover is best-effort: fall back to the system image on any failure
	coverKey := w.fallbackImageURL
	if imageURL := extractImageURL(item); imageURL != "" {
		key, err := w.uploadMedia(
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

// uploadMedia streams a remote asset (audio or image) straight into the bronze
// bucket and returns its storage key.
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

// flushEpisodes persists a whole batch of episodes in a single bulk upsert.
func (w *worker) flushEpisodes(ctx context.Context, eps []db.Episode) error {
	_, err := w.store.BulkUpsertEpisodes(ctx, eps)
	return err
}

// extractEpisodeTimestamp finds the most reliable "last changed" time for an
// item, preferring pre-parsed times and falling back to parsing common raw
// date formats. Returns the unix epoch when nothing usable is present.
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

	// gofeed couldn't parse the date, so try a set of known layouts by hand
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

// extractImageURL pulls a cover image url from an item, checking the standard
// image field first and the itunes extension second. Returns "" if neither.
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

// parseDuration converts an itunes duration ("HH:MM:SS", "MM:SS", or plain
// seconds) into a total second count. Returns nil for empty or zero values.
func parseDuration(val string) *int {
	if val == "" {
		return nil
	}

	parts := strings.Split(val, ":")
	var total int
	switch len(parts) {
	case 3:
		h, _ := strconv.Atoi(parts[0])
		m, _ := strconv.Atoi(parts[1])
		s, _ := strconv.Atoi(parts[2])
		total = h*3600 + m*60 + s
	case 2:
		m, _ := strconv.Atoi(parts[0])
		s, _ := strconv.Atoi(parts[1])
		total = m*60 + s
	default:
		total, _ = strconv.Atoi(val)
	}

	if total == 0 {
		return nil
	}
	return &total
}

// storageID hashes an arbitrary guid into a hex string safe for use as an s3/minio object key
func storageID(guid string) string {
	hash := sha256.Sum256([]byte(guid))
	return hex.EncodeToString(hash[:])
}
