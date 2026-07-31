// Package index walks the user's notes folder, hashes every supported file,
// and asks the LLM for a one-shot summary + keywords for anything new or
// changed. The DB row is the index; the file on disk is the truth.
package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/MatteoAdamo82/loom/internal/extract"
	"github.com/MatteoAdamo82/loom/internal/llm"
	"github.com/MatteoAdamo82/loom/internal/storage"
)

// Result reports what changed during a scan.
type Result struct {
	Added    int
	Updated  int
	Moved    int // file moved/renamed: new path, same content, LLM skipped
	Removed  int
	Skipped  int
	Failed   int
	Errors   []FileError
	Duration time.Duration
}

// embedCharBudget caps how much of a file we feed to the embedding model. Small
// embedding models have a short context (e.g. embeddinggemma ≈ 2K tokens); this
// keeps the vector representative without overflowing it. Files larger than this
// should be split upstream (one logical chunk per file).
const embedCharBudget = 8000

// processOutcome classifies the result of processing a single file.
type processOutcome int

const (
	outcomeAdded   processOutcome = iota
	outcomeMoved                  // same content, new path — LLM call skipped
	outcomeUpdated                // content changed — LLM called
)

type FileError struct {
	RelPath string
	Err     error
}

// Payload renders the result as the JSON shape returned by the MCP and HTTP
// scan endpoints, so both report the same thing. Per-file errors are included
// whenever any file failed: a bare "failed: 1" leaves a caller with no way to
// tell which file broke or why, and re-running blind is the only recourse.
func (r *Result) Payload() map[string]any {
	p := map[string]any{
		"added":       r.Added,
		"updated":     r.Updated,
		"moved":       r.Moved,
		"removed":     r.Removed,
		"skipped":     r.Skipped,
		"failed":      r.Failed,
		"duration_ms": r.Duration.Milliseconds(),
	}
	if len(r.Errors) > 0 {
		errs := make([]map[string]string, 0, len(r.Errors))
		for _, fe := range r.Errors {
			errs = append(errs, map[string]string{
				"rel_path": fe.RelPath,
				"error":    fe.Err.Error(),
			})
		}
		p["errors"] = errs
	}
	return p
}

// Event is published while a scan runs so the GUI can show progress.
type Event struct {
	Phase   string // "scan" | "summarize" | "delete" | "done"
	RelPath string
	Index   int
	Total   int
	Err     error
}

// Indexer is the entry point. Construct with New() and call Scan() / Force().
type Indexer struct {
	NotesDirs  []string
	Store      *storage.Store
	LLM        llm.Client
	Embedder   llm.Embedder // optional: when set, store a vector per file for hybrid search
	Registry   *extract.Registry
	Concurrent int
	OnEvent    func(Event)
}

// New creates an Indexer for one or more notes directories.
// Pass a single-element slice for the common case of one folder.
func New(notesDirs []string, store *storage.Store, lc llm.Client) *Indexer {
	return &Indexer{
		NotesDirs:  notesDirs,
		Store:      store,
		LLM:        lc,
		Registry:   extract.DefaultRegistry(),
		Concurrent: 2,
	}
}

// Scan walks NotesDir and brings the DB in sync. Files not present on disk
// are dropped from the index; new or modified files are re-summarised.
func (ix *Indexer) Scan(ctx context.Context) (*Result, error) {
	return ix.scan(ctx, false)
}

// Force re-summarises every file regardless of mtime/hash.
func (ix *Indexer) Force(ctx context.Context) (*Result, error) {
	return ix.scan(ctx, true)
}

func (ix *Indexer) scan(ctx context.Context, force bool) (*Result, error) {
	started := time.Now()
	res := &Result{}

	for _, d := range ix.NotesDirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("ensure notes dir %s: %w", d, err)
		}
	}

	disk, err := ix.walk()
	if err != nil {
		return nil, err
	}
	ix.emit(Event{Phase: "scan", Total: len(disk)})

	known, err := ix.Store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list index: %w", err)
	}
	knownByPath := make(map[string]*storage.File, len(known))
	knownByHash := make(map[string]*storage.File, len(known))
	for _, f := range known {
		knownByPath[f.RelPath] = f
		// Only keep the first record per hash so moves are deterministic.
		// knownByHash is read-only during the concurrent processing phase.
		if _, dup := knownByHash[f.Hash]; !dup {
			knownByHash[f.Hash] = f
		}
	}
	// lookupByHash is a closure over the read-only map. Passing a function
	// instead of the whole map keeps processOne decoupled from the map type.
	lookupByHash := func(hash string) *storage.File { return knownByHash[hash] }

	// Find new/modified files needing re-summarisation.
	type job struct {
		entry diskEntry
		known *storage.File
	}
	var work []job
	for _, e := range disk {
		k := knownByPath[e.relPath]
		if !force && k != nil && k.MTime == e.mtime {
			res.Skipped++
			continue
		}
		work = append(work, job{entry: e, known: k})
	}

	// Process jobs with bounded concurrency.
	conc := ix.Concurrent
	if conc < 1 {
		conc = 1
	}
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	var mu sync.Mutex
	processed := 0

	for i, j := range work {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, j job) {
			defer wg.Done()
			defer func() { <-sem }()

			ix.emit(Event{Phase: "summarize", RelPath: j.entry.relPath, Index: idx + 1, Total: len(work)})

			outcome, err := ix.processOne(ctx, j.entry, j.known, lookupByHash, force)
			if err != nil {
				mu.Lock()
				res.Failed++
				res.Errors = append(res.Errors, FileError{RelPath: j.entry.relPath, Err: err})
				mu.Unlock()
				ix.emit(Event{Phase: "summarize", RelPath: j.entry.relPath, Index: idx + 1, Total: len(work), Err: err})
				return
			}
			mu.Lock()
			switch outcome {
			case outcomeMoved:
				res.Moved++
			case outcomeAdded:
				res.Added++
			default:
				res.Updated++
			}
			processed++
			mu.Unlock()
		}(i, j)
	}
	wg.Wait()

	// Reconcile deletions.
	diskByPath := make(map[string]bool, len(disk))
	for _, e := range disk {
		diskByPath[e.relPath] = true
	}
	for relPath := range knownByPath {
		if diskByPath[relPath] {
			continue
		}
		if err := ix.Store.Delete(ctx, relPath); err != nil {
			res.Errors = append(res.Errors, FileError{RelPath: relPath, Err: err})
			res.Failed++
			continue
		}
		res.Removed++
		ix.emit(Event{Phase: "delete", RelPath: relPath})
	}

	res.Duration = time.Since(started)
	ix.emit(Event{Phase: "done"})
	return res, nil
}

// processOne extracts, hashes, summarises, and upserts a single file.
// lookupByHash returns a prior DB record whose hash matches, enabling
// move/rename detection without re-calling the LLM.
func (ix *Indexer) processOne(
	ctx context.Context,
	e diskEntry,
	known *storage.File,
	lookupByHash func(hash string) *storage.File,
	force bool,
) (processOutcome, error) {
	doc, err := ix.extract(e.absPath)
	if err != nil {
		return outcomeAdded, fmt.Errorf("extract: %w", err)
	}

	if !force {
		// Same path, same hash → only mtime/title/content may have drifted; skip LLM.
		if known != nil && known.Hash == doc.Hash {
			return outcomeUpdated, ix.Store.Upsert(ctx, &storage.File{
				ID:       known.ID,
				RelPath:  e.relPath,
				Hash:     doc.Hash,
				MTime:    e.mtime,
				Kind:     doc.Kind,
				Title:    doc.Title,
				Content:  doc.Content,
				Summary:  known.Summary,
				Keywords: known.Keywords,
			})
		}

		// New path, matching hash → move/rename detected; inherit summary.
		if known == nil {
			if prior := lookupByHash(doc.Hash); prior != nil && prior.RelPath != e.relPath {
				if err := ix.Store.Upsert(ctx, &storage.File{
					RelPath:  e.relPath,
					Hash:     doc.Hash,
					MTime:    e.mtime,
					Kind:     doc.Kind,
					Title:    doc.Title,
					Content:  doc.Content,
					Summary:  prior.Summary,
					Keywords: prior.Keywords,
				}); err != nil {
					return outcomeMoved, err
				}
				// The moved file is a new row (new id) → it has no vector yet.
				ix.embedFile(ctx, e.relPath, doc.Title, doc.Content)
				return outcomeMoved, nil
			}
		}
	}

	// New or genuinely changed file — call the LLM.
	summary, keywords, err := Summarise(ctx, ix.LLM, doc.Title, doc.Content)
	if err != nil {
		return outcomeAdded, fmt.Errorf("summarise: %w", err)
	}

	outcome := outcomeUpdated
	if known == nil {
		outcome = outcomeAdded
	}
	if err := ix.Store.Upsert(ctx, &storage.File{
		RelPath:  e.relPath,
		Hash:     doc.Hash,
		MTime:    e.mtime,
		Kind:     doc.Kind,
		Title:    doc.Title,
		Content:  doc.Content,
		Summary:  summary,
		Keywords: keywords,
	}); err != nil {
		return outcome, err
	}
	ix.embedFile(ctx, e.relPath, doc.Title, doc.Content)
	return outcome, nil
}

// embedFile computes and stores the embedding for a file. It is a no-op when no
// Embedder is configured, and best-effort otherwise: an embedding failure is
// swallowed so the file still gets indexed (and stays searchable via BM25).
func (ix *Indexer) embedFile(ctx context.Context, relPath, title, content string) {
	if ix.Embedder == nil {
		return
	}
	text := strings.TrimSpace(title + "\n\n" + content)
	if len(text) > embedCharBudget {
		text = text[:embedCharBudget]
	}
	vecs, err := ix.Embedder.Embed(ctx, []string{text})
	if err != nil || len(vecs) == 0 || len(vecs[0]) == 0 {
		return
	}
	_ = ix.Store.SetVector(ctx, relPath, vecs[0], ix.Embedder.Name())
}

func (ix *Indexer) extract(absPath string) (*extract.Document, error) {
	ext, err := ix.Registry.Resolve(absPath)
	if err != nil {
		// Fallback to text for unknown extensions: read the bytes raw.
		return rawTextDocument(absPath)
	}
	return ext.Extract(absPath)
}

// rawTextDocument is used for unknown extensions: we just read the bytes and
// hope they're text. If not, the LLM will produce a useless summary — that's
// OK, the row still appears in the index so the user sees it.
func rawTextDocument(path string) (*extract.Document, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(b)
	base := filepath.Base(path)
	return &extract.Document{
		URI:     "file://" + path,
		Kind:    strings.TrimPrefix(filepath.Ext(path), "."),
		Title:   strings.TrimSuffix(base, filepath.Ext(base)),
		Content: string(b),
		Hash:    hex.EncodeToString(h[:]),
	}, nil
}

func (ix *Indexer) emit(e Event) {
	if ix.OnEvent != nil {
		ix.OnEvent(e)
	}
}

// ---------------------------------------------------------------------------
// disk walk
// ---------------------------------------------------------------------------

type diskEntry struct {
	relPath string
	absPath string
	mtime   int64
}


func (ix *Indexer) walk() ([]diskEntry, error) {
	// When scanning more than one root directory, prefix every relPath with
	// the basename of its root so paths stay unique in the DB.
	// Example: root "/home/user/work"  → relPath "work/a.md"
	//          root "/home/user/notes" → relPath "notes/b.md"
	// With a single directory the prefix is omitted (backward-compatible).
	usePrefix := len(ix.NotesDirs) > 1

	// Build a deduplicated prefix per root. If two roots share the same
	// basename (e.g. "/a/notes" and "/b/notes"), append a numeric suffix.
	prefixFor := make(map[string]string, len(ix.NotesDirs))
	baseCounts := map[string]int{}
	for _, root := range ix.NotesDirs {
		base := filepath.Base(root)
		baseCounts[base]++
	}
	baseUsed := map[string]int{}
	for _, root := range ix.NotesDirs {
		base := filepath.Base(root)
		if baseCounts[base] == 1 {
			prefixFor[root] = base
		} else {
			baseUsed[base]++
			prefixFor[root] = fmt.Sprintf("%s_%d", base, baseUsed[base])
		}
	}

	var out []diskEntry
	for _, root := range ix.NotesDirs {
		prefix := prefixFor[root]
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// Skip hidden dirs (.git, .obsidian, etc.) but allow the root.
				if p == root {
					return nil
				}
				if strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") {
				return nil
			}
			// Ask the registry whether any extractor handles this file.
			// This covers the base types (md, pdf, html, txt) and also image
			// types when a VisionExtractor has been registered.
			if !ix.Registry.Supports(p) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			relSlash := filepath.ToSlash(rel)
			if usePrefix {
				relSlash = prefix + "/" + relSlash
			}
			out = append(out, diskEntry{
				relPath: relSlash,
				absPath: p,
				mtime:   info.ModTime().Unix(),
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
