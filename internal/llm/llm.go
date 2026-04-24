package llm

import "context"

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model       string    `json:"-"`
	Messages    []Message `json:"-"`
	JSON        bool      `json:"-"` // request JSON-formatted output when supported
	Temperature float64   `json:"-"`
	MaxTokens   int       `json:"-"`
}

type ChatResponse struct {
	Content      string
	Model        string
	PromptTokens int
	OutputTokens int
}

type Client interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	Name() string
}
