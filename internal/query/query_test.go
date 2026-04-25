package query

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MatteoAdamo82/loom/internal/llm"
	"github.com/MatteoAdamo82/loom/internal/storage"
)

type scriptedLLM struct {
	responses []string
	calls     int
	requests  []llm.ChatRequest
}

func (s *scriptedLLM) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	s.requests = append(s.requests, req)
	if s.calls >= len(s.responses) {
		return &llm.ChatResponse{Content: s.responses[len(s.responses)-1]}, nil
	}
	r := s.responses[s.calls]
	s.calls++
	return &llm.ChatResponse{Content: r}, nil
}

func (s *scriptedLLM) Name() string { return "scripted" }

func seedStore(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	notes := []*storage.Note{
		{Slug: "karpathy", Title: "Andrej Karpathy", Kind: "entity",
			Content:  "Former director of AI at Tesla, built the autopilot team; now independent.",
			Summary:  "AI researcher formerly at OpenAI and Tesla.",
			Keywords: []string{"ai", "tesla", "autopilot", "openai"}},
		{Slug: "llm-wiki", Title: "LLM Wiki pattern", Kind: "concept",
			Content:  "A pattern where the LLM maintains a personal markdown wiki and updates backlinks.",
			Summary:  "LLM-maintained wiki as alternative to RAG.",
			Keywords: []string{"llm", "wiki", "knowledge-base"}},
		{Slug: "rag", Title: "Retrieval Augmented Generation", Kind: "concept",
			Content:  "Vector databases and embeddings support semantic retrieval for LLMs.",
			Summary:  "RAG overview.",
			Keywords: []string{"rag", "vector", "embedding"}},
	}
	for _, n := range notes {
		if err := s.CreateNote(ctx, n); err != nil {
			t.Fatal(err)
		}
		if err := s.IndexNote(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	// Link karpathy -> llm-wiki so graph boost has something to follow.
	linkNotes(t, s, "karpathy", "llm-wiki")
	return s
}

func linkNotes(t *testing.T, s *storage.Store, fromSlug, toSlug string) {
	t.Helper()
	ctx := context.Background()
	from, err := s.GetNoteBySlug(ctx, fromSlug)
	if err != nil {
		t.Fatal(err)
	}
	to, err := s.GetNoteBySlug(ctx, toSlug)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateLink(ctx, &storage.Link{
		FromNoteID: &from.ID, ToNoteID: &to.ID,
		Kind: storage.LinkWikilink, Context: "test-seeded",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestExpandParsesJSON(t *testing.T) {
	fake := &scriptedLLM{responses: []string{
		`{"queries":["karpathy tesla","autopilot team","openai cofounder","karpathy tesla","   "]}`,
	}}
	qs, err := Expand(context.Background(), fake, "Who founded Tesla's autopilot team?")
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(qs) != 3 {
		t.Errorf("got %d queries after dedup, want 3: %v", len(qs), qs)
	}
}

func TestRerankPreservesOnlyKnownIDs(t *testing.T) {
	cands := []Candidate{
		{EntityRef: "note:1", Title: "A"},
		{EntityRef: "note:2", Title: "B"},
		{EntityRef: "note:3", Title: "C"},
	}
	fake := &scriptedLLM{responses: []string{
		`{"ranked":["note:3","note:9","note:1"]}`,
	}}
	out, err := Rerank(context.Background(), fake, "q", cands, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 known ids, got %d: %+v", len(out), out)
	}
	if out[0].EntityRef != "note:3" || out[1].EntityRef != "note:1" {
		t.Errorf("unexpected rerank order: %+v", out)
	}
}

func TestEngineEndToEnd(t *testing.T) {
	s := seedStore(t)

	// Expand → BM25 search → (graph boost adds llm-wiki) → rerank → synthesize
	fake := &scriptedLLM{responses: []string{
		`{"queries":["karpathy autopilot","andrej karpathy","tesla ai"]}`,
		`{"ranked":["note:1","note:2"]}`, // note:1 is karpathy, note:2 is llm-wiki
		"Karpathy led Tesla's autopilot team. [note:1]\n\nSources: [note:1]",
	}}

	eng := NewEngine(s, fake)
	ans, err := eng.Run(context.Background(), "Who led Tesla's autopilot team?")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(ans.Content, "Karpathy") {
		t.Errorf("answer missing expected entity: %q", ans.Content)
	}
	if len(ans.Candidates) == 0 {
		t.Error("expected rerank candidates")
	}
	if len(ans.Citations) == 0 {
		t.Error("expected citations")
	}

	// operations log has a query entry
	var c int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM operations WHERE kind='query'`).Scan(&c); err != nil {
		t.Fatal(err)
	}
	if c != 1 {
		t.Errorf("query ops = %d, want 1", c)
	}
}

func TestEngineEmptyResult(t *testing.T) {
	s := seedStore(t)
	fake := &scriptedLLM{responses: []string{
		`{"queries":["zzzzz not in index qwerty"]}`,
		`{"ranked":[]}`,
		"Should not reach synthesize.",
	}}
	eng := NewEngine(s, fake)
	ans, err := eng.Run(context.Background(), "random unrelated question about qwerty")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ans.Content, "No relevant") {
		t.Errorf("unexpected answer for empty index: %q", ans.Content)
	}
}

func TestSynthesizeReceivesFullNoteBody(t *testing.T) {
	s := seedStore(t)
	ctx := context.Background()

	// The distinctive phrase lives only in Content; Summary and the 12-word
	// FTS snippet around "scaling" will both miss it.
	distinctive := "grep over markdown becomes unwieldy past a hundred notes"
	n := &storage.Note{
		Slug: "wiki-scaling", Title: "LLM Wiki scaling limits", Kind: "concept",
		Content: "The LLM Wiki pattern hits a wall at scale. Specifically, " +
			distinctive + ", and the model can no longer hold the full graph in its working context.",
		Summary:  "Why the wiki pattern stops working beyond ~100 notes.",
		Keywords: []string{"llm", "wiki", "scaling", "limits"},
	}
	if err := s.CreateNote(ctx, n); err != nil {
		t.Fatal(err)
	}
	if err := s.IndexNote(ctx, n); err != nil {
		t.Fatal(err)
	}

	fake := &scriptedLLM{responses: []string{
		`{"queries":["llm wiki scaling","wiki pattern limits"]}`,
		fmt.Sprintf(`{"ranked":["note:%d"]}`, n.ID),
		"Because " + distinctive + ". [note:" + fmt.Sprint(n.ID) + "]\n\nSources: [note:" + fmt.Sprint(n.ID) + "]",
	}}
	eng := NewEngine(s, fake)
	ans, err := eng.Run(ctx, "Why does the LLM Wiki pattern stop working at scale?")
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// The synthesize call is the third LLM call in the pipeline.
	if len(fake.requests) < 3 {
		t.Fatalf("expected >=3 LLM calls, got %d", len(fake.requests))
	}
	synthReq := fake.requests[len(fake.requests)-1]
	var userContent string
	for _, m := range synthReq.Messages {
		if m.Role == llm.RoleUser {
			userContent = m.Content
		}
	}
	if !strings.Contains(userContent, distinctive) {
		t.Errorf("synthesize prompt missing full note body.\nGot:\n%s", userContent)
	}

	// The hydrated Candidate that the engine returns should carry FullContent.
	if len(ans.Candidates) == 0 || ans.Candidates[0].FullContent == "" {
		t.Errorf("expected top candidate to have FullContent set, got %+v", ans.Candidates)
	}
}

func TestEnforceContextBudgetTruncatesLongestFirst(t *testing.T) {
	cands := []Candidate{
		{EntityRef: "note:1", FullContent: strings.Repeat("a", 200)},
		{EntityRef: "note:2", FullContent: strings.Repeat("b", 50)},
		{EntityRef: "note:3", FullContent: strings.Repeat("c", 1000)},
	}
	out := enforceContextBudget(cands, 300)

	total := 0
	for _, c := range out {
		total += len(c.FullContent)
	}
	if total > 300 {
		t.Errorf("total %d exceeds budget 300", total)
	}
	// The 50-char candidate should remain untouched; the longest must shrink.
	if out[1].FullContent != strings.Repeat("b", 50) {
		t.Errorf("short candidate was mutated: %q", out[1].FullContent)
	}
	if len(out[2].FullContent) >= 1000 {
		t.Errorf("longest candidate was not truncated: len=%d", len(out[2].FullContent))
	}
}

func TestTruncateStringPreservesUTF8(t *testing.T) {
	// "è" is 2 bytes; cutting at an odd byte must not emit a broken rune.
	in := "perché così è"
	got := truncateString(in, 9)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis suffix, got %q", got)
	}
	// Stripped of the ellipsis, the prefix must be valid UTF-8.
	prefix := strings.TrimSuffix(got, "…")
	for _, r := range prefix {
		if r == '\uFFFD' {
			t.Errorf("invalid rune in truncated output: %q", got)
		}
	}
}

func TestParseFormat(t *testing.T) {
	cases := map[string]Format{
		"":                FormatMarkdown,
		"markdown":        FormatMarkdown,
		"MARKDOWN":        FormatMarkdown,
		"  marp  ":        FormatMarp,
		"slides":          FormatMarp,
		"presentation":    FormatMarp,
		"text":            FormatText,
		"plain":           FormatText,
		"unknown-thing":   FormatMarkdown, // safe fallback
	}
	for in, want := range cases {
		if got := ParseFormat(in); got != want {
			t.Errorf("ParseFormat(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSynthesizeMarpUsesSlideSystemPrompt(t *testing.T) {
	fake := &scriptedLLM{responses: []string{
		"---\nmarp: true\ntheme: default\npaginate: true\n---\n\n# Slide 1\n\n---\n\n## Body [note:1]",
	}}
	cands := []Candidate{
		{EntityRef: "note:1", Title: "T", FullContent: "body sentence."},
	}
	out, err := Synthesize(context.Background(), fake, "q?", cands, FormatMarp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "marp: true") {
		t.Errorf("Marp synth output missing frontmatter: %q", out)
	}

	// The first request must have used the Marp system prompt.
	if len(fake.requests) == 0 {
		t.Fatal("no LLM call captured")
	}
	var sysContent string
	for _, m := range fake.requests[0].Messages {
		if m.Role == llm.RoleSystem {
			sysContent = m.Content
			break
		}
	}
	if !strings.Contains(sysContent, "Marp presentation") {
		t.Errorf("Marp format should select the Marp system prompt; got %q", sysContent)
	}
}

func TestSynthesizeTextStripsCitations(t *testing.T) {
	fake := &scriptedLLM{responses: []string{"plain prose answer."}}
	cands := []Candidate{{EntityRef: "note:1", Title: "T", FullContent: "x"}}
	if _, err := Synthesize(context.Background(), fake, "q?", cands, FormatText); err != nil {
		t.Fatal(err)
	}
	var sysContent string
	for _, m := range fake.requests[0].Messages {
		if m.Role == llm.RoleSystem {
			sysContent = m.Content
			break
		}
	}
	if !strings.Contains(sysContent, "no citations") {
		t.Errorf("Text format should instruct no citations; got %q", sysContent)
	}
}

func TestSynthesizeEmptyCandidatesUsesFormatStub(t *testing.T) {
	out, _ := Synthesize(context.Background(), &scriptedLLM{}, "q", nil, FormatMarp)
	if !strings.Contains(out, "marp: true") {
		t.Errorf("empty Marp answer should still be a valid Marp doc, got %q", out)
	}
}

func TestSanitizeFTSQuery(t *testing.T) {
	cases := map[string]string{
		"hello world":    `"hello" OR "world"`,
		"what's up?":     `"what's" OR "up"`,
		"":               "",
		"(malicious *)":  `"malicious"`,
	}
	for in, want := range cases {
		got := sanitizeFTSQuery(in)
		if got != want {
			t.Errorf("sanitizeFTSQuery(%q) = %q, want %q", in, got, want)
		}
	}
}
