# Loom

> A folder of files + an LLM as your knowledge base.

Loom is a small desktop app and CLI built around one idea: **the files in a folder are the truth**. You drop markdown, PDF, HTML and text files into a directory of your choice. Loom scans it, asks an LLM to write a summary and a handful of keywords for each file, and stores those next to a full-text index in SQLite. When you ask a question, Loom does ONE LLM call: it picks the most relevant files with BM25 and hands them to the model.

By default: no embeddings, no vector DB, no chunking, no graph, no fancy reranking. Semantic search is available as an **opt-in** (hybrid BM25 + vectors) for those who want it — it stays off unless you enable it, so the default experience is unchanged. See [Hybrid search](#hybrid-search-optional).

This is a deliberate revival of [Andrej Karpathy's "LLM Wiki" pattern](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f), with one concession: a tiny SQLite index so search stays snappy as the folder grows past `grep`-friendly size.

## How it works

```
~/loom/                          ← your folder, your files, your editor
├── ricette.md
├── papers/
│   └── llm-wiki.pdf
├── articolo.html
└── note-libere.md

# or point Loom at folders that already exist on your machine:
# notes_dirs = ["/Users/you/Documents/work", "/Users/you/Obsidian/vault"]

~/.loom/
├── config.toml                  ← provider + model + folder paths
└── index.db                     ← SQLite (regenerable from the folder)
```

**Scan** — for every new or modified file, Loom extracts text (via `ledongthuc/pdf`, `go-readability`, plain read, or a vision OCR model for images), then asks the LLM:
> "Riassumi in 150-250 parole + estrai 5-8 keywords come JSON."

The summary, keywords, content, hash and mtime go into one SQLite row plus an FTS5 mirror.

**Ask** — Loom runs an FTS5 BM25 search over `title + summary + keywords + content`, takes the top 5 hits, and hands their summaries + (truncated) content to the LLM with a system prompt that says "rispondi solo usando queste note, cita con `[rel_path]`". One call. The answer streams back.

That's it.

## Install

**macOS (Homebrew) — easiest:**

```bash
brew tap MatteoAdamo82/loom
brew install loom
```

This installs both `loom` (CLI) and `loom-mcp` (MCP server). The desktop GUI (`Loom.app`) is available as a separate download on the [releases page](https://github.com/MatteoAdamo82/loom/releases).

**From source** — requires Go 1.26+ and (for the GUI) Node 20+:

```bash
git clone https://github.com/MatteoAdamo82/loom.git
cd loom
go install ./cmd/loom ./cmd/loom-mcp

# Optional: HTTP server (for integrating Loom into other apps/services)
go install ./cmd/loom-http

# GUI
go install github.com/wailsapp/wails/v2/cmd/wails@latest
cd cmd/loom-gui && wails build
```

## Quick start

```bash
# 1. Initialise: writes ~/.loom/config.toml and creates ~/loom/.
loom init

# 2. Drop files into ~/loom/. Markdown, PDF, HTML, TXT.

# 3. Scan: extracts text, summarises each new/changed file via the LLM.
loom scan

# 4. Ask:
loom ask "cosa ho scritto sul progetto X?"
```

## Configuration

`~/.loom/config.toml` is created by `loom init` with all three provider options as ready-to-use commented blocks.

| Provider | `provider` | Model example | `api_key_env` |
|---|---|---|---|
| Ollama (local) | `ollama` | `llama3.1:8b` | — |
| Anthropic | `anthropic` | `claude-sonnet-4-5` | `ANTHROPIC_API_KEY` |
| OpenAI | `openai` | `gpt-4o` | `OPENAI_API_KEY` |

> **`api_key_env` is the name of an environment variable**, not the key itself — the key is never written to disk. Set it in your shell before running Loom:
> ```bash
> export ANTHROPIC_API_KEY=sk-ant-...
> ```

To switch provider, open `~/.loom/config.toml`, uncomment the block you want and comment out the others:

```toml
# Ollama (local, default)
[llm]
provider    = "ollama"
model       = "llama3.1:8b"
endpoint    = "http://localhost:11434"
api_key_env = ""

# Anthropic Claude
# [llm]
# provider    = "anthropic"
# model       = "claude-sonnet-4-5"
# api_key_env = "ANTHROPIC_API_KEY"

# OpenAI
# [llm]
# provider    = "openai"
# model       = "gpt-4o"
# api_key_env = "OPENAI_API_KEY"
```

The `endpoint` field is optional for cloud providers (defaults are built in). For Ollama it defaults to `http://localhost:11434`.

## Hybrid search (optional)

By default Loom searches with **BM25 keyword matching** — fast, deterministic, no extra moving parts. That works well when the question shares words with the notes, but it misses paraphrases and cross-language queries (ask in English, notes in Italian).

You can optionally turn on **hybrid search**: Loom stores one embedding vector per file at scan time and, at query time, fuses semantic similarity with BM25 using [Reciprocal Rank Fusion](https://plg.uwaterloo.ca/~gvcormac/cormacksigir09-rrf.pdf). It stays true to Loom's design — the vectors live in the same SQLite file (a small `file_vectors` table), search is pure-Go brute-force cosine (no `sqlite-vec`, no CGO, no vector server), and **it's off unless you enable it**.

Add an `[embeddings]` block to `~/.loom/config.toml`:

```toml
[embeddings]
enabled  = true
provider = "ollama"
model    = "embeddinggemma:300m"   # multilingual, ~700 MB RAM, runs on CPU
endpoint = "http://localhost:11434"
dim      = 768                     # optional; 0 = use the model's native size
# api_key_env = "OPENAI_API_KEY"   # only for provider = "openai"
```

Then re-index so the vectors get built:

```sh
ollama pull embeddinggemma:300m   # one-time, ~620 MB
loom scan --force
```

Notes:

- **Recommended model:** `embeddinggemma:300m` (Ollama) — multilingual (100+ languages, good for cross-language search), CPU-friendly, no GPU required. `provider = "openai"` (or any OpenAI-compatible `/v1/embeddings` endpoint) also works.
- **Changing the embedding model** changes the vector space. Vectors from a different model/dimension are ignored until you run `loom scan --force` to re-embed everything — search degrades gracefully (back to BM25 for those files) rather than breaking.
- **Granularity is per file.** Loom embeds one vector per file (consistent with its one-row-per-file model). For long documents, split them into smaller files so each vector stays focused.
- **Disabling** it is just `enabled = false` (or removing the block). Existing vectors are left in place but ignored; BM25 keeps working.

## Organising your notes

### Single folder

Loom walks the notes folder recursively, so subdirectories work out of the box:

```
~/loom/
├── work/
│   ├── project-x.md
│   └── meetings.md
├── recipes/
│   └── carbonara.md
└── papers/
    └── llm-wiki.pdf
```

Citations in answers reflect the full relative path: `[work/project-x.md]`.

### Multiple folders — no need to move files

You can point Loom at as many folders as you like. No need to copy or move anything:

```toml
notes_dirs = [
  "/Users/you/Documents/work",
  "/Users/you/Obsidian/vault",
  "/Users/you/Desktop/papers",
]
```

When more than one folder is configured, Loom prefixes each relative path with the folder's name to keep them unambiguous:

```
work/meeting-notes.md
vault/zettelkasten.md
papers/llm-wiki.pdf
```

Citations in answers carry this prefix too: `[work/meeting-notes.md]`.

**Backward compatibility** — old configs that use `notes_dir = "..."` (singular) are migrated automatically; no changes needed.

### Moving or renaming files

Loom detects that the content hash matches an existing record and reuses the summary and keywords without an extra LLM call. The old path is removed from the index automatically.

Hidden directories (names starting with `.`, e.g. `.git`, `.obsidian`) are always skipped.

## PDFs and images

### Text-based PDFs

Pure-Go extraction (`ledongthuc/pdf`) — no dependencies, always runs.

### Scanned PDFs and image files (OCR)

Two OCR backends are available, both optional:

**Option A — GLM-OCR via Ollama (recommended)**: a local vision model that outputs structured markdown (preserves headings, tables, lists).

```bash
ollama pull glm-ocr
```

Then add one line to `~/.loom/config.toml`:

```toml
[llm]
provider  = "ollama"
model     = "gemma4:31b"   # your chat model
endpoint  = "http://localhost:11434"
ocr_model = "glm-ocr"      # vision model for OCR — leave empty to disable
```

When `ocr_model` is set, Loom also indexes **image files** directly (PNG, JPG, JPEG, WebP, GIF, TIFF). Drop a screenshot or a scanned page into your notes folder and it will be transcribed and searchable like any other file.

> **Memory note:** Loom caps the OCR context window (`num_ctx`) so the vision
> model stays within a few GB of RAM. Vision OCR is still CPU/GPU-heavy and runs
> on the order of a minute or two per image on a laptop — the first scan of a
> folder with many images can take a while, but subsequent scans skip unchanged
> files via their mtime/hash.

**Option B — Tesseract** (fallback when `ocr_model` is not set):

```bash
# macOS
brew install poppler tesseract tesseract-lang

# Debian/Ubuntu
sudo apt install poppler-utils tesseract-ocr tesseract-ocr-ita
```

Override the OCR language:

```bash
export TESSERACT_LANGS="eng+ita"
```

### Fallback behaviour

If all OCR options fail, the file is still indexed with a placeholder summary; the LLM won't quote it but the user sees it in the list.

## Use Loom as memory for Claude Code / Claude Desktop

`loom-mcp` is a stdio MCP server that exposes the indexed folder to any MCP-aware client. How you connect it depends on the client.

### Claude Code (and other config-file MCP clients)

Add to `~/.claude/settings.json`:

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

### Claude Desktop (the macOS/Windows app)

The current Claude Desktop app does **not** read `claude_desktop_config.json` or `~/.claude/settings.json` — it loads MCP servers as **Desktop Extensions** (`.mcpb` bundles). Install Loom this way:

1. Download `loom-<version>-<os>-<arch>.mcpb` from the [releases page](https://github.com/MatteoAdamo82/loom/releases) (e.g. `darwin-arm64` for Apple Silicon Macs).
2. In the app: **Settings → Extensions → Install Extension…** and pick the `.mcpb` file (or drag it onto the window).
3. When prompted, set **Loom config file** to your `~/.loom/config.toml`, then enable the extension.
4. First launch on macOS may flag the unsigned binary: **System Settings → Privacy & Security → Open Anyway**.

To build a bundle yourself instead of downloading one:

```bash
scripts/build-mcpb.sh 0.4.3 darwin arm64   # → dist/loom-0.4.3-darwin-arm64.mcpb
```

Then ask Claude things like *"check loom for what I wrote about postgres tuning"* — it'll call `loom_ask` and ground its answer in your notes.

Tools exposed:

| Tool | Purpose |
|---|---|
| `loom_ask(question, top_k?)` | One-shot answer with file citations (1 LLM call) |
| `loom_search(query, limit?)` | Raw BM25 hits, no LLM call |
| `loom_scan(force?)` | (Re)index the notes folder |
| `loom_list_files()` | Browse every indexed file with summary + keywords |
| `loom_get_file(rel_path)` | Fetch full extracted content of one file |

## Use Loom over HTTP (optional)

Loom is local-first: the CLI, MCP and GUI all run against a folder on your machine and need no server. But if you want **another application** (a backend, a microservice, anything that isn't MCP-aware) to query your knowledge base, `loom-http` exposes the same index over a tiny REST API. It's entirely optional — Loom works without it.

```bash
LOOM_HTTP_ADDR=:8080 loom-http --config ~/.loom/config.toml
```

| Method & path | Purpose |
|---|---|
| `GET /healthz` | Liveness check |
| `GET /corpora` | List corpora (multi-corpus mode) |
| `POST /search {query, limit?, corpus?}` | Search hits (`rel_path,title,summary,content,rank`) — **no answer generation** (BM25, or hybrid if embeddings are on) |
| `POST /scan {force?, corpus?}` | (Re)index the notes folder (uses the LLM to summarise) |

`/search` is the natural fit for "retrieve, then let *my* model answer": your app gets the most relevant files and composes the reply with its own prompt. Example:

```bash
curl -s localhost:8080/search -d '{"query":"check-in time","limit":3}'
```

### Multi-corpus (multi-tenant) mode

By default one `loom-http` process serves the single corpus in its config file. Set `LOOM_CORPUS_ROOT` to serve **many isolated corpora** from one process — handy for multi-tenant hosts:

```bash
LOOM_CORPUS_ROOT=/srv/knowledge LOOM_HTTP_ADDR=:8080 loom-http
```

Each request then carries a `corpus` name, resolved to `<root>/<corpus>/{notes,index.db}` with a **separate SQLite index per corpus**:

```bash
curl -s localhost:8080/scan   -d '{"corpus":"acme","force":true}'
curl -s localhost:8080/search -d '{"corpus":"acme","query":"refund policy"}'
curl -s localhost:8080/corpora
```

- Corpus names are validated to a single safe path segment (`[A-Za-z0-9_-]`, ≤64 chars), so one corpus can never read another's files.
- The LLM and embeddings providers are **shared** across corpora (same models, same config). Hybrid search applies per corpus once its index has vectors.
- Omitting `corpus` still works and targets the config-file corpus, so existing single-corpus integrations are unchanged.

## GUI

`Loom.app` is a Wails desktop app with two panels: the file list on the left, the chat on the right. Click a file to open a read-only viewer with markdown rendering. Click a citation pill `[file.md]` in an answer to jump to that file. The settings cog opens a single modal — provider, model, endpoint, API-key env var, folder paths. That's all.

```bash
cd cmd/loom-gui && wails dev    # hot-reload during development
cd cmd/loom-gui && wails build  # release binary
```

## Repository layout

```
cmd/
  loom/        CLI: init / scan / ask
  loom-mcp/    MCP stdio server (Claude Code / Claude Desktop / any MCP client)
  loom-http/   Optional REST server: /search, /scan, /healthz (for non-MCP apps)
  loom-gui/    Wails desktop app (Svelte + TS frontend)
internal/
  config/      TOML loader (5 fields)
  extract/     Text, PDF, HTML extractors
  index/       Folder walk + LLM summarise + DB upsert
  llm/         Ollama / OpenAI / Anthropic adapters
  query/       BM25 search → 1 LLM call → answer + citations
  storage/     SQLite (one `files` table + FTS5 mirror)
extension/     Claude Desktop extension manifest (.mcpb template)
scripts/       build-mcpb.sh — package loom-mcp as a .mcpb bundle
```

## Development

```bash
go test ./...                    # all tests
go vet ./...

# Smoke test against a real Ollama:
mkdir -p /tmp/loomtest
echo "# Carbonara\nGuanciale, pecorino, uova." > /tmp/loomtest/ricetta.md
echo 'notes_dirs = ["/tmp/loomtest"]
db_path = "/tmp/loomtest.db"
[llm]
provider = "ollama"
model    = "llama3.1:8b"' > /tmp/loomtest.toml

go run ./cmd/loom --config /tmp/loomtest.toml scan
go run ./cmd/loom --config /tmp/loomtest.toml ask "cosa serve per la carbonara?"
```

## Status

**v0.4** — radical rewrite from a 4-LLM-call RAG pipeline back to the original Karpathy-flavoured idea. Three binaries (`loom`, `loom-mcp`, `Loom.app`), one SQLite table.

## License

MIT — see [LICENSE](LICENSE).
