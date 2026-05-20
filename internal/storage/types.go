package storage

import "time"

// File is one row in the `files` table: a single document on disk that has
// been scanned and summarised by the LLM.
type File struct {
	ID        int64
	RelPath   string
	Hash      string
	MTime     int64
	Kind      string
	Title     string
	Content   string
	Summary   string
	Keywords  []string
	IndexedAt time.Time
}

// Hit is a search result row, with FTS BM25 ranking attached.
type Hit struct {
	File
	Rank float64
}
