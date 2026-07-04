package repository

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/lib/pq"
	"media-lens/backend/internal/model"
)

// --- Episode Repository ---

type EpisodeRepository interface {
	GetByID(ctx context.Context, id string) (*model.Episode, error)
	ListPaginated(ctx context.Context, q string, cursor string, limit int) (episodes []model.Episode, total int, err error)
}

type postgresEpisodeRepo struct {
	db *sql.DB
}

func NewEpisodeRepository(db *sql.DB) EpisodeRepository {
	return &postgresEpisodeRepo{db: db}
}

func (r *postgresEpisodeRepo) GetByID(ctx context.Context, id string) (*model.Episode, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT e.id, e.title, e.podcast_id, p.title,
		       e.published_at, e.duration_seconds,
		       COALESCE(e.audio_key, ''), COALESCE(e.cover_key, ''),
		       COALESCE(e.summary, ''), e.ingested_at,
		       COALESCE(p.image_url, ''),
		       (pb.stage = 'fact_checker' AND pb.status IN ('success', 'consumed')) AS processing_complete
		FROM episodes e
		JOIN podcasts p ON p.id = e.podcast_id
		JOIN pipeline_batches pb ON pb.id = e.batch_id
		WHERE e.id = $1
		  AND pb.stage IN ('transcription', 'segmenting', 'text_summarizer', 'emotion_scoring', 'embedder', 'fact_checker')
		  AND pb.status IN ('success', 'consumed')
	`, id)

	ep, err := scanEpisode(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan episode: %w", err)
	}
	return ep, nil
}

func (r *postgresEpisodeRepo) ListPaginated(ctx context.Context, q string, cursor string, limit int) ([]model.Episode, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}

	var whereClauses []string
	var args []any
	argIdx := 1

	whereClauses = append(whereClauses, "pb.stage IN ('transcription', 'segmenting', 'text_summarizer', 'emotion_scoring', 'embedder', 'fact_checker')")
	whereClauses = append(whereClauses, "pb.status IN ('success', 'consumed')")

	if q != "" {
		pattern := "%" + q + "%"
		whereClauses = append(whereClauses, fmt.Sprintf("(e.title ILIKE $%d OR p.title ILIKE $%d)", argIdx, argIdx))
		args = append(args, pattern)
		argIdx++
	}

	if cursor != "" {
		decoded, err := base64.StdEncoding.DecodeString(cursor)
		if err == nil {
			parts := strings.SplitN(string(decoded), "|", 2)
			if len(parts) == 2 {
				whereClauses = append(whereClauses, fmt.Sprintf(
					"(e.ingested_at, e.id) < ($%d::timestamptz, $%d::uuid)", argIdx, argIdx+1))
				args = append(args, parts[0], parts[1])
				argIdx += 2
			}
		}
	}

	whereSQL := "WHERE " + strings.Join(whereClauses, " AND ")

	countQuery := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM episodes e
		JOIN podcasts p ON p.id = e.podcast_id
		JOIN pipeline_batches pb ON pb.id = e.batch_id
		%s`, whereSQL)
	var total int
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count episodes: %w", err)
	}

	dataQuery := fmt.Sprintf(`
		SELECT e.id, e.title, e.podcast_id, p.title,
		       e.published_at, e.duration_seconds,
		       COALESCE(e.audio_key, ''), COALESCE(e.cover_key, ''),
		       COALESCE(e.summary, ''), e.ingested_at,
		       COALESCE(p.image_url, ''),
		       (pb.stage = 'fact_checker' AND pb.status IN ('success', 'consumed')) AS processing_complete
		FROM episodes e
		JOIN podcasts p ON p.id = e.podcast_id
		JOIN pipeline_batches pb ON pb.id = e.batch_id
		%s
		ORDER BY e.ingested_at DESC, e.id DESC
		LIMIT $%d
	`, whereSQL, argIdx)
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, dataQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("query paginated episodes: %w", err)
	}
	defer rows.Close()

	episodes, err := scanEpisodes(rows)
	if err != nil {
		return nil, 0, err
	}

	return episodes, total, nil
}

func EncodeCursor(ep model.Episode) string {
	raw := ep.IngestedAt.Format("2006-01-02T15:04:05.999999Z07:00") + "|" + ep.ID
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

func scanEpisode(row *sql.Row) (*model.Episode, error) {
	var ep model.Episode
	var durationSeconds sql.NullInt64
	err := row.Scan(&ep.ID, &ep.Title, &ep.PodcastID, &ep.PodcastName,
		&ep.PublishedAt, &durationSeconds,
		&ep.AudioKey, &ep.CoverKey, &ep.Summary, &ep.IngestedAt,
		&ep.PodcastImageURL, &ep.ProcessingComplete)
	if err != nil {
		return nil, err
	}
	if durationSeconds.Valid {
		d := int(durationSeconds.Int64)
		ep.DurationSeconds = &d
	}
	return &ep, nil
}

func scanEpisodes(rows *sql.Rows) ([]model.Episode, error) {
	var episodes []model.Episode
	for rows.Next() {
		var ep model.Episode
		var durationSeconds sql.NullInt64
		if err := rows.Scan(&ep.ID, &ep.Title, &ep.PodcastID, &ep.PodcastName,
			&ep.PublishedAt, &durationSeconds,
			&ep.AudioKey, &ep.CoverKey, &ep.Summary, &ep.IngestedAt,
			&ep.PodcastImageURL, &ep.ProcessingComplete); err != nil {
			return nil, fmt.Errorf("scan episode row: %w", err)
		}
		if durationSeconds.Valid {
			d := int(durationSeconds.Int64)
			ep.DurationSeconds = &d
		}
		episodes = append(episodes, ep)
	}
	return episodes, rows.Err()
}

// --- Chapter Repository ---

type ChapterRepository interface {
	ListByEpisodeID(ctx context.Context, episodeID string) ([]model.ChapterCard, error)
}

type postgresChapterRepo struct {
	db *sql.DB
}

func NewChapterRepository(db *sql.DB) ChapterRepository {
	return &postgresChapterRepo{db: db}
}

func (r *postgresChapterRepo) ListByEpisodeID(ctx context.Context, episodeID string) ([]model.ChapterCard, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, chapter_idx, COALESCE(title, ''), COALESCE(summary, ''), start_time, end_time
		FROM chapters
		WHERE episode_id = $1
		ORDER BY chapter_idx ASC
	`, episodeID)
	if err != nil {
		return nil, fmt.Errorf("query chapters: %w", err)
	}
	defer rows.Close()

	var chapters []model.ChapterCard
	for rows.Next() {
		var ch model.ChapterCard
		if err := rows.Scan(&ch.ID, &ch.ChapterIdx, &ch.Title, &ch.Summary, &ch.StartTime, &ch.EndTime); err != nil {
			return nil, fmt.Errorf("scan chapter row: %w", err)
		}
		chapters = append(chapters, ch)
	}
	return chapters, rows.Err()
}

// --- Transcript Repository ---

type TranscriptRepository interface {
	ListByEpisodeID(ctx context.Context, episodeID string) ([]model.TranscriptLine, error)
}

type postgresTranscriptRepo struct {
	db *sql.DB
}

func NewTranscriptRepository(db *sql.DB) TranscriptRepository {
	return &postgresTranscriptRepo{db: db}
}

func (r *postgresTranscriptRepo) ListByEpisodeID(ctx context.Context, episodeID string) ([]model.TranscriptLine, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT tl.id, tl.chapter_id, tl.start_time, tl.end_time, tl.text,
		       COALESCE(tl.emotion::text, 'neutral'), COALESCE(tl.emotion_score, 0),
		       EXISTS(SELECT 1 FROM fact_checked_claims fc WHERE fc.chapter_id = tl.chapter_id)
		FROM transcript_lines tl
		JOIN chapters ch ON ch.id = tl.chapter_id
		WHERE ch.episode_id = $1
		ORDER BY ch.chapter_idx, tl.line_idx
	`, episodeID)
	if err != nil {
		return nil, fmt.Errorf("query transcript lines: %w", err)
	}
	defer rows.Close()

	var lines []model.TranscriptLine
	for rows.Next() {
		var l model.TranscriptLine
		if err := rows.Scan(&l.ID, &l.ChapterID, &l.StartTime, &l.EndTime, &l.Text,
			&l.Emotion, &l.EmotionScore, &l.HasFactFlag); err != nil {
			return nil, fmt.Errorf("scan transcript line: %w", err)
		}
		lines = append(lines, l)
	}
	return lines, rows.Err()
}

// --- FactCheck Repository ---

type FactCheckRepository interface {
	ListByEpisodeID(ctx context.Context, episodeID string) ([]model.FactCheckClaim, error)
}

type postgresFactCheckRepo struct {
	db *sql.DB
}

func NewFactCheckRepository(db *sql.DB) FactCheckRepository {
	return &postgresFactCheckRepo{db: db}
}

func (r *postgresFactCheckRepo) ListByEpisodeID(ctx context.Context, episodeID string) ([]model.FactCheckClaim, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT fc.id, fc.chapter_id, COALESCE(fc.claim_idx, 0), COALESCE(fc.claim, ''),
		       fc.verdict::text, COALESCE(fc.explanation, ''), COALESCE(fc.sources, '{}')
		FROM fact_checked_claims fc
		JOIN chapters ch ON ch.id = fc.chapter_id
		WHERE ch.episode_id = $1
		ORDER BY ch.chapter_idx, fc.claim_idx
	`, episodeID)
	if err != nil {
		return nil, fmt.Errorf("query fact checks: %w", err)
	}
	defer rows.Close()

	var claims []model.FactCheckClaim
	for rows.Next() {
		var c model.FactCheckClaim
		if err := rows.Scan(&c.ID, &c.ChapterID, &c.ClaimIdx, &c.Claim,
			&c.Verdict, &c.Explanation, pq.Array(&c.Sources)); err != nil {
			return nil, fmt.Errorf("scan fact check: %w", err)
		}
		claims = append(claims, c)
	}
	return claims, rows.Err()
}

// ParseLimit parses a limit string with bounds enforcement.
func ParseLimit(s string, defaultVal, maxVal int) int {
	if s == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(s)
	if err != nil || v <= 0 {
		return defaultVal
	}
	if v > maxVal {
		return maxVal
	}
	return v
}
