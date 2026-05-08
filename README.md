# Loom

> A folder of files + an LLM as your knowledge base.

Loom is a small desktop app and CLI built around one idea: **the files in a folder are the truth**. You drop markdown, PDF, HTML and text files into a directory of your choice. Loom scans it, asks an LLM to write a summary and a handful of keywords for each file, and stores those next to a full-text index in SQLite. When you ask a question, Loom does ONE LLM call: it picks the most relevant files with BM25 and hands them to the model.

No embeddings. No vector DB. No chunking. No graph. No fancy reranking.

This is a deliberate revival of [Andrej Karpathy's "LLM Wiki" pattern](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f), with one concession: a tiny SQLite index so search stays snappy as the folder grows past `grep`-friendly size.

## How it works

```
~/loom/                          ← your folder, your files, your editor
├── ricette.md
├── papers/
│   └── llm-wiki.pdf
├── articolo.html
└── note-libere.md

~/.loom/
├── config.toml                  ← provider + model + folder paths
└── index.db                     ← SQLite (regenerable from the folder)
```

**Scan** — for every new or modified file, Loom extracts text (via `ledongthuc/pdf`, `go-readability`, or plain read), then asks the LLM:
> "Riassumi in 150-250 parole + estrai 5-8 keywords come JSON."

The summary, keywords, content, hash and mtime go into one SQLite row plus an FTS5 mirror.

**Ask** — Loom runs an FTS5 BM25 search over `title + summary + keywords + content`, takes the top 5 hits, and hands their summaries + (truncated) content to the LLM with a system prompt that says "rispondi solo usando queste note, cita con `[rel_path]`". One call. The answer streams back.

That's it.

## Install

Requires Go 1.26+ and (for the GUI) Node 20+.

```bash
git clone https://github.com/MatteoAdamo82/loom.git
cd loom
go install ./cmd/loom ./cmd/loom-mcp

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

`~/.loom/config.toml`:

```toml
notes_dir = "~/loom"
db_path   = "~/.loom/index.db"

[llm]
provider    = "ollama"           # "ollama" | "openai" | "anthropic"
model       = "llama3.1:8b"
endpoint    = "http://localhost:11434"
api_key_env = ""                 # e.g. "OPENAI_API_KEY"
```

Cloud providers:

```toml
[llm]
provider    = "anthropic"
model       = "claude-sonnet-4-6"
api_key_env = "ANTHROPIC_API_KEY"
```

Loom never stores the API key on disk — only the env var name.

## PDFs

PDFs are best-effort:

1. Pure-Go text extraction first (`ledongthuc/pdf`).
2. If a page comes out empty AND `pdftoppm` + `tesseract` are on PATH, OCR fills the gaps.
3. If both fail, the file is still indexed with a placeholder summary; the LLM just won't quote it.

To install OCR tools:

```bash
# macOS
brew install poppler tesseract tesseract-lang

# Debian/Ubuntu
sudo apt install poppler-utils tesseract-ocr tesseract-ocr-ita
```

Want a non-English language? Set `TESSERACT_LANGS` in your shell:

```bash
export TESSERACT_LANGS="eng+ita"
```

## Use Loom as memory for Claude Code / Claude Desktop

`loom-mcp` is a stdio MCP server that exposes the indexed folder to any MCP-aware client. Add to `~/.claude/settings.json`:

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

Then ask Claude things like *"check loom for what I wrote about postgres tuning"* — it'll call `loom.ask` and ground its answer in your notes.

Tools exposed:

| Tool | Purpose |
|---|---|
| `loom.ask(question, top_k?)` | One-shot answer with file citations (1 LLM call) |
| `loom.search(query, limit?)` | Raw BM25 hits, no LLM call |
| `loom.scan(force?)` | (Re)index the notes folder |
| `loom.list_files()` | Browse every indexed file with summary + keywords |
| `loom.get_file(rel_path)` | Fetch full extracted content of one file |

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
  loom-gui/    Wails desktop app (Svelte + TS frontend)
internal/
  config/      TOML loader (5 fields)
  extract/     Text, PDF, HTML extractors
  index/       Folder walk + LLM summarise + DB upsert
  llm/         Ollama / OpenAI / Anthropic adapters
  query/       BM25 search → 1 LLM call → answer + citations
  storage/     SQLite (one `files` table + FTS5 mirror)
```

## Development

```bash
go test ./...                    # all tests
go vet ./...

# Smoke test against a real Ollama:
mkdir -p /tmp/loomtest
echo "# Carbonara\nGuanciale, pecorino, uova." > /tmp/loomtest/ricetta.md
echo 'notes_dir = "/tmp/loomtest"
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
