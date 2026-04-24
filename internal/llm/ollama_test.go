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
