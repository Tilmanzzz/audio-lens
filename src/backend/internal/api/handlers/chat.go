package handlers

import (
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"media-lens/backend/internal/model"
)

// @Summary      Frage zum Podcast stellen
// @Description  Ask a question about a podcast episode. The answer is generated based solely on the episode transcript.
// @Tags         chat
// @Accept       json
// @Produce      json
// @Param        id    path      string             true  "Episode ID (UUID)"
// @Param        body  body      model.ChatRequest  true  "User question"
// @Success      200   {object}  model.ChatResponse
// @Failure      400   {object}  model.ApiError
// @Failure      404   {object}  model.ApiError
// @Failure      503   {object}  model.ApiError
// @Router       /episodes/{id}/chat [post]
func (h *Handler) Chat(c *gin.Context) {
	episode := h.getEpisodeOrAbort(c, c.Param("id"))
	if episode == nil {
		return
	}

	var req model.ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "Ungültige Anfrage: "+err.Error())
		return
	}

	lines, err := h.Transcripts.ListByEpisodeID(c.Request.Context(), episode.ID)
	if err != nil {
		respondInternalError(c, err)
		return
	}

	if len(lines) == 0 {
		respondError(c, http.StatusNotFound, "transcript_not_found", "Kein Transkript für diese Episode vorhanden.")
		return
	}

	var sb strings.Builder
	for _, line := range lines {
		sb.WriteString(fmt.Sprintf("[%s] %s\n", formatTimestamp(line.StartTime), line.Text))
	}

	answer, err := h.LLM.Chat(c.Request.Context(), sb.String(), req.History, req.Question)
	if err != nil {
		log.Printf("ERROR chat LLM episode=%s: %v", episode.ID, err)
		respondError(c, http.StatusServiceUnavailable, "LLM_UNAVAILABLE", "LLM-Service ist nicht verfügbar.")
		return
	}

	c.JSON(http.StatusOK, model.ChatResponse{Answer: answer})
}

func formatTimestamp(seconds float64) string {
	total := int(math.Round(seconds))
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
