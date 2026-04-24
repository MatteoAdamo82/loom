package query

import (
	"context"
	"fmt"
	"strings"

	"github.com/MatteoAdamo82/loom/internal/llm"
)

const synthSystem = `You are Loom's answer composer.
Compose a clear, concise answer to the user's question grounded ONLY in the
provided context notes. Cite notes by their id in square brackets, e.g.
[note:42]. If the context doesn't answer the question, say so plainly.

Rules:
- Do not invent facts. If something isn't in the context, don't claim it.
- Keep the answer short: aim for 4–10 sentences unless the question clearly
  needs more.
- End with a one-line "Sources:" list of the ids you actually cited.`

const synthUserTemplate = `Question: %s

Context:
%s

Now write the answer.`

func Synthesize(ctx context.Context, client llm.Client, question string, candidates []Candidate) (string, error) {
	if len(candidates) == 0 {
		return "No relevant notes or sources found.", nil
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
			{Role: llm.RoleSystem, Content: synthSystem},
			{Role: llm.RoleUser, Content: fmt.Sprintf(synthUserTemplate, question, b.String())},
		},
	})
	if err != nil {
		return "", fmt.Errorf("synthesize: %w", err)
	}
	return strings.TrimSpace(resp.Content), nil
}
