// Package config loads Loom's user configuration from ~/.loom/config.toml.
// Six fields cover everything: where the notes folder(s) live, which LLM to
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
	NotesDirs []string  `toml:"notes_dirs"`
	DBPath    string    `toml:"db_path"`
	LLM       LLMConfig `toml:"llm"`

	loadedFrom string
}

type LLMConfig struct {
	Provider  string `toml:"provider"`    // "ollama" | "openai" | "anthropic"
	Model     string `toml:"model"`
	Endpoint  string `toml:"endpoint"`
	APIKeyEnv string `toml:"api_key_env"` // env var name holding the key
	OCRModel  string `toml:"ocr_model"`   // optional: Ollama vision model for image/PDF OCR
}

func Default() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		NotesDirs: []string{filepath.Join(home, "loom")},
		DBPath:    filepath.Join(home, ".loom", "index.db"),
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
	active := cfg.LLM.Provider
	if active == "" {
		active = "ollama"
	}

	// pick returns val when it is non-empty and the active provider matches p;
	// otherwise it returns def. This selects the user's real value for the
	// active block and a sensible default for the commented-out examples.
	pick := func(p, val, def string) string {
		if val != "" && active == p {
			return val
		}
		return def
	}

	// Format notes_dirs as a TOML inline array.
	dirs := cfg.NotesDirs
	if len(dirs) == 0 {
		home, _ := os.UserHomeDir()
		dirs = []string{filepath.Join(home, "loom")}
	}
	var sb strings.Builder
	sb.WriteString("[")
	for i, d := range dirs {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "%q", d)
	}
	sb.WriteString("]")
	notesDirsToml := sb.String()

	type providerBlock struct {
		label  string
		active bool
		lines  []string
	}
	blocks := []providerBlock{
		{
			label:  "Option 1 — Ollama (local, no API key required)",
			active: active == "ollama",
			lines: []string{
				`[llm]`,
				`provider    = "ollama"`,
				fmt.Sprintf(`model       = %q`, pick("ollama", cfg.LLM.Model, "llama3.1:8b")),
				fmt.Sprintf(`endpoint    = %q`, pick("ollama", cfg.LLM.Endpoint, "http://localhost:11434")),
				`api_key_env = ""`,
				`# ocr_model  = "glm-ocr"  # optional: vision model for PDF/image OCR`,
			},
		},
		{
			label:  "Option 2 — Anthropic Claude",
			active: active == "anthropic",
			lines: []string{
				`[llm]`,
				`provider    = "anthropic"`,
				fmt.Sprintf(`model       = %q`, pick("anthropic", cfg.LLM.Model, "claude-sonnet-4-5")),
				fmt.Sprintf(`api_key_env = %q`, pick("anthropic", cfg.LLM.APIKeyEnv, "ANTHROPIC_API_KEY")),
			},
		},
		{
			label:  "Option 3 — OpenAI",
			active: active == "openai",
			lines: []string{
				`[llm]`,
				`provider    = "openai"`,
				fmt.Sprintf(`model       = %q`, pick("openai", cfg.LLM.Model, "gpt-4o")),
				fmt.Sprintf(`api_key_env = %q`, pick("openai", cfg.LLM.APIKeyEnv, "OPENAI_API_KEY")),
			},
		},
	}

	_, err := fmt.Fprintf(w, `# Loom configuration — edit this file to change provider, model, or folder paths.
# Run "loom scan" after any change.

# One or more folders to scan. Add as many paths as you like.
# Example: notes_dirs = ["/home/user/work", "/home/user/personal"]
notes_dirs = %s
db_path    = %q

# ── LLM provider ─────────────────────────────────────────────────────────────
# Uncomment the block for the provider you want to use.
# Only one [llm] block should be active at a time.
#
# api_key_env is the NAME of an environment variable holding your API key.
# The key itself is never written to disk.
#   export ANTHROPIC_API_KEY=sk-ant-...
#   export OPENAI_API_KEY=sk-...

`, notesDirsToml, cfg.DBPath)
	if err != nil {
		return err
	}

	for _, b := range blocks {
		fmt.Fprintf(w, "# %s\n", b.label)
		for _, l := range b.lines {
			if b.active {
				fmt.Fprintln(w, l)
			} else if l == "" {
				fmt.Fprintln(w, "#")
			} else {
				fmt.Fprintln(w, "# "+l)
			}
		}
		fmt.Fprintln(w)
	}
	return nil
}

// rawConfig is used only during TOML decoding. It holds both the new
// notes_dirs array and the legacy notes_dir string so we can migrate
// old config files transparently.
type rawConfig struct {
	NotesDir  string    `toml:"notes_dir"`  // legacy — single path
	NotesDirs []string  `toml:"notes_dirs"` // new — slice of paths
	DBPath    string    `toml:"db_path"`
	LLM       LLMConfig `toml:"llm"`
}

// Load reads config from path, merging values over Default(). A missing file
// is not an error — defaults are returned and the path is remembered.
// Old configs that use notes_dir (singular) are migrated automatically.
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

	var raw rawConfig
	if _, err := toml.Decode(string(b), &raw); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Migrate legacy notes_dir → notes_dirs.
	if len(raw.NotesDirs) == 0 && raw.NotesDir != "" {
		raw.NotesDirs = []string{raw.NotesDir}
	}

	cfg.merge(&raw)
	cfg.expand()
	return cfg, nil
}

func (c *Config) merge(other *rawConfig) {
	if len(other.NotesDirs) > 0 {
		c.NotesDirs = other.NotesDirs
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
	for i, d := range c.NotesDirs {
		c.NotesDirs[i] = expandPath(d)
	}
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
