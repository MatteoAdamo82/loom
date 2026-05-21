package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultReturnsExpectedFields(t *testing.T) {
	cfg := Default()
	if cfg.LLM.Provider != "ollama" {
		t.Errorf("expected default provider 'ollama', got %q", cfg.LLM.Provider)
	}
	if cfg.LLM.Model == "" {
		t.Error("expected non-empty default model")
	}
	if cfg.LLM.Endpoint == "" {
		t.Error("expected non-empty default endpoint")
	}
	if cfg.NotesDir == "" || cfg.DBPath == "" {
		t.Error("expected non-empty default paths")
	}
}

func TestSaveWritesAllThreeProviderBlocks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	if err := Save(Default(), path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(b)

	for _, want := range []string{
		`provider    = "ollama"`,
		`provider    = "anthropic"`,
		`provider    = "openai"`,
		"api_key_env",
		"ANTHROPIC_API_KEY",
		"OPENAI_API_KEY",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("generated config missing %q", want)
		}
	}
}

func TestLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	orig := Default()
	orig.NotesDir = filepath.Join(dir, "notes")
	orig.DBPath = filepath.Join(dir, "index.db")
	orig.LLM.Provider = "openai"
	orig.LLM.Model = "gpt-4o"
	orig.LLM.APIKeyEnv = "OPENAI_API_KEY"

	if err := Save(orig, path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.LLM.Provider != "openai" {
		t.Errorf("provider: want 'openai', got %q", loaded.LLM.Provider)
	}
	if loaded.LLM.Model != "gpt-4o" {
		t.Errorf("model: want 'gpt-4o', got %q", loaded.LLM.Model)
	}
	if loaded.LLM.APIKeyEnv != "OPENAI_API_KEY" {
		t.Errorf("api_key_env: want 'OPENAI_API_KEY', got %q", loaded.LLM.APIKeyEnv)
	}
	if loaded.NotesDir != orig.NotesDir {
		t.Errorf("notes_dir: want %q, got %q", orig.NotesDir, loaded.NotesDir)
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	cfg, err := Load("/nonexistent/path/config.toml")
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if cfg.LLM.Provider != "ollama" {
		t.Errorf("expected default provider, got %q", cfg.LLM.Provider)
	}
}

func TestAPIKeyReadsFromEnv(t *testing.T) {
	t.Setenv("TEST_LLM_KEY", "secret-value")
	lc := LLMConfig{APIKeyEnv: "TEST_LLM_KEY"}
	if got := lc.APIKey(); got != "secret-value" {
		t.Errorf("APIKey() = %q, want 'secret-value'", got)
	}
}

func TestAPIKeyEmptyWhenEnvNotSet(t *testing.T) {
	lc := LLMConfig{APIKeyEnv: "LOOM_NONEXISTENT_KEY_XYZ"}
	if got := lc.APIKey(); got != "" {
		t.Errorf("APIKey() = %q, want empty", got)
	}
}

func TestLoadedFromReportsPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := Save(Default(), path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LoadedFrom() != path {
		t.Errorf("LoadedFrom() = %q, want %q", cfg.LoadedFrom(), path)
	}
}
