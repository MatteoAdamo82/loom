package query

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MatteoAdamo82/loom/internal/llm"
)

const expandSystem = `You are Loom's query expander.
Given a user question, produce 3–5 distinct keyword queries that would hit
relevant notes in a BM25 full-text index.

Rules:
- Output strict JSON: {"queries": [string, ...]}
- Each query is 2–6 lowercase words, no punctuation, no quotes.
- Cover: (a) the literal question, (b) synonyms / aliases, (c) adjacent concepts.
- No duplicates. No explanations.`

const expandUserTemplate = `Question: %s

Produce the JSON object.`

type expandOutput struct {
	Queries []string `json:"queries"`
}

func Expand(ctx context.Context, client llm.Client, question string) ([]string, error) {
	resp, err := client.Chat(ctx, llm.ChatRequest{
		JSON:        true,
		Temperature: 0.2,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: expandSystem},
			{Role: llm.RoleUser, Content: fmt.Sprintf(expandUserTemplate, question)},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("expand: %w", err)
	}
	raw := stripCodeFence(strings.TrimSpace(resp.Content))

	var out expandOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("parse expand output (%q): %w", raw, err)
	}
	return dedupNonEmpty(out.Queries), nil
}

func dedupNonEmpty(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	}
	s = strings.TrimSpace(s)
	return strings.TrimSpace(strings.TrimSuffix(s, "```"))
}
