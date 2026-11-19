package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Prokud1n/ai-agents/internal/knowledge/chunk"
)

const (
	DefaultLimit = 5
	MaxLimit     = 20

	// Document limits: brand materials are far smaller; larger texts are split into several files.
	MaxDocumentRunes  = 1_000_000 // about 330k tokens
	MaxDocumentChunks = 1000

	reembedBatch      = 100 // rows embedded per request by Reembed
	vectorPrecision   = 32  // float32 components
	bytesPerComponent = 12  // rough size of one rendered component, for preallocation
)

type embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Model names the vectors' model: vectors of different models are never compared.
	Model() string
}

type Service struct {
	repo     *repo
	embedder embedder
	log      *slog.Logger
}

func NewService(r *repo, e embedder, log *slog.Logger) *Service {
	return &Service{repo: r, embedder: e, log: log}
}

// Ingest adds the document or replaces its chunks when its text, the embedding model or the chunker
// version changed; otherwise only title and kind are updated. Chunks are replaced in one transaction.
func (s *Service) Ingest(ctx context.Context, data IngestDocumentData) (IngestResult, error) {
	data, err := normalizeIngestData(data)
	if err != nil {
		return IngestResult{}, err
	}

	doc := &documentRow{
		Source:         data.Source,
		Title:          data.Title,
		Kind:           data.Kind,
		Checksum:       Checksum(data.Text),
		EmbeddingModel: s.embedder.Model(),
		ChunkerVersion: chunk.Version,
	}

	if res, unchanged, err := s.skipUnchanged(ctx, doc); err != nil || unchanged {
		return res, err
	}

	rows, err := s.embedChunks(ctx, data.Source, data.Text)
	if err != nil {
		return IngestResult{}, err
	}

	res, err := s.repo.ReplaceDocument(ctx, doc, rows)
	if err != nil {
		return IngestResult{}, err
	}

	s.log.InfoContext(ctx, "knowledge: ingested document",
		"source", data.Source, "status", res.Status, "chunks", res.Chunks, "document_id", res.DocumentID)

	return res, nil
}

// skipUnchanged reports whether the stored version is current; then only its title and kind are
// updated, and chunks with embeddings are kept. ReplaceDocument checks again under a lock.
func (s *Service) skipUnchanged(ctx context.Context, doc *documentRow) (res IngestResult, unchanged bool, err error) {
	existing, err := s.repo.GetDocumentBySource(ctx, doc.Source)
	if errors.Is(err, ErrNotFound) {
		return IngestResult{}, false, nil
	}

	if err != nil {
		return IngestResult{}, false, err
	}

	if !doc.sameVersion(&existing) {
		return IngestResult{}, false, nil
	}

	if existing.Title != doc.Title || existing.Kind != doc.Kind {
		if err := s.repo.UpdateDocumentMeta(ctx, existing.ID, doc.Title, doc.Kind); err != nil {
			return IngestResult{}, false, err
		}
	}

	return IngestResult{DocumentID: existing.ID, Status: UnchangedIngestStatus, Chunks: existing.ChunkCount}, true, nil
}

func (s *Service) embedChunks(ctx context.Context, source, text string) ([]chunkRow, error) {
	chunks := chunk.Split(text)
	if len(chunks) == 0 {
		return nil, fmt.Errorf("document %q has no text: %w", source, ErrValidation)
	}

	if len(chunks) > MaxDocumentChunks {
		return nil, fmt.Errorf("document %q has %d chunks, at most %d: %w", source, len(chunks), MaxDocumentChunks, ErrValidation)
	}

	texts := make([]string, 0, len(chunks))
	for _, c := range chunks {
		texts = append(texts, c.Text)
	}

	vectors, err := s.embedAll(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("failed to embed document chunks: %w", err)
	}

	rows := make([]chunkRow, 0, len(chunks))
	for i, c := range chunks {
		rows = append(rows, chunkRow{ord: i, content: c.Text, embedding: vectors[i], tokens: c.Tokens})
	}

	return rows, nil
}

// Search finds chunks closest in meaning to the query among documents of the current embedding model,
// best first.
func (s *Service) Search(ctx context.Context, data SearchChunksData) ([]SearchResult, error) {
	query := strings.TrimSpace(data.Query)
	if query == "" {
		return nil, fmt.Errorf("search query is empty: %w", ErrValidation)
	}

	kinds := make([]string, 0, len(data.Kinds))
	for _, k := range data.Kinds {
		if _, err := ParseKind(string(k)); err != nil {
			return nil, err
		}

		kinds = append(kinds, string(k))
	}

	vector, err := s.embedOne(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to embed search query: %w", err)
	}

	return s.repo.SearchChunks(ctx, vector, s.embedder.Model(), kinds, normalizeLimit(data.Limit))
}

// Delete removes the document and its chunks; ErrNotFound if there is no such source.
func (s *Service) Delete(ctx context.Context, source string) error {
	source = strings.TrimSpace(source)
	if source == "" {
		return fmt.Errorf("document source is empty: %w", ErrValidation)
	}

	if err := s.repo.DeleteDocument(ctx, source); err != nil {
		return err
	}

	s.log.InfoContext(ctx, "knowledge: deleted document", "source", source)

	return nil
}

// SaveCorrection stores the trainer's correction. It is saved even when embeddings are unavailable:
// it then has no vector until Reembed, and is found only by FindSimilarCorrections' fallback.
func (s *Service) SaveCorrection(ctx context.Context, input *SaveCorrectionData) (Correction, error) {
	data, err := normalizeCorrectionData(input)
	if err != nil {
		return Correction{}, err
	}

	vector, err := s.embedOne(ctx, CorrectionEmbeddingText(data.Comment, data.Original))
	if err != nil {
		s.log.ErrorContext(ctx, "knowledge: failed to embed correction, saving without embedding",
			"error", err, "agent", data.Agent, "task_kind", data.TaskKind)

		vector = ""
	}

	c, err := s.repo.InsertCorrection(ctx, &data, vector, s.embedder.Model())
	if err != nil {
		return Correction{}, err
	}

	s.log.InfoContext(ctx, "knowledge: saved correction", "correction_id", c.ID, "agent", c.Agent, "task_kind", c.TaskKind)

	return c, nil
}

// FindSimilarCorrections returns the agent's corrections closest to the query, for few-shot examples.
// If the query is empty or cannot be embedded, the most recent corrections are returned instead.
func (s *Service) FindSimilarCorrections(ctx context.Context, data FindSimilarCorrectionsData) ([]Correction, error) {
	filter := &FindSimilarCorrectionsData{Agent: strings.TrimSpace(data.Agent), TaskKind: strings.TrimSpace(data.TaskKind)}
	if filter.Agent == "" {
		return nil, fmt.Errorf("agent is required to find corrections: %w", ErrValidation)
	}

	limit := normalizeLimit(data.Limit)

	query := strings.TrimSpace(data.Query)
	if query == "" {
		return s.repo.FindRecentCorrections(ctx, filter, limit)
	}

	vector, err := s.embedOne(ctx, query)
	if err != nil {
		s.log.ErrorContext(ctx, "knowledge: failed to embed corrections query, using recent corrections",
			"error", err, "agent", filter.Agent, "task_kind", filter.TaskKind)

		return s.repo.FindRecentCorrections(ctx, filter, limit)
	}

	return s.repo.FindSimilarCorrections(ctx, vector, s.embedder.Model(), filter, limit)
}

// Reembed fills in missing vectors and replaces vectors of other models with the current one:
// corrections saved while embeddings were unavailable, and chunks of documents ingested with another
// model. Documents split by another chunker version are only counted: they need `kb ingest` again.
func (s *Service) Reembed(ctx context.Context) (ReembedResult, error) {
	model := s.embedder.Model()

	var res ReembedResult

	corrections, err := s.reembedCorrections(ctx, model)
	res.Corrections = corrections

	if err != nil {
		return res, err
	}

	ids, err := s.repo.DocumentsWithOtherModel(ctx, model)
	if err != nil {
		return res, err
	}

	for _, id := range ids {
		chunks, err := s.repo.DocumentChunks(ctx, id)
		if err != nil {
			return res, err
		}

		chunkIDs, vectors, err := s.embedPending(ctx, chunks)
		if err != nil {
			return res, fmt.Errorf("failed to embed chunks of document %d: %w", id, err)
		}

		if err := s.repo.SetDocumentEmbeddings(ctx, id, model, chunkIDs, vectors); err != nil {
			return res, err
		}

		res.Documents++
		res.Chunks += len(chunks)
	}

	if res.StaleChunking, err = s.repo.CountStaleChunking(ctx, chunk.Version); err != nil {
		return res, err
	}

	s.log.InfoContext(ctx, "knowledge: reembedded", "corrections", res.Corrections, "documents", res.Documents,
		"chunks", res.Chunks, "stale_chunking", res.StaleChunking, "model", model)

	return res, nil
}

func (s *Service) reembedCorrections(ctx context.Context, model string) (int, error) {
	var (
		done    int
		afterID int64
	)

	for {
		pending, err := s.repo.PendingCorrections(ctx, model, afterID, reembedBatch)
		if err != nil || len(pending) == 0 {
			return done, err
		}

		ids, vectors, err := s.embedPending(ctx, pending)
		if err != nil {
			return done, fmt.Errorf("failed to embed corrections: %w", err)
		}

		if err := s.repo.SetCorrectionEmbeddings(ctx, model, ids, vectors); err != nil {
			return done, err
		}

		done += len(pending)
		afterID = pending[len(pending)-1].id
	}
}

func (s *Service) embedPending(ctx context.Context, pending []pendingEmbedding) (ids []int64, vectors []string, err error) {
	ids = make([]int64, 0, len(pending))
	texts := make([]string, 0, len(pending))

	for _, p := range pending {
		ids = append(ids, p.id)
		texts = append(texts, p.text)
	}

	if vectors, err = s.embedAll(ctx, texts); err != nil {
		return nil, nil, err
	}

	return ids, vectors, nil
}

// embedAll embeds texts and renders the vectors as pgvector literals, one per text.
func (s *Service) embedAll(ctx context.Context, texts []string) ([]string, error) {
	vectors, err := s.embedder.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}

	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("got %d vectors for %d texts", len(vectors), len(texts))
	}

	rendered := make([]string, 0, len(vectors))
	for _, v := range vectors {
		rendered = append(rendered, formatVector(v))
	}

	return rendered, nil
}

func (s *Service) embedOne(ctx context.Context, text string) (string, error) {
	vectors, err := s.embedAll(ctx, []string{text})
	if err != nil {
		return "", err
	}

	return vectors[0], nil
}

// Checksum identifies the document text: re-ingesting the same text is a no-op.
func Checksum(text string) string {
	sum := sha256.Sum256([]byte(text))

	return hex.EncodeToString(sum[:])
}

// CorrectionEmbeddingText is what a correction is found by: the trainer's comment says what was wrong,
// the original draft is what a new draft will resemble.
func CorrectionEmbeddingText(comment, original string) string {
	if comment == "" {
		return original
	}

	return comment + "\n\n" + original
}

func normalizeIngestData(data IngestDocumentData) (IngestDocumentData, error) {
	data.Source = strings.TrimSpace(data.Source)
	data.Title = strings.TrimSpace(data.Title)
	data.Text = chunk.Normalize(data.Text)

	if data.Source == "" {
		return IngestDocumentData{}, fmt.Errorf("document source is empty: %w", ErrValidation)
	}

	if data.Kind == "" {
		data.Kind = OtherKind
	}

	if _, err := ParseKind(string(data.Kind)); err != nil {
		return IngestDocumentData{}, err
	}

	if data.Text == "" {
		return IngestDocumentData{}, fmt.Errorf("document %q has no text: %w", data.Source, ErrValidation)
	}

	if n := utf8.RuneCountInString(data.Text); n > MaxDocumentRunes {
		return IngestDocumentData{}, fmt.Errorf("document %q has %d characters, at most %d: %w",
			data.Source, n, MaxDocumentRunes, ErrValidation)
	}

	if data.Title == "" {
		data.Title = data.Source
	}

	return data, nil
}

func normalizeCorrectionData(input *SaveCorrectionData) (SaveCorrectionData, error) {
	data := *input
	data.Agent = strings.TrimSpace(data.Agent)
	data.TaskKind = strings.TrimSpace(data.TaskKind)
	data.Original = strings.TrimSpace(data.Original)
	data.Corrected = strings.TrimSpace(data.Corrected)
	data.Comment = strings.TrimSpace(data.Comment)

	required := []struct{ name, value string }{
		{"agent", data.Agent},
		{"task kind", data.TaskKind},
		{"original", data.Original},
		{"corrected", data.Corrected},
	}
	for _, f := range required {
		if f.value == "" {
			return SaveCorrectionData{}, fmt.Errorf("correction %s is empty: %w", f.name, ErrValidation)
		}
	}

	return data, nil
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return DefaultLimit
	}

	return min(limit, MaxLimit)
}

// formatVector renders a pgvector text literal: [0.1,0.2,...].
func formatVector(v []float32) string {
	buf := make([]byte, 0, len(v)*bytesPerComponent)
	buf = append(buf, '[')

	for i, x := range v {
		if i > 0 {
			buf = append(buf, ',')
		}

		buf = strconv.AppendFloat(buf, float64(x), 'g', -1, vectorPrecision)
	}

	return string(append(buf, ']'))
}
