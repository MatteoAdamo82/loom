package query

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MatteoAdamo82/loom/internal/llm"
)

const rerankSystem = `You are Loom's reranker.
Given a user question and a list of candidate notes (each with an id, title,
and snippet), return the ids of the most relevant candidates, ordered from
most to least relevant.

Rules:
- Output strict JSON: {"ranked": [id, ...]}
- Ids are the strings provided in the input (e.g. "note:42", "source:7").
- Include only candidates that plausibly answer the question.
- Up to the requested top_k; fewer is allowed.`

const rerankUserTemplate = `Question: %s

Candidates (JSON):
%s

Return the JSON object with the top %d ids.`

type rerankCandidate struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	Kind    string `json:"kind"`
}

type rerankOutput struct {
	Ranked []string `json:"ranked"`
}

func Rerank(ctx context.Context, client llm.Client, question string, candidates []Candidate, topK int) ([]Candidate, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	payload := make([]rerankCandidate, 0, len(candidates))
	for _, c := range candidates {
		payload = append(payload, rerankCandidate{
			ID:      c.EntityRef,
			Title:   c.Title,
			Snippet: c.Snippet,
			Kind:    c.Kind,
		})
	}
	jb, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	resp, err := client.Chat(ctx, llm.ChatRequest{
		JSON:        true,
		Temperature: 0.0,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: rerankSystem},
			{Role: llm.RoleUser, Content: fmt.Sprintf(rerankUserTemplate, question, string(jb), topK)},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("rerank: %w", err)
	}
	var out rerankOutput
	if err := json.Unmarshal([]byte(stripCodeFence(strings.TrimSpace(resp.Content))), &out); err != nil {
		return nil, fmt.Errorf("parse rerank output (%q): %w", resp.Content, err)
	}

	byRef := make(map[string]Candidate, len(candidates))
	for _, c := range candidates {
		byRef[c.EntityRef] = c
	}

	var ranked []Candidate
	seen := map[string]struct{}{}
	for _, id := range out.Ranked {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if c, ok := byRef[id]; ok {
			ranked = append(ranked, c)
		}
	}
	if len(ranked) > topK {
		ranked = ranked[:topK]
	}
	return ranked, nil
}
