package knowledge

// IngestDocumentData is a document to add or update. Source identifies it: ingesting the same
// source again replaces the document when Text changed and does nothing otherwise.
type IngestDocumentData struct {
	Source string
	Title  string
	// Kind of the document; empty keeps the kind of an existing document (other for a new one).
	// Changing the kind of an existing document is ErrConflict unless Force is set: a different kind
	// usually means another document that happens to have the same source.
	Kind  Kind
	Text  string
	Force bool
}

type SearchChunksData struct {
	Query string
	Limit int    // default 5, at most 20
	Kinds []Kind // nil or empty: all kinds
}

type SaveCorrectionData struct {
	Agent     string
	TaskKind  string
	Original  string
	Corrected string
	Comment   string
}

// FindSimilarCorrectionsData looks for the agent's corrections similar to Query (usually the new draft
// or the task). Agent is required; an empty TaskKind matches any.
type FindSimilarCorrectionsData struct {
	Query    string
	Agent    string
	TaskKind string
	Limit    int // default 5, at most 20
}
