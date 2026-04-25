package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/MatteoAdamo82/loom/internal/llm"
)

// minAnalyzeChars guards against models that hallucinate "please provide the
// document" when handed an empty or trivially-short input. We still allow a
// few hundred chars (a tweet, a one-paragraph note) but bail on anything
// shorter than this threshold.
const minAnalyzeChars = 40

const analyzeSystem = `You are Loom's ingest analyzer.

CRITICAL OUTPUT RULES:
- Reply with a SINGLE JSON object and nothing else. No prose, no questions,
  no apologies, no code fences, no preamble like "Here is".
- The user's message ALREADY contains the document inline between
  "==== BEGIN DOCUMENT ====" and "==== END DOCUMENT ====" markers. Never
  ask the user to provide a document — it is already there. If the document
  is short, terse, or in another language, do your best with what you have.

CONTENT RULES:
- "title": short canonical title (≤ 90 chars). Prefer the document's own title.
- "summary": 2–3 sentences, dense, factual, no filler.
- "keywords": 5–10 lowercase topical keywords, 1–3 words each, no duplicates.
- "entities": distinct people, organisations, products, places, or named
  concepts that the document treats as first-class subjects. Skip trivia.
  Keep entity names proper-cased as they appear in the source.`

const analyzeUserTemplate = `Below is a document to analyze. Return ONLY a JSON object — no preamble, no questions, no code fences.

Required JSON shape (replace the placeholder values with real ones drawn from the document):
{
  "title":    "<short canonical title>",
  "summary":  "<2-3 dense factual sentences>",
  "keywords": ["lowercase","topical","keywords","5-to-10","entries"],
  "entities": [{"name": "Proper Name", "kind": "person|organization|product|place|concept|event|work"}]
}

==== BEGIN DOCUMENT ====
%s
==== END DOCUMENT ====

Return the JSON object now.`

// retryAnalyzeSystem is used on a single retry after the model returns
// non-JSON. It's strictly more aggressive about formatting and tells the
// model exactly what went wrong.
const retryAnalyzeSystem = `You returned text that wasn't valid JSON. Retry.
Output ONLY the JSON object — start your response with "{" and end it with "}".
The document is in the user message between BEGIN/END DOCUMENT markers; analyze it, do not ask for it.`

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

// ErrEmptyDocument is returned when the extractor produced no usable content.
// The pipeline surfaces this verbatim to the caller so the GUI/CLI can show
// a clear "the file looks empty" message instead of a confusing JSON parse
// error.
var ErrEmptyDocument = errors.New("ingest: extracted document is empty or too short")

// Analyze runs the ingest-analyzer prompt on the given source text. The caller
// is responsible for trimming very large inputs to fit the model context.
//
// On a parse failure, Analyze retries once with a blunter system prompt
// before giving up.
func Analyze(ctx context.Context, client llm.Client, text string) (*AnalysisResult, error) {
	if len(strings.TrimSpace(text)) < minAnalyzeChars {
		return nil, fmt.Errorf("%w (got %d chars, need at least %d)",
			ErrEmptyDocument, len(strings.TrimSpace(text)), minAnalyzeChars)
	}

	userMsg := fmt.Sprintf(analyzeUserTemplate, text)
	out, raw, err := callAnalyze(ctx, client, analyzeSystem, userMsg)
	if err == nil {
		out.normalize()
		return out, nil
	}

	// Single retry with a blunter prompt that includes the model's last
	// (broken) reply so it understands what to fix.
	retryUser := fmt.Sprintf(
		"Your previous reply was not valid JSON:\n>>> %s <<<\n\nReanalyze the document below and return ONLY the JSON object.\n\n%s",
		truncate(raw, 200), userMsg,
	)
	out, raw, err2 := callAnalyze(ctx, client, retryAnalyzeSystem, retryUser)
	if err2 != nil {
		// Surface the *original* parse error context — it's more actionable.
		return nil, err
	}
	out.normalize()
	return out, nil
}

// callAnalyze does the round-trip and returns either a parsed result or the
// raw response so the caller can inspect / retry on it.
func callAnalyze(ctx context.Context, client llm.Client, system, user string) (*AnalysisResult, string, error) {
	resp, err := client.Chat(ctx, llm.ChatRequest{
		JSON:        true,
		Temperature: 0.1,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: system},
			{Role: llm.RoleUser, Content: user},
		},
	})
	if err != nil {
		return nil, "", fmt.Errorf("llm analyze: %w", err)
	}
	raw := stripCodeFence(strings.TrimSpace(resp.Content))

	var out AnalysisResult
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, raw, fmt.Errorf("parse analyze output (%q): %w", truncate(raw, 200), err)
	}
	return &out, raw, nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
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
