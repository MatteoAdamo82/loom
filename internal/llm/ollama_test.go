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

func TestOllamaChatSuccess(t *testing.T) {
	var captured ollamaChatRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %q", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode captured: %v", err)
		}
		resp := ollamaChatResponse{
			Model:           "llama3.1:8b",
			Message:         Message{Role: RoleAssistant, Content: `{"ok": true}`},
			Done:            true,
			PromptEvalCount: 42,
			EvalCount:       17,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewOllama(OllamaConfig{Endpoint: srv.URL, Model: "llama3.1:8b"})
	out, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{
			{Role: RoleSystem, Content: "you are helpful"},
			{Role: RoleUser, Content: "ping"},
		},
		JSON:        true,
		Temperature: 0.2,
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}

	if out.Content != `{"ok": true}` {
		t.Errorf("content = %q", out.Content)
	}
	if out.PromptTokens != 42 || out.OutputTokens != 17 {
		t.Errorf("token counts = %d / %d", out.PromptTokens, out.OutputTokens)
	}
	if captured.Format != "json" {
		t.Errorf("JSON flag not forwarded: format = %q", captured.Format)
	}
	if captured.Options == nil || captured.Options.Temperature == nil || *captured.Options.Temperature != 0.2 {
		t.Errorf("temperature not forwarded: %+v", captured.Options)
	}
	if captured.Stream {
		t.Error("stream should be false for non-streaming chat")
	}
}

func TestOllamaChatErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewOllama(OllamaConfig{Endpoint: srv.URL, Model: "nope"})
	_, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	})
	if err == nil {
		t.Fatal("expected error on 404 status")
	}
	if !strings.Contains(err.Error(), "model not found") {
		t.Errorf("error missing body: %v", err)
	}
}

func TestOllamaTrimsTrailingSlash(t *testing.T) {
	c := NewOllama(OllamaConfig{Endpoint: "http://localhost:11434/", Model: "m"})
	if !strings.HasSuffix(c.cfg.Endpoint, "11434") {
		t.Errorf("expected trailing slash trimmed, got %q", c.cfg.Endpoint)
	}
}

func TestOllamaDefaultEndpoint(t *testing.T) {
	c := NewOllama(OllamaConfig{Model: "m"})
	if c.cfg.Endpoint != "http://localhost:11434" {
		t.Errorf("default endpoint = %q", c.cfg.Endpoint)
	}
}

func TestOllamaStreamEmitsChunksAndAggregates(t *testing.T) {
	frames := []string{
		`{"model":"llama3.1:8b","message":{"role":"assistant","content":"Hello "},"done":false}`,
		`{"model":"llama3.1:8b","message":{"role":"assistant","content":"world"},"done":false}`,
		`{"model":"llama3.1:8b","message":{"role":"assistant","content":"!"},"done":true,"prompt_eval_count":7,"eval_count":3}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, _ := w.(http.Flusher)
		for _, f := range frames {
			_, _ = w.Write([]byte(f + "\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer srv.Close()

	c := NewOllama(OllamaConfig{Endpoint: srv.URL, Model: "llama3.1:8b"})

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
		t.Errorf("chunk concat = %q, want %q", got, "Hello world!")
	}
	if resp.Content != "Hello world!" {
		t.Errorf("response content = %q", resp.Content)
	}
	if resp.PromptTokens != 7 || resp.OutputTokens != 3 {
		t.Errorf("token counts = %d / %d", resp.PromptTokens, resp.OutputTokens)
	}
	if len(chunks) != 3 {
		t.Errorf("expected 3 separate chunks, got %d: %v", len(chunks), chunks)
	}
}

func TestOllamaStreamErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewOllama(OllamaConfig{Endpoint: srv.URL, Model: "m"})
	_, err := c.Stream(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 in error, got %v", err)
	}
}
