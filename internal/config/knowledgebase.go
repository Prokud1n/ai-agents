package config

// KnowledgeBase configures where `kb ingest` takes brand materials from.
type KnowledgeBase struct {
	// Root is the only folder files are ingested from: a document's source is its path relative to it,
	// so the same file always gets the same source, whatever folder or file was passed to ingest.
	// Relative to the current directory; make targets run from the project root, so it is ./kb there.
	Root string `env:"KB_ROOT" envDefault:"./kb"`
}
