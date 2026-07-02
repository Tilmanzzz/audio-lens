package db

import (
	"context"
	"fmt"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
)

func (s *Store) CreatePipelineBatch(ctx context.Context, stage string, mode string) (string, error) {
	var id string
	query := `
		INSERT INTO pipeline_batches (stage, load_mode, status, start_ts, fin_ts)
		VALUES ($1, $2, 'pending', NOW(), NOW())
		RETURNING id`
	err := s.Pool.QueryRow(ctx, query, stage, mode).Scan(&id)
	return id, err
}

func (s *Store) CompletePipelineBatch(ctx context.Context, batchID string, status string) error {
	query := `
		UPDATE pipeline_batches
		SET status = $1, fin_ts = NOW()
		WHERE id = $2`
	_, err := s.Pool.Exec(ctx, query, status, batchID)
	if err != nil {
		return fmt.Errorf("failed to update pipeline batch status: %w", err)
	}

	if status == "success" {
		payload := fmt.Sprintf(`{"batch_id": "%s"}`, batchID)
		_, err = s.Pool.Exec(ctx, "SELECT pg_notify('transcription_ready', $1)", payload)
		if err != nil {
			return fmt.Errorf("failed to emit pg_notify event: %w", err)
		}
		fmt.Printf("Broadcasted 'transcription_ready' event for batch: %s\n", batchID)
	}

	return nil
}

func (s *Store) StopPreviousBatchIfNeeded(ctx context.Context, batchID string) error {
	query := `
		UPDATE pipeline_batches
		SET status = 'stopped'
		WHERE id = $1
		  AND NOT (
		      (stage = 'processing' AND status = 'success')
		      OR status IN ('failed', 'stopped', 'consumed')
		  )`
	_, err := s.Pool.Exec(ctx, query, batchID)
	return err
}

func (s *Store) InsertPodcast(
	ctx context.Context,
	guid string,
	hosts *string,
	feedURL string,
	title string,
	description *string,
	episodeCount *int,
	categories []string,
	imageURL *string,
	publishedAt *time.Time,
	maxEpisodes *int,
	sourceSystemUpdatedAt *time.Time,
) error {
	_, err := s.Pool.Exec(
		ctx,
		`
		INSERT INTO podcasts (
			guid,
			hosts,
			feed_url,
			title,
			description,
			episode_count,
			categories,
			image_url,
			published_at,
			max_episodes,
			source_system_updated_at
		)
		VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10, $11
		)
		ON CONFLICT (feed_url) DO NOTHING
		`,
		guid, hosts, feedURL, title, description,
		episodeCount, categories, imageURL, publishedAt, maxEpisodes, sourceSystemUpdatedAt,
	)
	return err
}

func (s *Store) GetPodcastsForIngestion(ctx context.Context, mode string) ([]Podcast, error) {
	var pp []Podcast
	var query string

	if mode == "full" {
		query = `SELECT id, guid, feed_url, title, source_system_updated_at, max_episodes FROM podcasts`
	} else {
		// Tier 1 Delta: Grab podcasts with new remote updates OR where max_episodes allows for more content
		query = `
			SELECT p.id, p.guid, p.feed_url, p.title, p.source_system_updated_at, p.max_episodes 
			FROM podcasts p
			LEFT JOIN (
				SELECT podcast_id, COUNT(*) as current_count 
				FROM episodes 
				GROUP BY podcast_id
			) e ON p.id = e.podcast_id
			WHERE p.ingested_at IS NULL 
			   OR p.source_system_updated_at > p.ingested_at
			   OR (p.max_episodes IS NOT NULL AND COALESCE(e.current_count, 0) < p.max_episodes)`
	}

	err := pgxscan.Select(ctx, s.Pool, &pp, query)
	return pp, err
}

// UpdateMaxEpisodesIfHigher updates the max_episodes count only if the provided integer is greater than the current DB value.
// It returns true if the row was updated.
func (s *Store) UpdateMaxEpisodesIfHigher(ctx context.Context, feedURL string, newMax int) (bool, error) {
	query := `
		UPDATE podcasts
		SET max_episodes = $1
		WHERE feed_url = $2 
		  AND (max_episodes IS NULL OR max_episodes < $1)`

	tag, err := s.Pool.Exec(ctx, query, newMax, feedURL)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// SyncPodcastMetadata is used by the Metadata module (Insertion) to overwrite show-level data.
func (s *Store) SyncPodcastMetadata(
	ctx context.Context,
	id, guid, title string,
	description, hosts *string,
	sourceSystemUpdatedAt *time.Time,
) (bool, error) {
	query := `
		UPDATE podcasts
		SET guid = $2,
		    title = $3,
		    description = $4,
		    hosts = $5, 
		    source_system_updated_at = CASE 
		        WHEN source_system_updated_at IS NULL THEN COALESCE($6, NOW()) 
		        WHEN $6 > source_system_updated_at THEN $6 
		        ELSE source_system_updated_at 
		    END
		WHERE id = $1::uuid
		  AND (
		      guid IS DISTINCT FROM $2 OR
		      title IS DISTINCT FROM $3 OR
		      description IS DISTINCT FROM $4 OR
		      hosts IS DISTINCT FROM $5 OR
		      source_system_updated_at IS NULL OR 
		      $6 > source_system_updated_at
		  )`

	tag, err := s.Pool.Exec(ctx, query, id, guid, title, description, hosts, sourceSystemUpdatedAt)
	if err != nil {
		return false, err
	}

	return tag.RowsAffected() > 0, nil
}

func (s *Store) MarkPodcastIngested(ctx context.Context, id, batchID, xmlKey string) error {
	query := `
		UPDATE podcasts
		SET batch_id = $2::uuid,
		    xml_key = $3,
		    ingested_at = NOW()
		WHERE id = $1::uuid`

	_, err := s.Pool.Exec(ctx, query, id, batchID, xmlKey)
	return err
}
