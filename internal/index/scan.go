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
	Removed  int
	Skipped  int
	Failed   int
	Errors   []FileError
	Duration time.Duration
}

type FileError struct {
	RelPath string
	Err     error
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
	NotesDir   string
	Store      *storage.Store
	LLM        llm.Client
	Registry   *extract.Registry
	Concurrent int
	OnEvent    func(Event)
}

func New(notesDir string, store *storage.Store, lc llm.Client) *Indexer {
	return &Indexer{
		NotesDir:   notesDir,
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

	if err := os.MkdirAll(ix.NotesDir, 0o755); err != nil {
		return nil, fmt.Errorf("ensure notes dir: %w", err)
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
	for _, f := range known {
		knownByPath[f.RelPath] = f
	}

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

			if err := ix.processOne(ctx, j.entry, j.known, force); err != nil {
				mu.Lock()
				res.Failed++
				res.Errors = append(res.Errors, FileError{RelPath: j.entry.relPath, Err: err})
				mu.Unlock()
				ix.emit(Event{Phase: "summarize", RelPath: j.entry.relPath, Index: idx + 1, Total: len(work), Err: err})
				return
			}
			mu.Lock()
			if j.known == nil {
				res.Added++
			} else {
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
func (ix *Indexer) processOne(ctx context.Context, e diskEntry, known *storage.File, force bool) error {
	doc, err := ix.extract(e.absPath)
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	// If hash is unchanged from a known row, skip the LLM call. (The mtime
	// check above already covers most of these — this is the second-line
	// defence for files touched without content change.)
	if !force && known != nil && known.Hash == doc.Hash {
		return ix.Store.Upsert(ctx, &storage.File{
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

	summary, keywords, err := Summarise(ctx, ix.LLM, doc.Title, doc.Content)
	if err != nil {
		return fmt.Errorf("summarise: %w", err)
	}

	return ix.Store.Upsert(ctx, &storage.File{
		RelPath:  e.relPath,
		Hash:     doc.Hash,
		MTime:    e.mtime,
		Kind:     doc.Kind,
		Title:    doc.Title,
		Content:  doc.Content,
		Summary:  summary,
		Keywords: keywords,
	})
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

var supportedExts = map[string]bool{
	".md":       true,
	".markdown": true,
	".txt":      true,
	".pdf":      true,
	".html":     true,
	".htm":      true,
}

func (ix *Indexer) walk() ([]diskEntry, error) {
	var out []diskEntry
	err := filepath.WalkDir(ix.NotesDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip hidden dirs (.git, .obsidian, etc.) but allow the root.
			if p == ix.NotesDir {
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
		if !supportedExts[strings.ToLower(filepath.Ext(p))] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(ix.NotesDir, p)
		if err != nil {
			return err
		}
		out = append(out, diskEntry{
			relPath: filepath.ToSlash(rel),
			absPath: p,
			mtime:   info.ModTime().Unix(),
		})
		return nil
	})
	return out, err
}
