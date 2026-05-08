package index

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MatteoAdamo82/loom/internal/llm"
)

// maxAnalyzeChars caps how much of a long document we send for summarisation.
// The first N characters usually contain the title, abstract, and intro of
// any document — good enough for a 200-word summary plus keywords.
const maxAnalyzeChars = 16000

const summarisePrompt = `You are indexing a knowledge base. Given the file below, return a JSON object with two fields:
- "summary": 150-250 words, plain prose, capturing what the document is about and its key claims or topics. Italian or English depending on the source language.
- "keywords": 5-8 short lower-case strings (one or two words each) that describe the topics and would help retrieve this document by keyword search.

Return ONLY the JSON object, no preamble, no markdown fences.`

// Summarise asks the LLM for a one-shot summary + keyword set. It returns
// usable defaults if the LLM response can't be parsed (so a flaky model
// doesn't poison the index).
func Summarise(ctx context.Context, lc llm.Client, title, content string) (string, []string, error) {
	body := content
	if len(body) > maxAnalyzeChars {
		body = body[:maxAnalyzeChars] + "\n\n[...truncated]"
	}

	user := fmt.Sprintf("TITLE: %s\n\n---\n\n%s", title, body)

	resp, err := lc.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: summarisePrompt},
			{Role: llm.RoleUser, Content: user},
		},
		JSON:        true,
		Temperature: 0.2,
		MaxTokens:   600,
	})
	if err != nil {
		return "", nil, err
	}

	summary, keywords, perr := parseSummariseJSON(resp.Content)
	if perr != nil {
		// Fall back to a heuristic so the file still gets indexed.
		return fallbackSummary(title, content), fallbackKeywords(title), nil
	}
	return summary, keywords, nil
}

type summariseOutput struct {
	Summary  string   `json:"summary"`
	Keywords []string `json:"keywords"`
}

func parseSummariseJSON(s string) (string, []string, error) {
	s = strings.TrimSpace(s)
	// Strip ```json fences if the model added them.
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	// Find the first { ... } block to be tolerant of preambles.
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	if i := strings.LastIndex(s, "}"); i >= 0 && i < len(s)-1 {
		s = s[:i+1]
	}

	var out summariseOutput
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return "", nil, err
	}
	out.Summary = strings.TrimSpace(out.Summary)
	if out.Summary == "" {
		return "", nil, fmt.Errorf("empty summary")
	}
	cleaned := make([]string, 0, len(out.Keywords))
	for _, k := range out.Keywords {
		k = strings.TrimSpace(strings.ToLower(k))
		if k != "" {
			cleaned = append(cleaned, k)
		}
	}
	return out.Summary, cleaned, nil
}

// fallbackSummary takes the first 250 words of the content as a safety net.
func fallbackSummary(title, content string) string {
	words := strings.Fields(content)
	if len(words) > 80 {
		words = words[:80]
	}
	preview := strings.Join(words, " ")
	if title != "" {
		return title + " — " + preview
	}
	return preview
}

// fallbackKeywords splits the title into rough keywords. Better than nothing.
func fallbackKeywords(title string) []string {
	parts := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return !('a' <= r && r <= 'z' || '0' <= r && r <= '9')
	})
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, p := range parts {
		if len(p) < 3 || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
		if len(out) >= 6 {
			break
		}
	}
	return out
}
