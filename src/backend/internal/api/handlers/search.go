package handlers

import (
	"media-lens/backend/internal/model"
	"media-lens/backend/internal/storage"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// @Summary      Semantic search
// @Description  Search episodes and transcript chunks using natural-language queries via vector similarity.
// @Tags         search
// @Produce      json
// @Param        q           query  string  true   "Search query"
// @Param        limit       query  int     false  "Max episode results"      default(10)
// @Param        highlights  query  int     false  "Max highlights per episode" default(3)
// @Param        min_score   query  number  false  "Minimum similarity score"  default(0.73)
// @Success      200  {object}  model.SearchResponse
// @Failure      400  {object}  model.ApiError
// @Failure      503  {object}  model.ApiError
// @Router       /search [get]
func (h *Handler) SemanticSearch(c *gin.Context) {
	q := c.Query("q")
	if q == "" {
		respondError(c, http.StatusBadRequest, "MISSING_QUERY", "Query-Parameter 'q' ist erforderlich.")
		return
	}

	limit := parseIntParam(c.Query("limit"), 10, 50)
	highlights := parseIntParam(c.Query("highlights"), 3, 10)
	minScore := parseFloatParam(c.Query("min_score"), 0.73)

	vector, err := h.Embedder.Embed(c.Request.Context(), q)
	if err != nil {
		respondError(c, http.StatusServiceUnavailable, "EMBEDDING_UNAVAILABLE", "Embedding-Service ist nicht verfügbar.")
		return
	}

	episodeHits, err := h.VectorStore.SearchEpisodes(c.Request.Context(), vector, limit, minScore)
	if err != nil {
		respondError(c, http.StatusServiceUnavailable, "SEARCH_UNAVAILABLE", "Suchservice ist nicht verfügbar.")
		return
	}

	if len(episodeHits) == 0 {
		c.JSON(http.StatusOK, model.SearchResponse{Query: q, Items: []model.SearchResultItem{}, Total: 0})
		return
	}

	episodeIDs := make([]string, len(episodeHits))
	for i, hit := range episodeHits {
		episodeIDs[i] = hit.EpisodeID
	}

	totalChunkLimit := limit * highlights
	chunkHits, err := h.VectorStore.SearchChunks(c.Request.Context(), vector, episodeIDs, totalChunkLimit, minScore)
	if err != nil {
		respondError(c, http.StatusServiceUnavailable, "SEARCH_UNAVAILABLE", "Suchservice ist nicht verfügbar.")
		return
	}

	chunksByEpisode := make(map[string][]model.SearchHighlight)
	for _, ch := range chunkHits {
		if len(chunksByEpisode[ch.EpisodeID]) < highlights {
			chunksByEpisode[ch.EpisodeID] = append(chunksByEpisode[ch.EpisodeID], model.SearchHighlight{
				Text:      ch.Text,
				StartTime: ch.StartTime,
				Score:     ch.Score,
			})
		}
	}

	items := make([]model.SearchResultItem, 0, len(episodeHits))
	for _, ep := range episodeHits {
		coverURL := ""
		if ep.CoverKey != "" {
			if u, err := storage.GeneratePresignedURL(c.Request.Context(), h.Minio, h.Config.MinioBucket, ep.CoverKey, time.Hour); err == nil {
				coverURL = u.String()
			}
		}
		if coverURL == "" && ep.PodcastImageURL != "" {
			coverURL = ep.PodcastImageURL
		}

		hl := chunksByEpisode[ep.EpisodeID]
		if hl == nil {
			hl = []model.SearchHighlight{}
		}

		items = append(items, model.SearchResultItem{
			EpisodeID:   ep.EpisodeID,
			Title:       ep.Title,
			PodcastName: ep.PodcastName,
			CoverURL:    coverURL,
			Score:       ep.Score,
			Highlights:  hl,
		})
	}

	c.JSON(http.StatusOK, model.SearchResponse{
		Query: q,
		Items: items,
		Total: len(items),
	})
}

func parseIntParam(s string, defaultVal, maxVal int) int {
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

func parseFloatParam(s string, defaultVal float64) float64 {
	if s == "" {
		return defaultVal
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || v > 1 {
		return defaultVal
	}
	return v
}
