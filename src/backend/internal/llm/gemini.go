package llm

import (
	"context"
	"fmt"

	"google.golang.org/genai"
	"media-lens/backend/internal/model"
)

const systemPrompt = `You are a podcast analysis assistant. You answer questions about a podcast episode based ONLY on its transcript provided below.

Each line of the transcript is prefixed with a timestamp in the format [M:SS] or [H:MM:SS].

Rules:
- Answer exclusively based on the transcript content
- If the transcript does not contain enough information to answer the question, say so clearly
- Be concise and direct
- When referencing specific parts of the transcript, ALWAYS include the timestamp in [M:SS] or [H:MM:SS] format so the user can jump to that part of the episode
- Answer in the same language the question was asked in`

type GeminiClient struct {
	client *genai.Client
	model  string
}

func NewGeminiClient(ctx context.Context, apiKey string) (*GeminiClient, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("create gemini client: %w", err)
	}

	return &GeminiClient{
		client: client,
		model:  "gemini-2.5-flash-lite",
	}, nil
}

func (g *GeminiClient) Ask(ctx context.Context, transcript string, question string) (string, error) {
	return g.Chat(ctx, transcript, nil, question)
}

func (g *GeminiClient) Chat(ctx context.Context, transcript string, history []model.ChatMessage, question string) (string, error) {
	sysMsg := fmt.Sprintf("%s\n\nTRANSCRIPT:\n%s", systemPrompt, transcript)

	var contents []*genai.Content
	for _, msg := range history {
		role := "user"
		if msg.Role == "assistant" {
			role = "model"
		}
		contents = append(contents, &genai.Content{
			Role: role,
			Parts: []*genai.Part{{Text: msg.Content}},
		})
	}
	contents = append(contents, &genai.Content{
		Role:  "user",
		Parts: []*genai.Part{{Text: question}},
	})

	result, err := g.client.Models.GenerateContent(ctx,
		g.model,
		contents,
		&genai.GenerateContentConfig{
			SystemInstruction: &genai.Content{
				Parts: []*genai.Part{{Text: sysMsg}},
			},
		},
	)
	if err != nil {
		return "", fmt.Errorf("gemini generate content: %w", err)
	}

	if len(result.Candidates) == 0 || result.Candidates[0].Content == nil || len(result.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("gemini returned empty response")
	}

	return result.Candidates[0].Content.Parts[0].Text, nil
}
