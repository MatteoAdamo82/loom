package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIChatSuccess(t *testing.T) {
	var captured openAIChatRequest
	var authHeader string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		authHeader = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode captured: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "gpt-4o-mini",
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": `{"ok":true}`}},
			},
			"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 4},
		})
	}))
	defer srv.Close()

	c := NewOpenAI(OpenAIConfig{Endpoint: srv.URL, Model: "gpt-4o-mini", APIKey: "sk-test"})
	out, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{
			{Role: RoleSystem, Content: "you are helpful"},
			{Role: RoleUser, Content: "ping"},
		},
		JSON:        true,
		Temperature: 0.3,
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if out.Content != `{"ok":true}` {
		t.Errorf("content = %q", out.Content)
	}
	if out.PromptTokens != 10 || out.OutputTokens != 4 {
		t.Errorf("token counts = %d / %d", out.PromptTokens, out.OutputTokens)
	}
	if authHeader != "Bearer sk-test" {
		t.Errorf("auth header = %q", authHeader)
	}
	if captured.ResponseFormat == nil || captured.ResponseFormat.Type != "json_object" {
		t.Errorf("JSON flag not forwarded: %+v", captured.ResponseFormat)
	}
	if captured.Temperature == nil || *captured.Temperature != 0.3 {
		t.Errorf("temperature not forwarded: %+v", captured.Temperature)
	}
	if len(captured.Messages) != 2 {
		t.Errorf("messages count = %d", len(captured.Messages))
	}
}

func TestOpenAIChatErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"bad model","type":"invalid_request_error"}}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	c := NewOpenAI(OpenAIConfig{Endpoint: srv.URL, Model: "x"})
	_, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	})
	if err == nil {
		t.Fatal("expected error on 400 status")
	}
	if !strings.Contains(err.Error(), "bad model") {
		t.Errorf("error missing body: %v", err)
	}
}

func TestOpenAIChatNoChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{}})
	}))
	defer srv.Close()
	c := NewOpenAI(OpenAIConfig{Endpoint: srv.URL, Model: "x"})
	_, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	})
	if err == nil || !strings.Contains(err.Error(), "no choices") {
		t.Errorf("expected no-choices error, got %v", err)
	}
}

func TestOpenAIDefaultEndpoint(t *testing.T) {
	c := NewOpenAI(OpenAIConfig{Model: "m"})
	if c.cfg.Endpoint != "https://api.openai.com" {
		t.Errorf("default endpoint = %q", c.cfg.Endpoint)
	}
}

func TestOpenAIStreamEmitsDeltasAndAggregates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = w.Write([]byte(s))
			if flusher != nil {
				flusher.Flush()
			}
		}
		write(`data: {"model":"gpt-4o-mini","choices":[{"delta":{"role":"assistant","content":"Hello "}}]}` + "\n\n")
		write(`data: {"model":"gpt-4o-mini","choices":[{"delta":{"content":"world"}}]}` + "\n\n")
		write(`data: {"model":"gpt-4o-mini","choices":[{"delta":{"content":"!"}}]}` + "\n\n")
		write(`data: {"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":4}}` + "\n\n")
		write("data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := NewOpenAI(OpenAIConfig{Endpoint: srv.URL, Model: "gpt-4o-mini", APIKey: "sk"})

	var chunks []string
	resp, err := c.Stream(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(s string) {
		chunks = append(chunks, s)
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if got := strings.Join(chunks, ""); got != "Hello world!" {
		t.Errorf("chunk concat = %q", got)
	}
	if resp.Content != "Hello world!" {
		t.Errorf("response content = %q", resp.Content)
	}
	if resp.PromptTokens != 11 || resp.OutputTokens != 4 {
		t.Errorf("token counts = %d / %d", resp.PromptTokens, resp.OutputTokens)
	}
	if len(chunks) != 3 {
		t.Errorf("expected 3 deltas (excl. usage frame), got %d", len(chunks))
	}
}
