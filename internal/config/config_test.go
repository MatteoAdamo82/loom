package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if cfg.LLM.Provider != "ollama" {
		t.Errorf("default provider = %q", cfg.LLM.Provider)
	}
	if !strings.HasSuffix(cfg.Storage.DBPath, "loom.db") {
		t.Errorf("default db path = %q", cfg.Storage.DBPath)
	}
}

func TestLoadMergesAndExpands(t *testing.T) {
	dir := t.TempDir()
	body := `
[storage]
db_path = "~/loom-override.db"

[llm]
provider = "anthropic"
model    = "claude-sonnet-4-6"
api_key_env = "TEST_LOOM_KEY"

[query]
rerank_top_k = 5
`
	path := filepath.Join(dir, "c.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_LOOM_KEY", "secret-abc")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.LLM.Provider != "anthropic" {
		t.Errorf("provider = %q", cfg.LLM.Provider)
	}
	if cfg.LLM.Model != "claude-sonnet-4-6" {
		t.Errorf("model = %q", cfg.LLM.Model)
	}
	if cfg.LLM.APIKey() != "secret-abc" {
		t.Errorf("api key resolution failed: %q", cfg.LLM.APIKey())
	}
	if cfg.Query.RerankTopK != 5 {
		t.Errorf("rerank top_k = %d", cfg.Query.RerankTopK)
	}
	// defaults still present for unspecified keys
	if cfg.Ingest.ChunkTokens != 500 {
		t.Errorf("chunk tokens overwritten: %d", cfg.Ingest.ChunkTokens)
	}
	if strings.HasPrefix(cfg.Storage.DBPath, "~/") {
		t.Errorf("~ should be expanded: %q", cfg.Storage.DBPath)
	}
	if cfg.LoadedFrom() != path {
		t.Errorf("loadedFrom = %q", cfg.LoadedFrom())
	}
}
