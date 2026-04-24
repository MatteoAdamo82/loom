package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MatteoAdamo82/loom/internal/llm"
)

const analyzeSystem = `You are Loom's ingest analyzer.
Given a source document, produce a compact structured summary.

Rules:
- Output strictly the JSON object described in the user message. No prose.
- "title": short canonical title (≤ 90 chars). Prefer the document's own title.
- "summary": 2–3 sentences, dense, factual, no filler.
- "keywords": 5–10 lowercase topical keywords, 1–3 words each, no duplicates.
- "entities": distinct people, organisations, products, places, or named
  concepts that the document treats as first-class subjects. Skip trivia.
- Keep entities proper-cased as they appear in the source.`

const analyzeUserTemplate = `Analyze this document and return JSON with exactly these keys:
{
  "title":    string,
  "summary":  string,
  "keywords": [string, ...],
  "entities": [{"name": string, "kind": "person|organization|product|place|concept|event|work"}, ...]
}

---
%s
---`

// AnalysisResult mirrors what the analyzer LLM pass is asked to return.
type AnalysisResult struct {
	Title    string   `json:"title"`
	Summary  string   `json:"summary"`
	Keywords []string `json:"keywords"`
	Entities []Entity `json:"entities"`
}

type Entity struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// Analyze runs the ingest-analyzer prompt on the given source text. The caller
// is responsible for trimming very large inputs to fit the model context.
func Analyze(ctx context.Context, client llm.Client, text string) (*AnalysisResult, error) {
	resp, err := client.Chat(ctx, llm.ChatRequest{
		JSON:        true,
		Temperature: 0.1,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: analyzeSystem},
			{Role: llm.RoleUser, Content: fmt.Sprintf(analyzeUserTemplate, text)},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("llm analyze: %w", err)
	}
	raw := strings.TrimSpace(resp.Content)
	raw = stripCodeFence(raw)

	var out AnalysisResult
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("parse analyze output (%q): %w", raw, err)
	}
	out.normalize()
	return &out, nil
}

func (r *AnalysisResult) normalize() {
	r.Title = strings.TrimSpace(r.Title)
	r.Summary = strings.TrimSpace(r.Summary)
	seenKW := map[string]struct{}{}
	var kw []string
	for _, k := range r.Keywords {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		if _, ok := seenKW[k]; ok {
			continue
		}
		seenKW[k] = struct{}{}
		kw = append(kw, k)
	}
	r.Keywords = kw

	seenE := map[string]struct{}{}
	var ents []Entity
	for _, e := range r.Entities {
		e.Name = strings.TrimSpace(e.Name)
		e.Kind = strings.ToLower(strings.TrimSpace(e.Kind))
		if e.Name == "" {
			continue
		}
		if e.Kind == "" {
			e.Kind = "concept"
		}
		key := strings.ToLower(e.Name)
		if _, ok := seenE[key]; ok {
			continue
		}
		seenE[key] = struct{}{}
		ents = append(ents, e)
	}
	r.Entities = ents
}

// stripCodeFence removes a ```json ... ``` wrapper if the model emits one
// despite being asked for raw JSON.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// drop first line (``` or ```json)
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	}
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "```") {
		s = strings.TrimSuffix(s, "```")
	}
	return strings.TrimSpace(s)
}
