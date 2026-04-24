package ingest

import (
	"strings"
	"testing"
)

func TestSplitEmpty(t *testing.T) {
	if got := Split("", ChunkConfig{}); got != nil {
		t.Errorf("empty text should yield no chunks, got %d", len(got))
	}
	if got := Split("   \n\n\n   ", ChunkConfig{}); got != nil {
		t.Errorf("whitespace-only text should yield no chunks, got %d", len(got))
	}
}

func TestSplitPacksParagraphs(t *testing.T) {
	text := strings.Join([]string{
		"first paragraph",
		"second paragraph with more words than the first one",
		"third",
	}, "\n\n")

	chunks := Split(text, ChunkConfig{MaxTokens: 1000})
	if len(chunks) != 1 {
		t.Fatalf("small paragraphs should fit one chunk, got %d", len(chunks))
	}
	if !strings.Contains(chunks[0].Content, "third") {
		t.Errorf("chunk missing tail paragraph: %q", chunks[0].Content)
	}
}

func TestSplitRespectsTokenBudget(t *testing.T) {
	// ~14 tokens per paragraph (10 words * 1.3 rounded).
	para := strings.Repeat("word ", 10)
	text := strings.Join([]string{para, para, para, para}, "\n\n")

	chunks := Split(text, ChunkConfig{MaxTokens: 20})
	if len(chunks) < 3 {
		t.Errorf("small budget should produce multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if c.Tokens > 25 {
			t.Errorf("chunk %d exceeded budget: %d tokens", i, c.Tokens)
		}
	}
	// positions are sequential
	for i, c := range chunks {
		if c.Position != i {
			t.Errorf("chunk %d position = %d", i, c.Position)
		}
	}
}

func TestSplitHardSplitsOversizedParagraph(t *testing.T) {
	// One huge paragraph, no blank-line boundaries.
	big := strings.Repeat("word ", 1000)
	chunks := Split(big, ChunkConfig{MaxTokens: 50})
	if len(chunks) < 10 {
		t.Errorf("huge paragraph should be hard-split, got %d chunks", len(chunks))
	}
	for i, c := range chunks {
		if c.Tokens > 60 { // some slack for rounding
			t.Errorf("chunk %d overshoots: %d tokens", i, c.Tokens)
		}
	}
}

func TestSplitOverlap(t *testing.T) {
	paragraphs := []string{
		"alpha alpha alpha alpha alpha",
		"beta beta beta beta beta",
		"gamma gamma gamma gamma gamma",
		"delta delta delta delta delta",
	}
	text := strings.Join(paragraphs, "\n\n")

	chunks := Split(text, ChunkConfig{MaxTokens: 10, Overlap: 7})
	if len(chunks) < 2 {
		t.Fatalf("want multiple chunks with overlap, got %d", len(chunks))
	}
	// Adjacent chunks should share at least one paragraph.
	shared := false
	for _, p := range paragraphs {
		if strings.Contains(chunks[0].Content, p) && strings.Contains(chunks[1].Content, p) {
			shared = true
			break
		}
	}
	if !shared {
		t.Errorf("expected paragraph overlap between chunk 0 and 1:\n0=%q\n1=%q",
			chunks[0].Content, chunks[1].Content)
	}
}
