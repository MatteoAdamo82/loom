// Package config loads Loom's user configuration from ~/.loom/config.toml.
// Five fields cover everything: where the notes folder lives, which LLM to
// use (provider/model/endpoint), and an env var name holding the API key for
// cloud providers.
package config

import (
	"errors"
	"fmt"
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

// Save writes cfg to path as TOML, creating the parent directory if needed.
func Save(cfg *Config, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("ensure config dir: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create config: %w", err)
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(cfg)
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
