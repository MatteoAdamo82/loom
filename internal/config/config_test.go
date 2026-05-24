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
	if len(cfg.NotesDirs) == 0 || cfg.DBPath == "" {
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

func TestSaveWritesNotesDirsArray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg := Default()
	cfg.NotesDirs = []string{"/tmp/a", "/tmp/b"}

	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b, _ := os.ReadFile(path)
	content := string(b)

	if !strings.Contains(content, `notes_dirs`) {
		t.Error("expected notes_dirs key in generated config")
	}
	if !strings.Contains(content, `/tmp/a`) || !strings.Contains(content, `/tmp/b`) {
		t.Errorf("both paths should appear in config: %s", content)
	}
}

func TestLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	orig := Default()
	orig.NotesDirs = []string{filepath.Join(dir, "notes")}
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
	if len(loaded.NotesDirs) != 1 || loaded.NotesDirs[0] != orig.NotesDirs[0] {
		t.Errorf("notes_dirs: want %v, got %v", orig.NotesDirs, loaded.NotesDirs)
	}
}

func TestLoadLegacyNotesDirMigratedToSlice(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	// Write a legacy config that uses the old notes_dir (singular) key.
	legacy := `notes_dir = "/tmp/legacy"
db_path   = "/tmp/index.db"
[llm]
provider = "ollama"
model    = "llama3.1:8b"
endpoint = "http://localhost:11434"
`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.NotesDirs) != 1 || cfg.NotesDirs[0] != "/tmp/legacy" {
		t.Errorf("legacy notes_dir not migrated: got %v", cfg.NotesDirs)
	}
}

func TestLoadMultipleDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	content := `notes_dirs = ["/tmp/work", "/tmp/personal"]
db_path = "/tmp/index.db"
[llm]
provider = "ollama"
model    = "llama3.1:8b"
endpoint = "http://localhost:11434"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.NotesDirs) != 2 {
		t.Fatalf("expected 2 dirs, got %v", cfg.NotesDirs)
	}
	if cfg.NotesDirs[0] != "/tmp/work" || cfg.NotesDirs[1] != "/tmp/personal" {
		t.Errorf("unexpected dirs: %v", cfg.NotesDirs)
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
