# Loom

> **LLM memory that scales — without embeddings.**

Loom is a single-binary knowledge base for LLM agents. It combines the ease
of use of [Karpathy's LLM Wiki pattern](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f)
with the speed and precision of traditional RAG, using only **SQLite + FTS5
(BM25) + an LLM** — no vector databases, no embedding models, no ML libraries.

[![CI](https://github.com/MatteoAdamo82/loom/actions/workflows/ci.yml/badge.svg)](https://github.com/MatteoAdamo82/loom/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/MatteoAdamo82/loom?sort=semver)](https://github.com/MatteoAdamo82/loom/releases/latest)
[![Homebrew tap](https://img.shields.io/badge/homebrew-MatteoAdamo82%2Floom-orange)](https://github.com/MatteoAdamo82/homebrew-loom)
[![Go report](https://goreportcard.com/badge/github.com/MatteoAdamo82/loom)](https://goreportcard.com/report/github.com/MatteoAdamo82/loom)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

---

## Why Loom

Two existing approaches sit on opposite ends of a spectrum:

| | RAG (vector DB) | Karpathy's LLM Wiki |
|---|---|---|
| Setup complexity | High (vector DB, embedding model, infra) | None (Markdown + LLM) |
| Scales to | Millions of docs | ~100 notes |
| Search quality | Strong semantic recall | Limited by `grep` / `index.md` |
| Provenance | Lost in chunks | First-class via wikilinks |
| Runtime deps | Python ML libs | LLM only |

Loom takes the **simplicity** of the LLM Wiki — no embeddings, single file,
LLM-as-gardener — and pushes the scale ceiling by replacing Markdown +
`grep` with **SQLite FTS5 (BM25)**. Where a RAG stack would compute vector
embeddings, Loom asks the LLM to do the semantic work: expand queries,
rerank BM25 results, and generate concept fingerprints at ingest time.

Trade-off: more LLM calls per operation, zero ML infrastructure to manage.
Acceptable when Ollama is free and cloud API tokens cost cents per million.

## What you get

Three binaries built from the same Go module:

- **`loom`** — CLI for ingest, query, lint, note browsing.
- **`loom-mcp`** — Model Context Protocol stdio server. Plug Loom into
  Claude Code, Claude Desktop, or any MCP client as a memory backend.
- **`Loom.app`** *(macOS)* — desktop GUI with note list, chat panel, lint
  view. Ships as a 14 MB universal app.

## How it works

```
┌─ ingest ────────────────────────────────────────────────────────────┐
│                                                                    │
│  file ─► extract ─► dedup ─► LLM analyze ─┐                        │
│                                            │                        │
│                                            ▼  (tx)                  │
│                          source + chunks + summary note +           │
│                          entity stubs + wikilinks  ◄── FTS5 index   │
│                                                                    │
└────────────────────────────────────────────────────────────────────┘
┌─ query ─────────────────────────────────────────────────────────────┐
│                                                                    │
│  question ─► LLM expand ─► BM25 (RRF merge) ─► graph boost ──┐     │
│                                                                ▼   │
│                              hydrate full content ◄── LLM rerank   │
│                                       │                            │
│                                       ▼                            │
│                              LLM synthesize  ─► answer + citations │
│                                                                    │
└────────────────────────────────────────────────────────────────────┘
```

All state lives in a single `loom.db` SQLite file. The LLM layer is
pluggable — Ollama (local), OpenAI, or Anthropic via API.

## Install

### Homebrew (macOS, Linux)

```bash
brew install MatteoAdamo82/loom/loom
```

Installs both `loom` and `loom-mcp`. The Wails desktop GUI (`Loom.app`) is
uploaded as a separate `.zip` on each release page.

### Pre-built binaries

```bash
# macOS (Apple Silicon)
curl -L https://github.com/MatteoAdamo82/loom/releases/latest/download/loom_*_macos_arm64.tar.gz | tar xz
sudo mv loom loom-mcp /usr/local/bin/

# Linux (x86_64)
curl -L https://github.com/MatteoAdamo82/loom/releases/latest/download/loom_*_linux_x86_64.tar.gz | tar xz
sudo mv loom loom-mcp /usr/local/bin/
```

### From source

Requires Go 1.26+ and (for the GUI) Node 20+.

```bash
git clone https://github.com/MatteoAdamo82/loom.git
cd loom
go install ./cmd/loom ./cmd/loom-mcp

# GUI (optional)
go install github.com/wailsapp/wails/v2/cmd/wails@latest
cd cmd/loom-gui && wails build
```

## Quick start

```bash
# 1. Initialise config + DB
loom init

# 2. Tell Loom which LLM to use (default: Ollama / llama3.1:8b)
$EDITOR ~/.loom/config.toml

# 3. Ingest a few documents
loom ingest paper.pdf article.md https://example.com/page.html

# 4. Ask
loom query "what does paper.pdf say about scaling laws?"
loom query --format=marp "summarise the LLM Wiki pattern" > deck.md   # Marp slide deck
loom query --format=text "who proposed the Memex?" | tee answer.txt   # plain prose

# 5. Inspect
loom notes                        # list all notes
loom note <slug>                  # show one note with backlinks
loom lint                         # find orphans, duplicates, gaps

# 6. Niceties
loom config show                  # print effective TOML
loom config edit                  # open ~/.loom/config.toml in $EDITOR
loom completion zsh > _loom       # shell completion (bash/zsh/fish/powershell)
```

## Optional system tools

Loom is a single Go binary with no required external dependencies, but it
picks up two CLI tools when present to handle scanned PDFs:

```bash
# macOS
brew install poppler tesseract                      # base OCR
brew install tesseract-lang                         # all language packs

# Debian/Ubuntu
sudo apt install poppler-utils tesseract-ocr tesseract-ocr-ita
```

When both `pdftoppm` and `tesseract` are on `PATH`, Loom automatically falls
back to OCR for image-only PDF pages, composes a Markdown view with `## Page N`
headers, and caches the result under `~/.loom/cache/pdf/<sha256>.md` so
re-ingesting the same file is instant. Configure language(s) and behaviour in
`config.toml`:

```toml
[extract.pdf]
ocr           = "auto"        # "off" | "auto" (default) | "always"
ocr_languages = "eng+ita"     # tesseract `-l` value
ocr_dpi       = 300
cache_dir     = "~/.loom/cache/pdf"
```

To force a re-extraction after improving OCR settings, just delete the cache
file (`rm ~/.loom/cache/pdf/<hash>.md`) and re-run `loom ingest`.

## Configuration

`~/.loom/config.toml` (created by `loom init`):

```toml
[storage]
db_path = "~/.loom/loom.db"

[llm]
provider    = "ollama"            # "ollama" | "openai" | "anthropic"
model       = "llama3.1:8b"
endpoint    = "http://localhost:11434"
api_key_env = ""                  # e.g. "OPENAI_API_KEY"

[ingest]
chunk_tokens   = 500
chunk_overlap  = 50
max_concurrent = 2
max_analyze    = 12000

[query]
bm25_top_k       = 30
graph_expand_hop = 1
rerank_top_k     = 8
```

To use a cloud provider, point `api_key_env` at an environment variable
holding the key:

```toml
[llm]
provider    = "anthropic"
model       = "claude-sonnet-4-6"
api_key_env = "ANTHROPIC_API_KEY"
```

## Use Loom as memory for Claude Code

Add to `~/.claude/settings.json` (or your project's `.claude/settings.json`):

```json
{
  "mcpServers": {
    "loom": {
      "command": "loom-mcp",
      "args": ["--config", "/Users/you/.loom/config.toml"]
    }
  }
}
```

Then ask Claude things like *"check loom for what we discussed about
postgres tuning"* — it'll call `loom.query` and ground its answer in your
notes.

Tools exposed by the MCP server:

| Tool | Purpose |
|---|---|
| `loom.ingest(path)` | Add a file (txt, md, pdf, html) to the KB |
| `loom.query(question, top_k?)` | Hybrid retrieval + synthesized answer |
| `loom.search(query, limit?)` | Raw BM25 hits (no LLM expansion/rerank) |
| `loom.get_note(slug)` | Fetch one note with its links |
| `loom.list_notes(kind?, limit?)` | Browse notes |
| `loom.lint(min_overlap?)` | Hygiene report |

## Repository layout

```
cmd/
  loom/        CLI (cobra)
  loom-mcp/    MCP stdio server (mark3labs/mcp-go)
  loom-gui/    Desktop app (Wails + Svelte/TS)
internal/
  storage/     SQLite schema + repository (modernc.org/sqlite, no CGo)
  ingest/      Extract → chunk → analyze → tx-write pipeline
  query/       LLM expand → BM25 → graph boost → rerank → synthesize
  llm/         Ollama / OpenAI / Anthropic adapters
  extract/     Text, PDF (ledongthuc/pdf), HTML (go-readability)
  lint/        Orphans, near-duplicates, source gaps
  config/      TOML loader
go.work        Workspace binding the GUI sub-module
```

## Development

```bash
go test ./...                    # all tests
go test -race ./...              # with race detector
go vet ./...

# Run a smoke test against a real Ollama
echo '[storage]
db_path = "/tmp/loom-test.db"
[llm]
provider = "ollama"
model    = "qwen3.5:9b"' > /tmp/loom-test.toml

go run ./cmd/loom --config /tmp/loom-test.toml init
go run ./cmd/loom --config /tmp/loom-test.toml ingest some-doc.md
go run ./cmd/loom --config /tmp/loom-test.toml query "..."
```

GUI development with hot reload:

```bash
cd cmd/loom-gui && wails dev
```

## Status

**v0.3** — three working surfaces (CLI, MCP, GUI), 8 internal packages,
~40 unit tests. Production usage at your own risk; the public Go API is
not yet stable (everything lives under `internal/`).

## License

MIT — see [LICENSE](LICENSE).
