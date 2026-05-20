// Package query answers a natural-language question against the indexed
// notes. It does ONE LLM call: BM25 search picks the top-K files, the LLM
// reads their summaries plus the most relevant slice of content and writes
// a grounded answer with [rel_path] citations.
package query

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/MatteoAdamo82/loom/internal/llm"
	"github.com/MatteoAdamo82/loom/internal/storage"
)

// DefaultTopK is how many BM25 hits we feed into the LLM by default.
const DefaultTopK = 5

// PerFileCharBudget caps how much body content we include per source. Five
// files × 8000 chars = 40K chars ≈ 10K tokens, leaves headroom for a 200K
// model and stays usable on smaller local models too.
const PerFileCharBudget = 8000

// Citation points the user back to a file referenced in the answer.
type Citation struct {
	RelPath string `json:"rel_path"`
	Title   string `json:"title"`
}

// Result is what Answer / Stream return when done.
type Result struct {
	Answer    string     `json:"answer"`
	Citations []Citation `json:"citations"`
	Used      []Citation `json:"used"` // every file passed to the LLM, whether cited or not
}

const systemPrompt = `You are answering a question against a private knowledge base.
Use ONLY the notes provided below. If the notes don't contain the answer, say so.
Cite each fact with the file's relative path in square brackets, e.g. [ricette.md] or [papers/llm-wiki.md].
Keep the answer concise. Reply in the same language as the question.`

// Answer runs a non-streaming query end-to-end.
func Answer(ctx context.Context, store *storage.Store, lc llm.Client, question string, topK int) (*Result, error) {
	hits, err := pickHits(ctx, store, question, topK)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		return &Result{Answer: "Nessuna nota indicizzata corrisponde alla domanda."}, nil
	}

	prompt := buildUserPrompt(question, hits)
	resp, err := lc.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: systemPrompt},
			{Role: llm.RoleUser, Content: prompt},
		},
		Temperature: 0.3,
		MaxTokens:   1500,
	})
	if err != nil {
		return nil, err
	}
	return finalise(resp.Content, hits), nil
}

// Stream runs the same query but emits content deltas via onChunk while the
// model writes. Falls back to Answer() if the LLM client doesn't implement
// Streamer.
func Stream(ctx context.Context, store *storage.Store, lc llm.Client, question string, topK int, onChunk func(string)) (*Result, error) {
	streamer, ok := lc.(llm.Streamer)
	if !ok {
		res, err := Answer(ctx, store, lc, question, topK)
		if err != nil {
			return nil, err
		}
		if onChunk != nil {
			onChunk(res.Answer)
		}
		return res, nil
	}

	hits, err := pickHits(ctx, store, question, topK)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		msg := "Nessuna nota indicizzata corrisponde alla domanda."
		if onChunk != nil {
			onChunk(msg)
		}
		return &Result{Answer: msg}, nil
	}

	prompt := buildUserPrompt(question, hits)
	resp, err := streamer.Stream(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: systemPrompt},
			{Role: llm.RoleUser, Content: prompt},
		},
		Temperature: 0.3,
		MaxTokens:   1500,
	}, onChunk)
	if err != nil {
		return nil, err
	}
	return finalise(resp.Content, hits), nil
}

// pickHits runs FTS, falls back to a "list everything" mode when the index is
// small enough that a simple tag-along of all notes beats keyword search.
func pickHits(ctx context.Context, store *storage.Store, question string, topK int) ([]*storage.Hit, error) {
	if topK <= 0 {
		topK = DefaultTopK
	}

	hits, err := store.Search(ctx, question, topK)
	if err == nil && len(hits) > 0 {
		return hits, nil
	}

	// FTS returned nothing (rare keywords, or empty query) — list everything
	// up to topK so the LLM still has a chance to answer or say "I don't
	// know" with context.
	files, lerr := store.List(ctx)
	if lerr != nil {
		return nil, lerr
	}
	if len(files) > topK {
		files = files[:topK]
	}
	out := make([]*storage.Hit, 0, len(files))
	for _, f := range files {
		out = append(out, &storage.Hit{File: *f})
	}
	return out, nil
}

// buildUserPrompt formats the BM25 hits into a plain-text prompt the model
// can chew on without ceremony.
func buildUserPrompt(question string, hits []*storage.Hit) string {
	var b strings.Builder
	b.WriteString("# Notes\n\n")
	for i, h := range hits {
		body := h.Content
		if len(body) > PerFileCharBudget {
			body = body[:PerFileCharBudget] + "\n[...truncated]"
		}
		fmt.Fprintf(&b, "## [%s] %s\n", h.RelPath, h.Title)
		if h.Summary != "" {
			fmt.Fprintf(&b, "_Summary:_ %s\n\n", h.Summary)
		}
		b.WriteString(body)
		if i < len(hits)-1 {
			b.WriteString("\n\n---\n\n")
		}
	}
	b.WriteString("\n\n# Question\n\n")
	b.WriteString(strings.TrimSpace(question))
	return b.String()
}

var citationRe = regexp.MustCompile(`\[([^\[\]]+\.[a-zA-Z0-9]+)\]`)

// finalise extracts the citations the model wrote and pairs them back with
// the hits they reference.
func finalise(answer string, hits []*storage.Hit) *Result {
	answer = strings.TrimSpace(answer)

	used := make([]Citation, 0, len(hits))
	for _, h := range hits {
		used = append(used, Citation{RelPath: h.RelPath, Title: h.Title})
	}

	cited := make([]Citation, 0)
	seen := map[string]bool{}
	for _, m := range citationRe.FindAllStringSubmatch(answer, -1) {
		path := m[1]
		if seen[path] {
			continue
		}
		seen[path] = true
		title := path
		for _, h := range hits {
			if h.RelPath == path {
				title = h.Title
				break
			}
		}
		cited = append(cited, Citation{RelPath: path, Title: title})
	}
	sort.SliceStable(cited, func(i, j int) bool { return cited[i].RelPath < cited[j].RelPath })

	return &Result{Answer: answer, Citations: cited, Used: used}
}
