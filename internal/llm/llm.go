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

// Streamer is an optional capability for clients that support token-level
// streaming. Callers should type-assert to detect support and fall back to
// Chat() when a client doesn't implement it.
//
// Implementations MUST invoke onChunk with successive content deltas (not
// cumulative buffers) and MUST also return the full assembled message in the
// returned ChatResponse so callers don't have to accumulate themselves.
type Streamer interface {
	Client
	Stream(ctx context.Context, req ChatRequest, onChunk func(string)) (*ChatResponse, error)
}
