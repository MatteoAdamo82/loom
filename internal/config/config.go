// Package config loads and resolves Loom's user-facing configuration.
// Config lives in TOML; paths and secret references support environment
// variable expansion via `${VAR}`.
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
	Storage StorageConfig `toml:"storage"`
	LLM     LLMConfig     `toml:"llm"`
	Rerank  LLMConfig     `toml:"rerank"`
	Ingest  IngestConfig  `toml:"ingest"`
	Query   QueryConfig   `toml:"query"`

	loadedFrom string
}

type StorageConfig struct {
	DBPath string `toml:"db_path"`
}

type LLMConfig struct {
	Provider   string `toml:"provider"`    // "ollama" | "openai" | "anthropic"
	Model      string `toml:"model"`
	Endpoint   string `toml:"endpoint"`
	APIKeyEnv  string `toml:"api_key_env"` // env var name holding the key
}

type IngestConfig struct {
	ChunkTokens   int `toml:"chunk_tokens"`
	ChunkOverlap  int `toml:"chunk_overlap"`
	MaxConcurrent int `toml:"max_concurrent"`
	MaxAnalyze    int `toml:"max_analyze"`
}

type QueryConfig struct {
	BM25TopK       int `toml:"bm25_top_k"`
	GraphExpandHop int `toml:"graph_expand_hop"`
	RerankTopK     int `toml:"rerank_top_k"`
}

func Default() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		Storage: StorageConfig{
			DBPath: filepath.Join(home, ".loom", "loom.db"),
		},
		LLM: LLMConfig{
			Provider: "ollama",
			Model:    "llama3.1:8b",
			Endpoint: "http://localhost:11434",
		},
		Rerank: LLMConfig{
			Provider: "ollama",
			Model:    "llama3.1:8b",
			Endpoint: "http://localhost:11434",
		},
		Ingest: IngestConfig{
			ChunkTokens:   500,
			ChunkOverlap:  50,
			MaxConcurrent: 2,
			MaxAnalyze:    12000,
		},
		Query: QueryConfig{
			BM25TopK:       30,
			GraphExpandHop: 1,
			RerankTopK:     8,
		},
	}
}

// DefaultPath returns the conventional location for the config file.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".loom", "config.toml")
}

// Load reads config from path, merging values over Default(). A missing file
// is not an error — defaults are returned and the path is remembered so a
// later Save() can create it.
func Load(path string) (*Config, error) {
	cfg := Default()
	cfg.loadedFrom = path

	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	// BurntSushi/toml mutates the zero-valued fields we passed in, so we need
	// a two-step merge: decode into a scratch struct, then overlay on cfg.
	var scratch Config
	if _, err := toml.Decode(string(b), &scratch); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.merge(&scratch)
	cfg.expand()
	return cfg, nil
}

func (c *Config) merge(other *Config) {
	if other.Storage.DBPath != "" {
		c.Storage.DBPath = other.Storage.DBPath
	}
	mergeLLM(&c.LLM, other.LLM)
	mergeLLM(&c.Rerank, other.Rerank)
	if other.Ingest.ChunkTokens > 0 {
		c.Ingest.ChunkTokens = other.Ingest.ChunkTokens
	}
	if other.Ingest.ChunkOverlap > 0 {
		c.Ingest.ChunkOverlap = other.Ingest.ChunkOverlap
	}
	if other.Ingest.MaxConcurrent > 0 {
		c.Ingest.MaxConcurrent = other.Ingest.MaxConcurrent
	}
	if other.Ingest.MaxAnalyze > 0 {
		c.Ingest.MaxAnalyze = other.Ingest.MaxAnalyze
	}
	if other.Query.BM25TopK > 0 {
		c.Query.BM25TopK = other.Query.BM25TopK
	}
	if other.Query.GraphExpandHop > 0 {
		c.Query.GraphExpandHop = other.Query.GraphExpandHop
	}
	if other.Query.RerankTopK > 0 {
		c.Query.RerankTopK = other.Query.RerankTopK
	}
}

func mergeLLM(dst *LLMConfig, src LLMConfig) {
	if src.Provider != "" {
		dst.Provider = src.Provider
	}
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.Endpoint != "" {
		dst.Endpoint = src.Endpoint
	}
	if src.APIKeyEnv != "" {
		dst.APIKeyEnv = src.APIKeyEnv
	}
}

func (c *Config) expand() {
	c.Storage.DBPath = expandPath(c.Storage.DBPath)
}

// APIKey resolves the key named by LLMConfig.APIKeyEnv from the environment.
// Returns "" when the env var is unset or the reference is empty.
func (l LLMConfig) APIKey() string {
	if l.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(l.APIKeyEnv)
}

// LoadedFrom reports the path the config was loaded from (even if the file
// was missing at load time).
func (c *Config) LoadedFrom() string { return c.loadedFrom }

func expandPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	return os.ExpandEnv(p)
}
