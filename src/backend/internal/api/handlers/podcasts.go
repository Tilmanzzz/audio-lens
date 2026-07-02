package handlers

import (
	"database/sql"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"media-lens/backend/internal/config"
	"media-lens/backend/internal/embedder"
	"media-lens/backend/internal/llm"
	"media-lens/backend/internal/model"
	"media-lens/backend/internal/repository"
	"media-lens/backend/internal/storage"
	"media-lens/backend/internal/vectorstore"
)

type Handler struct {
	Episodes    repository.EpisodeRepository
	Chapters    repository.ChapterRepository
	Transcripts repository.TranscriptRepository
	FactChecks  repository.FactCheckRepository
	LLM         *llm.GeminiClient
	Minio       *minio.Client
	Config      *config.Config
	DB          *sql.DB
	Embedder    embedder.Embedder
	VectorStore *vectorstore.PgVectorClient
}

func respondError(c *gin.Context, status int, errCode, message string) {
	c.JSON(status, model.ApiError{
		Error:   errCode,
		Message: message,
		Status:  status,
	})
}

func respondInternalError(c *gin.Context, err error) {
	log.Printf("ERROR %s %s: %v", c.Request.Method, c.Request.URL.Path, err)
	respondError(c, http.StatusInternalServerError, "internal_error", "Ein interner Fehler ist aufgetreten.")
}

func ValidateUUID(paramName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param(paramName)
		if _, err := uuid.Parse(id); err != nil {
			respondError(c, http.StatusBadRequest, "invalid_id", "Ungültiges UUID-Format.")
			c.Abort()
			return
		}
		c.Next()
	}
}

func MaxBodySize(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}

func (h *Handler) getEpisodeOrAbort(c *gin.Context, episodeID string) *model.Episode {
	episode, err := h.Episodes.GetByID(c.Request.Context(), episodeID)
	if err != nil {
		respondInternalError(c, err)
		return nil
	}
	if episode == nil {
		respondError(c, http.StatusNotFound, "episode_not_found", "Episode mit dieser ID existiert nicht.")
		return nil
	}
	return episode
}

func (h *Handler) presignCoverURLs(c *gin.Context, episodes []model.Episode) map[string]*url.URL {
	urls := make(map[string]*url.URL, len(episodes))
	for _, ep := range episodes {
		if ep.CoverKey == "" {
			continue
		}
		if _, exists := urls[ep.CoverKey]; exists {
			continue
		}
		u, err := storage.GeneratePresignedURL(c.Request.Context(), h.Minio, h.Config.MinioBucket, ep.CoverKey, 1*time.Hour)
		if err != nil {
			log.Printf("WARN presign cover %q: %v", ep.CoverKey, err)
			continue
		}
		urls[ep.CoverKey] = u
	}
	return urls
}

func episodeToCard(ep model.Episode, coverURLs map[string]*url.URL) model.EpisodeCard {
	card := model.EpisodeCard{
		ID:                 ep.ID,
		Title:              ep.Title,
		PodcastName:        ep.PodcastName,
		PublishedAt:        formatDate(ep.PublishedAt),
		Summary:            ep.Summary,
		ProcessingComplete: ep.ProcessingComplete,
	}
	if ep.DurationSeconds != nil {
		card.DurationSeconds = *ep.DurationSeconds
	}
	if u, ok := coverURLs[ep.CoverKey]; ok {
		card.CoverURL = u.String()
	} else if ep.PodcastImageURL != "" {
		card.CoverURL = ep.PodcastImageURL
	}
	return card
}

func (h *Handler) episodeToDetail(c *gin.Context, ep model.Episode) model.EpisodeDetail {
	detail := model.EpisodeDetail{
		ID:                 ep.ID,
		Title:              ep.Title,
		PodcastName:        ep.PodcastName,
		PublishedAt:        formatDate(ep.PublishedAt),
		Summary:            ep.Summary,
		ProcessingComplete: ep.ProcessingComplete,
	}
	if ep.DurationSeconds != nil {
		detail.DurationSeconds = *ep.DurationSeconds
	}
	if ep.CoverKey != "" {
		u, err := storage.GeneratePresignedURL(c.Request.Context(), h.Minio, h.Config.MinioBucket, ep.CoverKey, 1*time.Hour)
		if err == nil {
			detail.CoverURL = u.String()
		} else {
			log.Printf("WARN presign cover %q: %v", ep.CoverKey, err)
		}
	}
	if detail.CoverURL == "" && ep.PodcastImageURL != "" {
		detail.CoverURL = ep.PodcastImageURL
	}
	if ep.AudioKey != "" {
		detail.AudioURL = "/api/v1/episodes/" + ep.ID + "/audio"
	}
	return detail
}

func formatDate(t sql.NullTime) string {
	if !t.Valid {
		return ""
	}
	return t.Time.Format("2006-01-02")
}

// @Summary      Episode-Liste
// @Description  Cursor-based paginated episode list with optional free-text search.
// @Tags         episodes
// @Produce      json
// @Param        q       query     string  false  "Free-text search on title and podcast name"
// @Param        cursor  query     string  false  "Cursor from previous response"
// @Param        limit   query     int     false  "Page size (default 100, max 100)"
// @Success      200  {object}  model.EpisodeListResponse
// @Failure      400  {object}  model.ApiError
// @Router       /episodes [get]
func (h *Handler) ListEpisodes(c *gin.Context) {
	q := c.Query("q")
	cursor := c.Query("cursor")
	limit := repository.ParseLimit(c.Query("limit"), 100, 100)

	episodes, total, err := h.Episodes.ListPaginated(c.Request.Context(), q, cursor, limit)
	if err != nil {
		respondInternalError(c, err)
		return
	}

	coverURLs := h.presignCoverURLs(c, episodes)

	items := make([]model.EpisodeCard, 0, len(episodes))
	for _, ep := range episodes {
		items = append(items, episodeToCard(ep, coverURLs))
	}

	var nextCursor *string
	if len(episodes) == limit {
		last := episodes[len(episodes)-1]
		cur := repository.EncodeCursor(last)
		nextCursor = &cur
	}

	c.JSON(http.StatusOK, model.EpisodeListResponse{
		Items:      items,
		NextCursor: nextCursor,
		Total:      total,
	})
}

// @Summary      Episode-Detail
// @Description  Get episode detail for the header area above the tabs.
// @Tags         episodes
// @Produce      json
// @Param        id   path      string  true  "Episode ID (UUID)"
// @Success      200  {object}  model.EpisodeDetailResponse
// @Failure      404  {object}  model.ApiError
// @Router       /episodes/{id} [get]
func (h *Handler) GetEpisode(c *gin.Context) {
	episode := h.getEpisodeOrAbort(c, c.Param("id"))
	if episode == nil {
		return
	}

	c.JSON(http.StatusOK, model.EpisodeDetailResponse{
		Episode: h.episodeToDetail(c, *episode),
	})
}
