// Package config loads Loom's user configuration from ~/.loom/config.toml.
// Five fields cover everything: where the notes folder lives, which LLM to
// use (provider/model/endpoint), and an env var name holding the API key for
// cloud providers.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	NotesDir string    `toml:"notes_dir"`
	DBPath   string    `toml:"db_path"`
	LLM      LLMConfig `toml:"llm"`

	loadedFrom string
}

type LLMConfig struct {
	Provider  string `toml:"provider"`    // "ollama" | "openai" | "anthropic"
	Model     string `toml:"model"`
	Endpoint  string `toml:"endpoint"`
	APIKeyEnv string `toml:"api_key_env"` // env var name holding the key
}

func Default() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		NotesDir: filepath.Join(home, "loom"),
		DBPath:   filepath.Join(home, ".loom", "index.db"),
		LLM: LLMConfig{
			Provider: "ollama",
			Model:    "llama3.1:8b",
			Endpoint: "http://localhost:11434",
		},
	}
}

// DefaultPath returns the conventional location for the config file.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".loom", "config.toml")
}

// Save writes cfg to path as a commented TOML template that shows all
// supported LLM providers as ready-to-use copy-paste examples.
func Save(cfg *Config, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("ensure config dir: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create config: %w", err)
	}
	defer f.Close()
	return writeTemplate(cfg, f)
}

// writeTemplate writes an annotated config to w. The provider block matching
// cfg.LLM.Provider is left uncommented; the other two are shown as examples.
func writeTemplate(cfg *Config, w io.Writer) error {
	type providerBlock struct {
		name    string
		label   string
		lines   []string
		example []string // fallback example when not active
	}

	// Determine active values, falling back to defaults for each provider.
	active := cfg.LLM.Provider
	if active == "" {
		active = "ollama"
	}

	// comment prefixes each line with "# ".
	comment := func(lines []string) []string {
		out := make([]string, len(lines))
		for i, l := range lines {
			if l == "" {
				out[i] = "#"
			} else {
				out[i] = "# " + l
			}
		}
		return out
	}

	// active block lines (use cfg values when provider matches).
	ollamaLines := func() []string {
		model := cfg.LLM.Model
		if model == "" || cfg.LLM.Provider != "ollama" {
			model = "llama3.1:8b"
		}
		ep := cfg.LLM.Endpoint
		if ep == "" || cfg.LLM.Provider != "ollama" {
			ep = "http://localhost:11434"
		}
		return []string{
			`[llm]`,
			`provider    = "ollama"`,
			`model       = "` + model + `"`,
			`endpoint    = "` + ep + `"`,
			`api_key_env = ""`,
		}
	}
	anthropicLines := func() []string {
		model := cfg.LLM.Model
		if model == "" || cfg.LLM.Provider != "anthropic" {
			model = "claude-sonnet-4-5"
		}
		keyEnv := cfg.LLM.APIKeyEnv
		if keyEnv == "" || cfg.LLM.Provider != "anthropic" {
			keyEnv = "ANTHROPIC_API_KEY"
		}
		return []string{
			`[llm]`,
			`provider    = "anthropic"`,
			`model       = "` + model + `"`,
			`api_key_env = "` + keyEnv + `"`,
		}
	}
	openaiLines := func() []string {
		model := cfg.LLM.Model
		if model == "" || cfg.LLM.Provider != "openai" {
			model = "gpt-4o"
		}
		keyEnv := cfg.LLM.APIKeyEnv
		if keyEnv == "" || cfg.LLM.Provider != "openai" {
			keyEnv = "OPENAI_API_KEY"
		}
		return []string{
			`[llm]`,
			`provider    = "openai"`,
			`model       = "` + model + `"`,
			`api_key_env = "` + keyEnv + `"`,
		}
	}

	type block struct {
		label  string
		lines  []string
		active bool
	}
	blocks := []block{
		{"Option 1 — Ollama (local, no API key required)", ollamaLines(), active == "ollama" || active == ""},
		{"Option 2 — Anthropic Claude", anthropicLines(), active == "anthropic"},
		{"Option 3 — OpenAI", openaiLines(), active == "openai"},
	}

	_, err := fmt.Fprintf(w, `# Loom configuration — edit this file to change provider, model, or folder paths.
# Run "loom scan" after any change.

notes_dir = "%s"
db_path   = "%s"

# ── LLM provider ─────────────────────────────────────────────────────────────
# Uncomment the block for the provider you want to use.
# Only one [llm] block should be active at a time.
#
# api_key_env is the NAME of an environment variable holding your API key.
# The key itself is never written to disk.
#   export ANTHROPIC_API_KEY=sk-ant-...
#   export OPENAI_API_KEY=sk-...

`, cfg.NotesDir, cfg.DBPath)
	if err != nil {
		return err
	}

	for _, b := range blocks {
		if b.active {
			fmt.Fprintf(w, "# %s\n", b.label)
			for _, l := range b.lines {
				fmt.Fprintln(w, l)
			}
		} else {
			fmt.Fprintf(w, "# %s\n", b.label)
			for _, l := range comment(b.lines) {
				fmt.Fprintln(w, l)
			}
		}
		fmt.Fprintln(w)
	}
	return nil
}

// Load reads config from path, merging values over Default(). A missing file
// is not an error — defaults are returned and the path is remembered.
func Load(path string) (*Config, error) {
	cfg := Default()
	cfg.loadedFrom = path

	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg.expand()
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	var scratch Config
	if _, err := toml.Decode(string(b), &scratch); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.merge(&scratch)
	cfg.expand()
	return cfg, nil
}

func (c *Config) merge(other *Config) {
	if other.NotesDir != "" {
		c.NotesDir = other.NotesDir
	}
	if other.DBPath != "" {
		c.DBPath = other.DBPath
	}
	if other.LLM.Provider != "" {
		c.LLM.Provider = other.LLM.Provider
	}
	if other.LLM.Model != "" {
		c.LLM.Model = other.LLM.Model
	}
	if other.LLM.Endpoint != "" {
		c.LLM.Endpoint = other.LLM.Endpoint
	}
	if other.LLM.APIKeyEnv != "" {
		c.LLM.APIKeyEnv = other.LLM.APIKeyEnv
	}
}

func (c *Config) expand() {
	c.NotesDir = expandPath(c.NotesDir)
	c.DBPath = expandPath(c.DBPath)
}

// APIKey resolves the key named by LLMConfig.APIKeyEnv from the environment.
func (l LLMConfig) APIKey() string {
	if l.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(l.APIKeyEnv)
}

// LoadedFrom reports the path the config was loaded from.
func (c *Config) LoadedFrom() string { return c.loadedFrom }

func expandPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	return os.ExpandEnv(p)
}
