// Package query implements Loom's retrieval pipeline:
// LLM expand → BM25 search → graph boost → LLM rerank → LLM synthesize.
package query

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/MatteoAdamo82/loom/internal/llm"
	"github.com/MatteoAdamo82/loom/internal/storage"
)

type Candidate struct {
	EntityRef string
	Title     string
	Snippet   string
	// FullContent is the hydrated body loaded after rerank — the actual note
	// content, source excerpt, or chunk text that synthesis should ground on.
	// Empty until hydrateContent runs; Synthesize falls back to Snippet.
	FullContent string
	Score       float64
	Kind        string
	NoteID      int64 // 0 when the hit isn't a note
	SourceID    int64 // 0 when the hit isn't a source/chunk-from-source
}

// Source content is trimmed during hydration so a single large document can't
// monopolize the synthesis context.
const sourceCharBudget = 1500

// Total budget across all hydrated candidates; longest candidates are shrunk
// first so short ones stay intact.
const contextCharBudget = 8000

type Answer struct {
	Question string
	Content  string
	Citations []Citation
	Candidates []Candidate // what the LLM was shown (after rerank)
	Expanded  []string     // the BM25 queries the expander produced
}

type Citation struct {
	EntityRef string
	Title     string
}

type Config struct {
	BM25TopK       int
	GraphExpandHop int
	RerankTopK     int
	Model          string
	RerankModel    string
	// Format controls the synthesized answer's shape (markdown, marp, text).
	// Empty defaults to FormatMarkdown.
	Format Format
}

func (c Config) withDefaults() Config {
	if c.BM25TopK == 0 {
		c.BM25TopK = 30
	}
	if c.RerankTopK == 0 {
		c.RerankTopK = 8
	}
	if c.GraphExpandHop == 0 {
		c.GraphExpandHop = 1
	}
	return c
}

type Engine struct {
	Store *storage.Store
	LLM   llm.Client
	Cfg   Config
	// OnSynthesisChunk, when non-nil, receives successive content deltas
	// from the synthesis step as they arrive from the LLM. Wire this from
	// the CLI to render tokens live; leave nil to keep Run() blocking.
	OnSynthesisChunk func(string)
}

func NewEngine(store *storage.Store, client llm.Client) *Engine {
	return &Engine{Store: store, LLM: client, Cfg: Config{}.withDefaults()}
}

// Run executes the full pipeline for a user question.
func (e *Engine) Run(ctx context.Context, question string) (*Answer, error) {
	cfg := e.Cfg.withDefaults()

	// 1. Expand.
	queries, err := Expand(ctx, e.LLM, question)
	if err != nil {
		return nil, err
	}
	if len(queries) == 0 {
		queries = []string{question}
	}

	// 2. Multi-query BM25 with RRF merge.
	candidates, err := e.searchAndMerge(ctx, queries, cfg.BM25TopK)
	if err != nil {
		return nil, err
	}

	// 3. Graph boost: pull 1-hop note neighbors for the best note candidates.
	if cfg.GraphExpandHop > 0 {
		boosted, err := e.graphBoost(ctx, candidates)
		if err != nil {
			return nil, err
		}
		candidates = boosted
	}

	if len(candidates) == 0 {
		return &Answer{
			Question: question,
			Content:  "No relevant notes or sources found.",
			Expanded: queries,
		}, nil
	}

	// 4. Rerank via LLM.
	top := candidates
	if len(top) > cfg.BM25TopK {
		top = top[:cfg.BM25TopK]
	}
	ranked, err := Rerank(ctx, e.LLM, question, top, cfg.RerankTopK)
	if err != nil {
		return nil, err
	}

	// 5. Hydrate: load full bodies so synthesis sees actual content, not
	//    12-word FTS snippets.
	ranked = e.hydrateContent(ctx, ranked)
	ranked = enforceContextBudget(ranked, contextCharBudget)

	// 6. Synthesize.
	content, err := Synthesize(ctx, e.LLM, question, ranked, cfg.Format, e.OnSynthesisChunk)
	if err != nil {
		return nil, err
	}

	citations := make([]Citation, 0, len(ranked))
	for _, c := range ranked {
		citations = append(citations, Citation{EntityRef: c.EntityRef, Title: c.Title})
	}

	// 7. Log.
	details, _ := json.Marshal(map[string]any{
		"question":    question,
		"expanded":    queries,
		"candidates":  len(candidates),
		"reranked":    len(ranked),
	})
	_ = e.Store.LogOperation(ctx, &storage.Operation{
		Kind:    "query",
		Actor:   e.LLM.Name(),
		Summary: firstLine(question, 120),
		Details: details,
	})

	return &Answer{
		Question:   question,
		Content:    content,
		Citations:  citations,
		Candidates: ranked,
		Expanded:   queries,
	}, nil
}

func (e *Engine) searchAndMerge(ctx context.Context, queries []string, topK int) ([]Candidate, error) {
	// Reciprocal Rank Fusion with k=60 (standard).
	type bucket struct {
		c     Candidate
		score float64
	}
	merged := map[string]*bucket{}

	for _, q := range queries {
		qSafe := sanitizeFTSQuery(q)
		if qSafe == "" {
			continue
		}
		hits, err := e.Store.Search(ctx, qSafe, topK)
		if err != nil {
			// Skip query errors so one malformed expansion doesn't sink the
			// whole run; log them in future via an observer hook.
			continue
		}
		for rank, h := range hits {
			rrf := 1.0 / float64(60+rank+1)
			if b, ok := merged[h.EntityRef]; ok {
				b.score += rrf
				continue
			}
			c := toCandidate(h)
			merged[h.EntityRef] = &bucket{c: c, score: rrf}
		}
	}

	out := make([]Candidate, 0, len(merged))
	for _, b := range merged {
		c := b.c
		c.Score = b.score
		out = append(out, c)
	}
	sortByScoreDesc(out)
	if len(out) > topK {
		out = out[:topK]
	}
	return out, nil
}

func (e *Engine) graphBoost(ctx context.Context, in []Candidate) ([]Candidate, error) {
	seen := make(map[string]int, len(in))
	out := make([]Candidate, len(in))
	copy(out, in)
	for i, c := range in {
		seen[c.EntityRef] = i
	}

	var notes []int64
	for _, c := range in {
		if c.NoteID != 0 {
			notes = append(notes, c.NoteID)
		}
		if len(notes) >= 5 {
			break
		}
	}

	for _, id := range notes {
		links, err := e.Store.LinksFromNote(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, l := range links {
			if l.ToNoteID == nil {
				continue
			}
			ref := fmt.Sprintf("note:%d", *l.ToNoteID)
			if _, ok := seen[ref]; ok {
				continue
			}
			n, err := e.Store.GetNote(ctx, *l.ToNoteID)
			if err != nil {
				continue
			}
			out = append(out, Candidate{
				EntityRef: ref,
				Title:     n.Title,
				Snippet:   n.Summary,
				Score:     0.0, // lower than organic hits; rerank decides
				Kind:      n.Kind,
				NoteID:    n.ID,
			})
			seen[ref] = len(out) - 1
		}
	}
	return out, nil
}

// hydrateContent fills Candidate.FullContent from the store so synthesis has
// the actual note body (not the 12-word FTS snippet). Errors on individual
// loads are swallowed: the caller falls back to Snippet rather than failing
// the whole query.
func (e *Engine) hydrateContent(ctx context.Context, in []Candidate) []Candidate {
	out := make([]Candidate, len(in))
	copy(out, in)
	for i := range out {
		c := &out[i]
		switch {
		case c.NoteID != 0:
			n, err := e.Store.GetNote(ctx, c.NoteID)
			if err == nil {
				c.FullContent = n.Content
			}
		case c.SourceID != 0:
			src, err := e.Store.GetSource(ctx, c.SourceID)
			if err == nil {
				c.FullContent = truncateString(src.Content, sourceCharBudget)
			}
		case strings.HasPrefix(c.EntityRef, "chunk:"):
			var id int64
			if _, err := fmt.Sscanf(c.EntityRef, "chunk:%d", &id); err != nil || id == 0 {
				continue
			}
			ch, err := e.Store.GetChunk(ctx, id)
			if err == nil {
				c.FullContent = ch.Content
			}
		}
	}
	return out
}

// enforceContextBudget caps the combined FullContent across candidates at
// budget characters. It applies a water-filling strategy: candidates shorter
// than their fair share keep their body intact, while the longest absorb the
// truncation. This preserves diversity across sources instead of letting one
// large note starve the rest.
func enforceContextBudget(cands []Candidate, budget int) []Candidate {
	if budget <= 0 || len(cands) == 0 {
		return cands
	}
	total := 0
	for _, c := range cands {
		total += len(c.FullContent)
	}
	if total <= budget {
		return cands
	}

	order := make([]int, len(cands))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return len(cands[order[a]].FullContent) < len(cands[order[b]].FullContent)
	})

	remaining := budget
	remainingCount := len(cands)
	for _, i := range order {
		fair := remaining / remainingCount
		if len(cands[i].FullContent) > fair {
			cands[i].FullContent = truncateString(cands[i].FullContent, fair)
		}
		remaining -= len(cands[i].FullContent)
		remainingCount--
	}
	return cands
}

// truncateString cuts s to at most n bytes total — including a trailing
// ellipsis when truncation happened — at a valid UTF-8 rune boundary.
// Returns s unchanged when already short enough.
func truncateString(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	const ellipsis = "…"
	budget := n - len(ellipsis)
	if budget <= 0 {
		// Budget too small to fit the marker; byte-truncate at a rune boundary.
		return utf8Prefix(s, n)
	}
	return utf8Prefix(s, budget) + ellipsis
}

// utf8Prefix returns the longest prefix of s whose length is ≤ n bytes and
// ends on a rune boundary.
func utf8Prefix(s string, n int) string {
	if n >= len(s) {
		return s
	}
	i := 0
	for i < n {
		_, size := utf8.DecodeRuneInString(s[i:])
		if size == 0 || i+size > n {
			break
		}
		i += size
	}
	return s[:i]
}

func toCandidate(h storage.SearchHit) Candidate {
	c := Candidate{
		EntityRef: h.EntityRef,
		Title:     h.Title,
		Snippet:   h.Snippet,
		Score:     h.Score,
		Kind:      h.Kind,
	}
	var id int64
	switch {
	case strings.HasPrefix(h.EntityRef, "note:"):
		_, _ = fmt.Sscanf(h.EntityRef, "note:%d", &id)
		c.NoteID = id
	case strings.HasPrefix(h.EntityRef, "source:"):
		_, _ = fmt.Sscanf(h.EntityRef, "source:%d", &id)
		c.SourceID = id
	case strings.HasPrefix(h.EntityRef, "chunk:"):
		// chunks reference their source indirectly; leave IDs as zero for now
	}
	return c
}

func sortByScoreDesc(c []Candidate) {
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j-1].Score < c[j].Score; j-- {
			c[j-1], c[j] = c[j], c[j-1]
		}
	}
}

// sanitizeFTSQuery turns a natural-language line into an FTS5 MATCH
// expression that won't blow up on punctuation. Each word becomes a term,
// joined by implicit OR.
func sanitizeFTSQuery(q string) string {
	fields := strings.FieldsFunc(q, func(r rune) bool {
		return !(r == '_' || r == '-' || r == '\'' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r > 127)
	})
	var out []string
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		// Quote each term to stop FTS from interpreting stray syntax.
		out = append(out, `"`+strings.ReplaceAll(f, `"`, ``)+`"`)
	}
	return strings.Join(out, " OR ")
}

func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[:nl]
	}
	if len(s) > max {
		s = s[:max-1] + "…"
	}
	return s
}
