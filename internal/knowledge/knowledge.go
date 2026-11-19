// Package knowledge is the brand knowledge base: documents (voice, audience, products, brand book,
// best posts) split into chunks with embeddings, and the trainer's corrections to agents' drafts.
package knowledge

import (
	"fmt"
	"time"
)

// Kind is the type of a knowledge base document.
type Kind string

const (
	BrandKind   Kind = "brand"   // voice, audience, brand book
	ProductKind Kind = "product" // products and offers
	PostKind    Kind = "post"    // best posts as examples
	OtherKind   Kind = "other"
)

// Kinds lists all document kinds, in the order shown to users.
var Kinds = []Kind{BrandKind, ProductKind, PostKind, OtherKind}

// ParseKind validates a kind name.
func ParseKind(s string) (Kind, error) {
	for _, k := range Kinds {
		if string(k) == s {
			return k, nil
		}
	}

	return "", fmt.Errorf("unknown document kind %q (want brand, product, post or other): %w", s, ErrValidation)
}

// IngestStatus says what Ingest did with the document.
type IngestStatus string

const (
	CreatedIngestStatus IngestStatus = "created"
	UpdatedIngestStatus IngestStatus = "updated"
	// Same checksum, embedding model and chunker version: chunks and embeddings are kept.
	UnchangedIngestStatus IngestStatus = "unchanged"
)

type Document struct {
	ID             int64
	Source         string
	Title          string
	Kind           Kind
	Checksum       string
	EmbeddingModel string
	ChunkerVersion int
	ChunkCount     int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type IngestResult struct {
	DocumentID int64
	Status     IngestStatus
	Chunks     int
}

// SearchResult is a chunk found by meaning; Score is cosine similarity (1 is identical).
type SearchResult struct {
	ChunkID int64
	Source  string
	Title   string
	Kind    Kind
	Content string
	Score   float64
}

// Correction is the trainer's edit of an agent's draft: a few-shot example of "how not to / how to".
type Correction struct {
	ID        int64
	Agent     string
	TaskKind  string
	Original  string
	Corrected string
	Comment   string
	CreatedAt time.Time
	Score     float64 // cosine similarity to the query; 0 when found without embeddings
}

// ReembedResult says what Reembed filled in.
type ReembedResult struct {
	Corrections int // corrections that got a vector of the current model
	Documents   int // documents whose chunks were re-embedded with the current model
	Chunks      int
	// Documents split by another chunker version: they need `kb ingest` again (the text is not stored).
	StaleChunking int
}
