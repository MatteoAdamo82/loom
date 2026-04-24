// Package query implements Loom's retrieval pipeline:
// LLM expand → BM25 search → graph boost → LLM rerank → LLM synthesize.
package query

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MatteoAdamo82/loom/internal/llm"
	"github.com/MatteoAdamo82/loom/internal/storage"
)

type Candidate struct {
	EntityRef string
	Title     string
	Snippet   string
	Score     float64
	Kind      string
	NoteID    int64 // 0 when the hit isn't a note
	SourceID  int64 // 0 when the hit isn't a source/chunk-from-source
}

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

	// 5. Synthesize.
	content, err := Synthesize(ctx, e.LLM, question, ranked)
	if err != nil {
		return nil, err
	}

	citations := make([]Citation, 0, len(ranked))
	for _, c := range ranked {
		citations = append(citations, Citation{EntityRef: c.EntityRef, Title: c.Title})
	}

	// 6. Log.
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
