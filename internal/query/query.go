// Package query answers a natural-language question against the indexed
// notes. It does ONE LLM call: BM25 search picks the top-K files, the LLM
// reads their summaries plus the most relevant slice of content and writes
// a grounded answer with [rel_path] citations.
package query

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/MatteoAdamo82/loom/internal/llm"
	"github.com/MatteoAdamo82/loom/internal/storage"
)

const (
	// DefaultTopK is how many BM25 hits we feed into the LLM by default.
	DefaultTopK = 5

	// PerFileCharBudget caps how much body content we include per source. Five
	// files × 8000 chars ≈ 40K chars ≈ 10K tokens, leaves headroom for a 200K
	// model and stays usable on smaller local models too.
	PerFileCharBudget = 8000

	// queryTemperature / queryMaxTokens tune the answer generation call.
	queryTemperature = 0.3
	queryMaxTokens   = 1500

	// noIndexedNotesMsg is returned when FTS finds nothing and the index is
	// empty. Kept as a constant to avoid duplication and ease i18n later.
	noIndexedNotesMsg = "No indexed notes match the question."
)

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

// Answer runs a non-streaming query end-to-end. When emb is non-nil, retrieval
// is hybrid (BM25 + vector); pass nil for pure BM25.
func Answer(ctx context.Context, store *storage.Store, lc llm.Client, emb llm.Embedder, question string, topK int) (*Result, error) {
	return run(ctx, store, lc, emb, question, topK, nil)
}

// Stream runs the same query but emits content deltas via onChunk while the
// model writes. Falls back to Answer() if the LLM client doesn't implement
// Streamer.
func Stream(ctx context.Context, store *storage.Store, lc llm.Client, emb llm.Embedder, question string, topK int, onChunk func(string)) (*Result, error) {
	return run(ctx, store, lc, emb, question, topK, onChunk)
}

// run is the single implementation shared by Answer and Stream.
// When onChunk is nil, or the client doesn't implement llm.Streamer, it falls
// back to a regular Chat() call.
func run(ctx context.Context, store *storage.Store, lc llm.Client, emb llm.Embedder, question string, topK int, onChunk func(string)) (*Result, error) {
	hits, err := pickHits(ctx, store, emb, question, topK)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		if onChunk != nil {
			onChunk(noIndexedNotesMsg)
		}
		return &Result{Answer: noIndexedNotesMsg}, nil
	}

	chatReq := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: systemPrompt},
			{Role: llm.RoleUser, Content: buildUserPrompt(question, hits)},
		},
		Temperature: queryTemperature,
		MaxTokens:   queryMaxTokens,
	}

	var content string
	streamer, canStream := lc.(llm.Streamer)
	if onChunk != nil && canStream {
		resp, err := streamer.Stream(ctx, chatReq, onChunk)
		if err != nil {
			return nil, err
		}
		content = resp.Content
	} else {
		resp, err := lc.Chat(ctx, chatReq)
		if err != nil {
			return nil, err
		}
		if onChunk != nil {
			onChunk(resp.Content) // fallback: single chunk
		}
		content = resp.Content
	}

	return finalise(content, hits), nil
}

// Retrieve picks the top-K files for a question. With an embedder it fuses BM25
// and vector similarity (Reciprocal Rank Fusion); without one (emb == nil) it is
// plain BM25 — byte-for-byte the previous behaviour. A failed query embedding
// degrades to BM25 rather than erroring. Returns a possibly-empty slice; an
// empty index or no-match is (nil, nil), not an error.
func Retrieve(ctx context.Context, store *storage.Store, emb llm.Embedder, question string, topK int) ([]*storage.Hit, error) {
	if topK <= 0 {
		topK = DefaultTopK
	}
	if emb != nil {
		if vecs, err := emb.Embed(ctx, []string{question}); err == nil && len(vecs) == 1 && len(vecs[0]) > 0 {
			hits, herr := store.HybridSearch(ctx, question, vecs[0], topK)
			if herr == nil {
				return hits, nil
			}
			if !errors.Is(herr, storage.ErrNotFound) {
				return nil, herr
			}
			return nil, nil
		}
		// embedding unavailable → fall through to BM25
	}
	hits, err := store.Search(ctx, question, topK)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return nil, err
	}
	return hits, nil
}

// pickHits runs retrieval, falling back to listing all files when the index is
// small enough that a keyword-free fallback still gives the LLM useful context.
// A storage error is always propagated — it is not silently treated as "no
// results".
func pickHits(ctx context.Context, store *storage.Store, emb llm.Embedder, question string, topK int) ([]*storage.Hit, error) {
	if topK <= 0 {
		topK = DefaultTopK
	}

	hits, err := Retrieve(ctx, store, emb, question, topK)
	if err != nil {
		return nil, fmt.Errorf("search index: %w", err)
	}
	if len(hits) > 0 {
		return hits, nil
	}

	// FTS returned nothing (rare keywords, empty query, or brand-new index) —
	// list everything up to topK so the LLM can still answer or say "I don't
	// know" with some context rather than none.
	files, err := store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list index: %w", err)
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

// buildUserPrompt formats the BM25 hits into a plain-text prompt.
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

// finalise extracts citations written by the model and pairs them with hits.
func finalise(answer string, hits []*storage.Hit) *Result {
	answer = strings.TrimSpace(answer)

	used := make([]Citation, 0, len(hits))
	for _, h := range hits {
		used = append(used, Citation{RelPath: h.RelPath, Title: h.Title})
	}

	// Index hits by path for O(1) title lookup.
	titleByPath := make(map[string]string, len(hits))
	for _, h := range hits {
		titleByPath[h.RelPath] = h.Title
	}

	seen := make(map[string]bool)
	var cited []Citation
	for _, m := range citationRe.FindAllStringSubmatch(answer, -1) {
		path := m[1]
		if seen[path] {
			continue
		}
		seen[path] = true
		title := path
		if t, ok := titleByPath[path]; ok {
			title = t
		}
		cited = append(cited, Citation{RelPath: path, Title: title})
	}
	sort.SliceStable(cited, func(i, j int) bool { return cited[i].RelPath < cited[j].RelPath })

	return &Result{Answer: answer, Citations: cited, Used: used}
}
