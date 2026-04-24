package storage

import (
	"encoding/json"
	"time"
)

type Source struct {
	ID         int64
	URI        string
	Kind       string
	Title      string
	Content    string
	Hash       string
	IngestedAt time.Time
	Metadata   json.RawMessage
}

type Note struct {
	ID        int64
	Slug      string
	Title     string
	Kind      string
	Content   string
	Summary   string
	Keywords  []string
	CreatedAt time.Time
	UpdatedAt time.Time
	Version   int
}

type LinkKind string

const (
	LinkWikilink    LinkKind = "wikilink"
	LinkCitation    LinkKind = "citation"
	LinkSeeAlso     LinkKind = "see-also"
	LinkDerivedFrom LinkKind = "derived-from"
)

type Link struct {
	ID           int64
	FromNoteID   *int64
	FromSourceID *int64
	ToNoteID     *int64
	ToSourceID   *int64
	Kind         LinkKind
	Context      string
}

type Chunk struct {
	ID       int64
	SourceID *int64
	NoteID   *int64
	Content  string
	Position int
	Tokens   int
}

type Operation struct {
	ID      int64
	Kind    string
	At      time.Time
	Actor   string
	Summary string
	Details json.RawMessage
}

type SearchHit struct {
	EntityRef string
	Title     string
	Snippet   string
	Score     float64
	Kind      string
}
