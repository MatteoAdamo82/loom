package query

import (
	"context"
	"fmt"
	"strings"

	"github.com/MatteoAdamo82/loom/internal/llm"
)

// Format selects the output shape of the synthesized answer. Markdown is the
// default conversational style; Marp emits a slide deck; Text strips
// markdown ornament and citations for piping into other tools.
type Format string

const (
	FormatMarkdown Format = "markdown"
	FormatMarp     Format = "marp"
	FormatText     Format = "text"
)

// ParseFormat normalises a user-supplied format string. Empty/unknown values
// fall back to Markdown.
func ParseFormat(s string) Format {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "marp", "slides", "presentation":
		return FormatMarp
	case "text", "plain", "txt":
		return FormatText
	default:
		return FormatMarkdown
	}
}

const synthMarkdownSystem = `You are Loom's answer composer.
Compose a clear, concise answer to the user's question grounded ONLY in the
provided context notes. Cite notes by their id in square brackets, e.g.
[note:42]. If the context doesn't answer the question, say so plainly.

Rules:
- Do not invent facts. If something isn't in the context, don't claim it.
- Keep the answer short: aim for 4–10 sentences unless the question clearly
  needs more.
- End with a one-line "Sources:" list of the ids you actually cited.`

const synthMarpSystem = `You are Loom's slide-deck composer.
Compose a Marp presentation that answers the user's question grounded ONLY
in the provided context notes. The output must be valid Marp markdown that
will render directly with the marp-cli or the Obsidian Marp plugin.

Rules:
- Start with this YAML frontmatter on its own:
    ---
    marp: true
    theme: default
    paginate: true
    ---
- Use a horizontal rule "---" on its own line as the slide separator.
- 4–8 slides total. The first slide is a title slide with the question
  reframed as a topic. Subsequent slides each cover ONE point.
- Each content slide: an H2 title plus 1–4 short bullets. No paragraphs.
- Cite source notes inline as [note:42] or [source:7] at the end of the
  bullet that uses them.
- The final slide is titled "## Sources" and lists, as bullets, every id
  cited in the deck.
- Do not invent facts. If the context doesn't answer the question, produce
  a single slide that says so plainly and skip the Sources slide.
- Output only the Marp markdown, no surrounding prose or code fences.`

const synthTextSystem = `You are Loom's answer composer.
Answer the user's question grounded ONLY in the provided context notes.
Output is plain prose for piping into other tools — no markdown, no
bullets, no headings, no citations, no source list. Do not invent facts.
Keep it under ten sentences unless the question clearly needs more.`

const synthUserTemplate = `Question: %s

Context:
%s

Now write the answer.`

// Synthesize asks the LLM to compose a final answer in the requested format.
// An empty format falls back to Markdown.
func Synthesize(ctx context.Context, client llm.Client, question string, candidates []Candidate, format Format) (string, error) {
	if len(candidates) == 0 {
		return emptyAnswer(format), nil
	}
	var b strings.Builder
	for _, c := range candidates {
		body := c.FullContent
		if body == "" {
			body = c.Snippet
		}
		body = strings.TrimSpace(body)
		if strings.ContainsRune(body, '\n') {
			fmt.Fprintf(&b, "- %s [%s]:\n%s\n\n", c.Title, c.EntityRef, body)
		} else {
			fmt.Fprintf(&b, "- %s [%s]: %s\n", c.Title, c.EntityRef, body)
		}
	}

	resp, err := client.Chat(ctx, llm.ChatRequest{
		Temperature: 0.2,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: systemPromptFor(format)},
			{Role: llm.RoleUser, Content: fmt.Sprintf(synthUserTemplate, question, b.String())},
		},
	})
	if err != nil {
		return "", fmt.Errorf("synthesize: %w", err)
	}
	return strings.TrimSpace(resp.Content), nil
}

func systemPromptFor(format Format) string {
	switch format {
	case FormatMarp:
		return synthMarpSystem
	case FormatText:
		return synthTextSystem
	default:
		return synthMarkdownSystem
	}
}

func emptyAnswer(format Format) string {
	switch format {
	case FormatMarp:
		return `---
marp: true
theme: default
---

## No matches

The Loom knowledge base has nothing relevant to this question yet.`
	case FormatText:
		return "No relevant notes or sources found."
	default:
		return "No relevant notes or sources found."
	}
}
