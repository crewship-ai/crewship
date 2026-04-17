package server

import (
	"context"
	"fmt"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/llm"
)

// llmSummarizer adapts an llm.Provider to consolidate.SummarizerClient so
// the memory consolidation worker can extract semantic rules from journal
// entries via whatever LLM the workspace has configured. The adapter is
// deliberately thin — it exists only to rebind the Summarize(prompt)
// single-argument contract to llm.Provider.Complete's richer Request
// struct, picking a sensible default model when the caller doesn't care.
//
// Kept in the server package rather than inside consolidate/ because the
// model string choice is deployment-dependent (local Ollama vs. cloud
// Anthropic) and the consolidate package stays provider-neutral.
type llmSummarizer struct {
	provider llm.Provider
	model    string
}

func newLLMSummarizer(p llm.Provider, model string) consolidate.SummarizerClient {
	if model == "" {
		// Default to the smallest Haiku — consolidation prompts are short
		// and cost-sensitive; the bigger models don't improve rule
		// extraction quality enough to justify the 10x price.
		model = "claude-haiku-4-5-20251001"
	}
	return &llmSummarizer{provider: p, model: model}
}

func (s *llmSummarizer) Summarize(ctx context.Context, prompt string) (string, error) {
	resp, err := s.provider.Complete(ctx, llm.Request{
		Model:     s.model,
		System:    "You extract stable semantic rules from agent event streams. Output ONLY valid JSON matching the requested schema. No prose, no markdown fences.",
		MaxTokens: 2048,
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: prompt}},
	})
	if err != nil {
		return "", fmt.Errorf("summarizer complete: %w", err)
	}
	return resp.Content, nil
}
