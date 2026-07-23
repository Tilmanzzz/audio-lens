package db

import (
	"context"
	"fmt"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
)

// GetEpisodeMap pre-fetches all episodes for a podcast into a guid-keyed lookup map.
func (s *Store) GetEpisodeMap(ctx context.Context, podcastID string) (map[string]Episode, error) {
	var episodes []Episode
	query := `SELECT id, podcast_id, guid, title, audio_key, cover_key, published_at, enclosure_url, COALESCE(batch_id::text, '') as batch_id, source_system_updated_at
	          FROM episodes WHERE podcast_id = $1`

	if err := pgxscan.Select(ctx, s.Pool, &episodes, query, podcastID); err != nil {
		return nil, err
	}

	epMap := make(map[string]Episode, len(episodes))
	for _, ep := range episodes {
		epMap[ep.GUID] = ep
	}
	return epMap, nil
}

// GetEpisodeByID fetches a single episode by its primary key.
func (s *Store) GetEpisodeByID(ctx context.Context, id string) (Episode, error) {
	var ep Episode
	query := `SELECT id, podcast_id, guid, title, audio_key, cover_key, status, published_at, enclosure_url
	          FROM episodes WHERE id = $1`

	if err := pgxscan.Get(ctx, s.Pool, &ep, query, id); err != nil {
		return Episode{}, err
	}
	return ep, nil
}

// MarkPendingSectioning transitions an episode to the pending_sectioning status.
func (s *Store) MarkPendingSectioning(ctx context.Context, id string) error {
	return s.setEpisodeStatus(ctx, id, "pending_sectioning")
}

// MarkPendingTranscription transitions an episode to the pending_transcription status.
func (s *Store) MarkPendingTranscription(ctx context.Context, id string) error {
	return s.setEpisodeStatus(ctx, id, "pending_transcription")
}

// setEpisodeStatus updates an episode's status and errors if no row matched.
func (s *Store) setEpisodeStatus(ctx context.Context, id, status string) error {
	result, err := s.Pool.Exec(ctx, `UPDATE episodes SET status = $1 WHERE id = $2`, status, id)
	if err != nil {
		return fmt.Errorf("failed to update episode status: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("no episode found with id: %s", id)
	}
	return nil
}

// SetTranscriptKey records the transcript object key for an episode and errors
// if no row matched.
func (s *Store) SetTranscriptKey(ctx context.Context, id, transcriptKey string) error {
	query := `UPDATE episodes SET transcript_key = $1 WHERE id = $2`
	result, err := s.Pool.Exec(ctx, query, transcriptKey, id)
	if err != nil {
		return fmt.Errorf("failed to update transcript key: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("no episode found with id: %s", id)
	}
	return nil
}

// BulkUpsertEpisodes inserts or updates episodes in a single batch and returns
// the affected ids. Conflicts are resolved on the guid column.
func (s *Store) BulkUpsertEpisodes(ctx context.Context, eps []Episode) ([]string, error) {
	if len(eps) == 0 {
		return nil, nil
	}

	podcastIDs := make([]string, len(eps))
	guids := make([]string, len(eps))
	titles := make([]string, len(eps))
	audioKeys := make([]string, len(eps))
	coverKeys := make([]string, len(eps))
	publishedAts := make([]*time.Time, len(eps))
	durations := make([]*int, len(eps))
	enclosureURLs := make([]string, len(eps))
	batchIDs := make([]string, len(eps))
	sourceSystemUpdatedAts := make([]*time.Time, len(eps))

	for i, ep := range eps {
		podcastIDs[i] = ep.PodcastID
		guids[i] = ep.GUID
		titles[i] = ep.Title
		audioKeys[i] = ep.AudioKey
		coverKeys[i] = ep.CoverKey
		publishedAts[i] = ep.PublishedAt
		durations[i] = ep.DurationSeconds
		enclosureURLs[i] = ep.EnclosureURL
		batchIDs[i] = ep.BatchID
		sourceSystemUpdatedAts[i] = ep.SourceSystemUpdatedAt
	}

	// unnest the parallel arrays into rows, then upsert on the guid conflict
	query := `
		INSERT INTO episodes (
			podcast_id, guid, title, audio_key, cover_key,
			published_at, duration_seconds, enclosure_url, batch_id, source_system_updated_at
		)
		SELECT * FROM unnest(
			$1::text[]::uuid[],
			$2::text[],
			$3::text[],
			$4::text[],
			$5::text[],
			$6::timestamptz[],
			$7::int[],
			$8::text[],
			$9::text[]::uuid[],
			$10::timestamptz[]
		)
		ON CONFLICT (guid) DO UPDATE SET
			title = EXCLUDED.title,
			audio_key = EXCLUDED.audio_key,
			cover_key = EXCLUDED.cover_key,
			published_at = EXCLUDED.published_at,
			duration_seconds = EXCLUDED.duration_seconds,
			enclosure_url = EXCLUDED.enclosure_url,
			batch_id = EXCLUDED.batch_id,
			source_system_updated_at = EXCLUDED.source_system_updated_at
		RETURNING id;
	`

	var ids []string
	err := pgxscan.Select(ctx, s.Pool, &ids, query,
		podcastIDs, guids, titles, audioKeys, coverKeys,
		publishedAts, durations, enclosureURLs, batchIDs, sourceSystemUpdatedAts,
	)
	if err != nil {
		return nil, fmt.Errorf("bulk upsert failed: %w", err)
	}
	return ids, nil
}
