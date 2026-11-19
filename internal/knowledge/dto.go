package knowledge

// IngestDocumentData is a document to add or update. Source identifies it: ingesting the same
// source again replaces the document when Text changed and does nothing otherwise.
type IngestDocumentData struct {
	Source string
	Title  string
	Kind   Kind
	Text   string
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
