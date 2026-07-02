package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mmcdole/gofeed"

	"github.com/tilmanzzz/media-lens/internal/go/db"
)

const (
	discoveryInterval = 3 * time.Minute
	pollingInterval   = 1 * time.Minute
	maxDiscoveryTicks = 20
	notifyChannel     = "podcast_insert_request"
)

type InsertPayload struct {
	URL         string `json:"url"`
	MaxEpisodes int    `json:"max_episodes"`
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func intPtr(i int) *int {
	return &i
}

func stripHTMLTags(input string) string {
	re := regexp.MustCompile(`<[^>]*>`)
	return re.ReplaceAllString(input, "")
}

func extractHosts(feed *gofeed.Feed) string {
	var hosts []string
	if len(feed.Authors) > 0 {
		for _, a := range feed.Authors {
			if a.Name != "" {
				hosts = append(hosts, a.Name)
			}
		}
	} else if itunes := feed.Extensions["itunes"]; itunes != nil && len(itunes["author"]) > 0 {
		for _, a := range itunes["author"] {
			if a.Value != "" {
				hosts = append(hosts, a.Value)
			}
		}
	}
	return strings.Join(hosts, ", ")
}

func main() {
	var seedPodcasts []InsertPayload

	if len(os.Args) < 2 {
		seedPodcasts = []InsertPayload{
			{URL: "https://podcasts.files.bbci.co.uk/p02nq0gn.rss", MaxEpisodes: 3},                // BBC Global News - ~30min
			{URL: "https://video-api.wsj.com/podcast/rss/wsj/minute-briefing", MaxEpisodes: 20},    // WSJ Minute Briefing - ~3min
			{URL: "https://feeds.npr.org/510289/podcast.xml", MaxEpisodes: 3},                      // Planet Money - ~30min
			{URL: "https://feed.podbean.com/northwell/feed.xml", MaxEpisodes: 4},                   // 20-minute health talk - ~20min
			{URL: "https://feeds.acast.com/public/shows/6152264dc28ad2001383af42", MaxEpisodes: 7}, // Make Your Damn Bed - ~10min
			{URL: "https://feeds.npr.org/510318/podcast.xml", MaxEpisodes: 6},                      // Up First - ~15min
			{URL: "http://feeds.thememorypalace.us/thememorypalace", MaxEpisodes: 7},               // The Memory Palace - ~11min
			{URL: "https://podcasts.files.bbci.co.uk/p004t1hd.rss", MaxEpisodes: 6},                // Witness History - ~15min
			{URL: "https://feeds.megaphone.fm/BVDWV5370667266", MaxEpisodes: 2},                    // The Ben Shapiro Show - ~1h
			{URL: "https://feeds.libsyn.com/576235/rss", MaxEpisodes: 1},                           // The Rubin Report - ~1h
			{URL: "https://rss.dw.com/xml/podcast_Berlin_Briefing", MaxEpisodes: 1},                // DW Berlin Briefing - ~1h
			{URL: "https://feeds.simplecast.com/BqbsxVfO", MaxEpisodes: 3},                         // 99% Invisible - ~30min
			{URL: "https://feeds.megaphone.fm/ADV3162807280", MaxEpisodes: 5},                      // Everything Everywhere Daily - ~15min
			{URL: "https://audioboom.com/channels/5162833.rss", MaxEpisodes: 5},                    // History Daily - ~15min
			{URL: "https://www.spreaker.com/show/6413557/episodes/feed", MaxEpisodes: 7},           // 10 Minute Mystery - ~10min
			{URL: "https://feeds.megaphone.fm/GLSS3382990828", MaxEpisodes: 7},                     // Morning Cup Of Murder - ~10min
			{URL: "https://feeds.megaphone.fm/RSV1597324942", MaxEpisodes: 1},                      // Tucker Carlson Show - ~2h
		}
	} else {
		// Fallback for CLI arguments (defaults to 1 episode)
		for _, arg := range os.Args[1:] {
			seedPodcasts = append(seedPodcasts, InsertPayload{URL: arg, MaxEpisodes: 1})
		}
	}

	fallbackImageURL := "/fallback_cover.svg"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := db.NewStore(ctx, os.Getenv("POSTGRES_URL"))
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer store.Close()

	parser := gofeed.NewParser()
	parser.UserAgent = "MediaLens/1.0 (Podcast Ingestion Pipeline)"
	go startDiscoveryLoop(ctx, store, parser, seedPodcasts, discoveryInterval, maxDiscoveryTicks, fallbackImageURL)
	go startPollingLoop(ctx, store, parser, pollingInterval)
	go startNotificationListener(ctx, store, parser, fallbackImageURL)

	log.Printf("Metadata module successfully started. Polling: %v | Discovery: %v | Channel: %s", pollingInterval, discoveryInterval, notifyChannel)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("Termination signal received. Shutting down metadata module operations...")
}

func startNotificationListener(ctx context.Context, store *db.Store, parser *gofeed.Parser, fallbackImageURL string) {
	dbURL := os.Getenv("POSTGRES_URL")

	for {
		select {
		case <-ctx.Done():
			log.Println("[Listener] Notification listener stopped cleanly.")
			return
		default:
		}

		conn, err := pgx.Connect(ctx, dbURL)
		if err != nil {
			log.Printf("[Listener] Connection failed for LISTEN: %v. Retrying in 5s...", err)
			time.Sleep(5 * time.Second)
			continue
		}

		_, err = conn.Exec(ctx, fmt.Sprintf("LISTEN %s", notifyChannel))
		if err != nil {
			log.Printf("[Listener] Query execution failed for LISTEN: %v", err)
			conn.Close(ctx)
			time.Sleep(5 * time.Second)
			continue
		}
		log.Printf("[Listener] Actively listening for PG NOTIFY events on channel '%s'...", notifyChannel)

		for {
			notification, err := conn.WaitForNotification(ctx)
			if err != nil {
				if ctx.Err() != nil {
					conn.Close(ctx)
					return
				}
				log.Printf("[Listener] Connection dropped or read fault encountered: %v. Reconnecting...", err)
				conn.Close(ctx)
				break
			}

			var payload InsertPayload
			if err := json.Unmarshal([]byte(notification.Payload), &payload); err != nil {
				payload.URL = strings.TrimSpace(notification.Payload)
				payload.MaxEpisodes = 1
			}

			if payload.URL == "" {
				continue
			}

			if payload.MaxEpisodes <= 0 {
				payload.MaxEpisodes = 1
			}

			log.Printf("[Listener] Trigger captured for feed: %s (Max Episodes: %d)", payload.URL, payload.MaxEpisodes)

			go func(p InsertPayload) {
				if err := insertSingleFeed(ctx, store, parser, p.URL, fallbackImageURL, p.MaxEpisodes); err != nil {
					log.Printf("[Listener] Dynamic ingestion failed for %s: %v", p.URL, err)
				}
			}(payload)
		}
	}
}

func insertSingleFeed(ctx context.Context, store *db.Store, parser *gofeed.Parser, rssURL string, fallbackImageURL string, maxEpisodes int) error {
	tracked, err := store.GetPodcastsForIngestion(ctx, "full")
	if err == nil {
		for _, p := range tracked {
			if p.FeedURL == rssURL {
				updated, updateErr := store.UpdateMaxEpisodesIfHigher(ctx, rssURL, maxEpisodes)
				if updateErr == nil && updated {
					log.Printf("[Ingestor] Existing feed detected. Increased max_episodes to %d. Waking ingestion worker...", maxEpisodes)
					sendIngestionTrigger(ctx, os.Getenv("POSTGRES_URL"), "delta")
				} else {
					log.Printf("[Ingestor] Aborted: Feed %s is already managed inside target database.", rssURL)
				}
				return nil
			}
		}
	}

	feed, err := parser.ParseURLWithContext(rssURL, ctx)
	if err != nil {
		return fmt.Errorf("failed to parse discovered target XML structure: %w", err)
	}

	guid := feed.Link
	if itunes := feed.Extensions["itunes"]; itunes != nil && len(itunes["author"]) > 0 {
		guid = itunes["author"][0].Value + feed.Title
	}
	if guid == "" {
		guid = rssURL
	}

	var imageURL *string
	if feed.Image != nil && feed.Image.URL != "" {
		imageURL = strPtr(feed.Image.URL)
	} else if itunes := feed.Extensions["itunes"]; itunes != nil && len(itunes["image"]) > 0 {
		href := itunes["image"][0].Attrs["href"]
		if href != "" {
			imageURL = strPtr(href)
		} else {
			imageURL = strPtr(fallbackImageURL)
		}
	} else {
		imageURL = strPtr(fallbackImageURL)
	}

	var publishedAt *time.Time
	if feed.PublishedParsed != nil {
		publishedAt = feed.PublishedParsed
	}

	episodeCount := len(feed.Items)

	var remoteUpdateTime *time.Time
	if feed.UpdatedParsed != nil {
		remoteUpdateTime = feed.UpdatedParsed
	} else if feed.PublishedParsed != nil {
		remoteUpdateTime = feed.PublishedParsed
	} else if len(feed.Items) > 0 && feed.Items[0].PublishedParsed != nil {
		remoteUpdateTime = feed.Items[0].PublishedParsed
	}

	if remoteUpdateTime == nil {
		now := time.Now()
		remoteUpdateTime = &now
	}

	truncated := remoteUpdateTime.Truncate(time.Second)
	remoteUpdateTime = &truncated

	hosts := extractHosts(feed)
	cleanDescription := stripHTMLTags(feed.Description)

	err = store.InsertPodcast(
		ctx,
		guid,
		strPtr(hosts),
		rssURL,
		feed.Title,
		strPtr(cleanDescription),
		intPtr(episodeCount),
		feed.Categories,
		imageURL,
		publishedAt,
		&maxEpisodes,
		remoteUpdateTime,
	)
	if err != nil {
		return fmt.Errorf("database write transaction failed: %w", err)
	}

	log.Printf("[Ingestor] Successfully processed and inserted target: %s", feed.Title)
	sendIngestionTrigger(ctx, os.Getenv("POSTGRES_URL"), "delta")
	return nil
}

func startDiscoveryLoop(ctx context.Context, store *db.Store, parser *gofeed.Parser, seedPodcasts []InsertPayload, interval time.Duration, maxDiscoverPerTick int, fallbackImageURL string) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	runDiscoveryPass(ctx, store, parser, seedPodcasts, maxDiscoverPerTick, fallbackImageURL)

	for {
		select {
		case <-ctx.Done():
			log.Println("Discovery loop stopped cleanly.")
			return
		case <-ticker.C:
			runDiscoveryPass(ctx, store, parser, seedPodcasts, maxDiscoverPerTick, fallbackImageURL)
		}
	}
}

func runDiscoveryPass(ctx context.Context, store *db.Store, parser *gofeed.Parser, seedPodcasts []InsertPayload, maxDiscover int, fallbackImageURL string) {
	log.Println("[Discovery] Starting discovery cycle...")

	tracked, err := store.GetPodcastsForIngestion(ctx, "full")
	if err != nil {
		log.Printf("[Discovery] Error fetching tracked podcasts for deduplication: %v", err)
		return
	}

	trackedMap := make(map[string]bool)
	for _, p := range tracked {
		trackedMap[p.FeedURL] = true
	}

	discoveredCount := 0
	for _, seed := range seedPodcasts {
		if discoveredCount >= maxDiscover {
			break
		}
		if trackedMap[seed.URL] || seed.URL == "" {
			continue
		}

		log.Printf("[Discovery] Evaluating untracked seed array item: %s (Max Episodes: %d)", seed.URL, seed.MaxEpisodes)

		if err := insertSingleFeed(ctx, store, parser, seed.URL, fallbackImageURL, seed.MaxEpisodes); err != nil {
			log.Printf("[Discovery] Inline tracking execution failure for %s: %v", seed.URL, err)
			continue
		}
		discoveredCount++
	}
	log.Printf("[Discovery] Cycle finished. Registered %d new podcast targets.", discoveredCount)
}

func startPollingLoop(ctx context.Context, store *db.Store, parser *gofeed.Parser, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	runPollingPass(ctx, store, parser)

	for {
		select {
		case <-ctx.Done():
			log.Println("Polling loop stopped cleanly.")
			return
		case <-ticker.C:
			runPollingPass(ctx, store, parser)
		}
	}
}

func runPollingPass(ctx context.Context, store *db.Store, parser *gofeed.Parser) {
	log.Println("[Poller] Starting change-detection polling cycle...")

	podcasts, err := store.GetPodcastsForIngestion(ctx, "full")
	if err != nil {
		log.Printf("[Poller] Error retrieving podcasts for tracking sync: %v", err)
		return
	}

	if len(podcasts) == 0 {
		log.Println("[Poller] No tracked podcasts found in database. Skipping cycle.")
		return
	}

	syncedCount := 0
	for _, p := range podcasts {
		select {
		case <-ctx.Done():
			return
		default:
		}

		feed, err := parser.ParseURLWithContext(p.FeedURL, ctx)
		if err != nil {
			log.Printf("[Poller] Network/Parsing failure for feed %s: %v", p.FeedURL, err)
			continue
		}

		var remoteUpdateTime *time.Time
		if feed.UpdatedParsed != nil {
			remoteUpdateTime = feed.UpdatedParsed
		} else if feed.PublishedParsed != nil {
			remoteUpdateTime = feed.PublishedParsed
		} else if len(feed.Items) > 0 && feed.Items[0].PublishedParsed != nil {
			remoteUpdateTime = feed.Items[0].PublishedParsed
		}

		if remoteUpdateTime != nil {
			truncated := remoteUpdateTime.Truncate(time.Second)
			remoteUpdateTime = &truncated
		}

		guid := feed.Link
		if itunes := feed.Extensions["itunes"]; itunes != nil && len(itunes["author"]) > 0 {
			guid = itunes["author"][0].Value + feed.Title
		}

		hosts := extractHosts(feed)
		cleanDescription := stripHTMLTags(feed.Description)

		updated, err := store.SyncPodcastMetadata(ctx, p.ID, guid, feed.Title, strPtr(cleanDescription), strPtr(hosts), remoteUpdateTime)
		if err != nil {
			log.Printf("[Poller] Failed to sync metadata for ID %s: %v", p.ID, err)
			continue
		}

		if updated {
			log.Printf("[Poller] Synced new metadata and source_system_updated_at for '%s'", p.Title)
			syncedCount++
		}
	}

	log.Println("[Poller] Change-detection polling cycle complete.")

	if syncedCount > 0 {
		// TODO FIX infinite updates and uncomment
		// sendIngestionTrigger(ctx, os.Getenv("POSTGRES_URL"), "delta")
	}
}

func sendIngestionTrigger(ctx context.Context, dbURL, mode string) {
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		log.Printf("[Trigger] Failed to connect to DB for notification: %v", err)
		return
	}
	defer conn.Close(ctx)

	payload := fmt.Sprintf(`{"load_mode": "%s"}`, mode)
	_, err = conn.Exec(ctx, "SELECT pg_notify('ingestion_ready', $1)", payload)
	if err != nil {
		log.Printf("[Trigger] Failed to send ingestion notification: %v", err)
	} else {
		log.Printf("[Trigger] Successfully sent pg_notify to ingestion worker (mode: %s)", mode)
	}
}
