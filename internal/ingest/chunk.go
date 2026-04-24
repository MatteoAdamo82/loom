// Package ingest orchestrates the pipeline that turns raw files into sources,
// chunks, notes, and links inside the Loom SQLite store.
package ingest

import (
	"strings"
	"unicode"
)

// ChunkConfig controls how text is split. Tokens are approximated at 1 word ≈
// 1.3 tokens (English/Italian mix); we avoid adding a real tokenizer until we
// integrate a provider-specific one.
type ChunkConfig struct {
	MaxTokens int // upper bound per chunk
	Overlap   int // token overlap between adjacent chunks
}

func (c ChunkConfig) withDefaults() ChunkConfig {
	if c.MaxTokens <= 0 {
		c.MaxTokens = 500
	}
	if c.Overlap < 0 {
		c.Overlap = 0
	}
	if c.Overlap >= c.MaxTokens {
		c.Overlap = c.MaxTokens / 5
	}
	return c
}

type Chunk struct {
	Content  string
	Position int
	Tokens   int
}

// Split breaks text into chunks at paragraph boundaries, packing paragraphs
// together up to MaxTokens. Oversized paragraphs are hard-split by words.
// Overlap is applied in whole-paragraph units to keep semantics intact.
func Split(text string, cfg ChunkConfig) []Chunk {
	cfg = cfg.withDefaults()

	paragraphs := splitParagraphs(text)
	if len(paragraphs) == 0 {
		return nil
	}

	var chunks []Chunk
	var buf []string
	bufTokens := 0

	flush := func() {
		if len(buf) == 0 {
			return
		}
		content := strings.Join(buf, "\n\n")
		chunks = append(chunks, Chunk{
			Content:  content,
			Position: len(chunks),
			Tokens:   approxTokens(content),
		})
		// Apply overlap: keep the last paragraphs whose combined tokens ≤ Overlap.
		if cfg.Overlap > 0 {
			keep := []string{}
			kept := 0
			for i := len(buf) - 1; i >= 0; i-- {
				t := approxTokens(buf[i])
				if kept+t > cfg.Overlap {
					break
				}
				keep = append([]string{buf[i]}, keep...)
				kept += t
			}
			buf = keep
			bufTokens = kept
		} else {
			buf = buf[:0]
			bufTokens = 0
		}
	}

	for _, p := range paragraphs {
		pTokens := approxTokens(p)

		// Oversized paragraph: hard-split by words.
		if pTokens > cfg.MaxTokens {
			flush()
			for _, piece := range splitByWordBudget(p, cfg.MaxTokens) {
				chunks = append(chunks, Chunk{
					Content:  piece,
					Position: len(chunks),
					Tokens:   approxTokens(piece),
				})
			}
			continue
		}

		if bufTokens+pTokens > cfg.MaxTokens && bufTokens > 0 {
			flush()
		}
		buf = append(buf, p)
		bufTokens += pTokens
	}
	flush()

	// Re-number positions after possible overlap-induced reorderings.
	for i := range chunks {
		chunks[i].Position = i
	}
	return chunks
}

func splitParagraphs(text string) []string {
	var out []string
	for _, p := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func approxTokens(s string) int {
	words := strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r)
	})
	if len(words) == 0 {
		return 0
	}
	// ~1.3 tokens per word is a reasonable heuristic across GPT-style tokenizers.
	return (len(words)*13 + 9) / 10
}

func splitByWordBudget(p string, maxTokens int) []string {
	words := strings.Fields(p)
	if len(words) == 0 {
		return nil
	}
	// derived from approxTokens: tokens ≈ words*1.3  =>  words ≈ tokens/1.3
	maxWords := (maxTokens * 10) / 13
	if maxWords < 1 {
		maxWords = 1
	}
	var out []string
	for i := 0; i < len(words); i += maxWords {
		j := i + maxWords
		if j > len(words) {
			j = len(words)
		}
		out = append(out, strings.Join(words[i:j], " "))
	}
	return out
}
