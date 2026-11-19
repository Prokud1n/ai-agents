package knowledge

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Prokud1n/ai-agents/internal/providers/postgres"
)

// Vectors are passed as pgvector text literals ('[0.1,0.2,...]') cast with ::text::vector,
// so no pgvector-specific Go type is needed.

// documentRow is what identifies a document version: re-ingest is needed when any field but
// Title and Kind changes.
type documentRow struct {
	Source         string
	Title          string
	Kind           Kind
	Checksum       string
	EmbeddingModel string
	ChunkerVersion int
}

func (d *documentRow) sameVersion(o *Document) bool {
	return d.Checksum == o.Checksum && d.EmbeddingModel == o.EmbeddingModel && d.ChunkerVersion == o.ChunkerVersion
}

type chunkRow struct {
	ord       int
	content   string
	embedding string
	tokens    int
}

// pendingEmbedding is a row whose vector is missing or made by another model.
type pendingEmbedding struct {
	id   int64
	text string
}

type repo struct {
	db *postgres.Client
}

func newRepo(db *postgres.Client) *repo {
	return &repo{db: db}
}

const selectDocument = `
	SELECT d.id, d.source, d.title, d.kind, d.checksum, d.embedding_model, d.chunker_version,
	       d.created_at, d.updated_at, (SELECT count(*) FROM kb_chunks c WHERE c.document_id = d.id)
	FROM kb_documents d
	WHERE d.source = $1`

func scanDocument(row pgx.Row, source string) (Document, error) {
	var d Document

	err := row.Scan(&d.ID, &d.Source, &d.Title, &d.Kind, &d.Checksum, &d.EmbeddingModel, &d.ChunkerVersion,
		&d.CreatedAt, &d.UpdatedAt, &d.ChunkCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return Document{}, fmt.Errorf("document %q: %w", source, ErrNotFound)
	}

	if err != nil {
		return Document{}, fmt.Errorf("failed to get document: %w", err)
	}

	return d, nil
}

func (r *repo) GetDocumentBySource(ctx context.Context, source string) (Document, error) {
	return scanDocument(r.db.GetReadConnection().QueryRow(ctx, selectDocument, source), source)
}

func (r *repo) UpdateDocumentMeta(ctx context.Context, id int64, title string, kind Kind) error {
	const query = `UPDATE kb_documents SET title = $2, kind = $3, updated_at = now() WHERE id = $1`

	if _, err := r.db.GetWriteConnection().Exec(ctx, query, id, title, string(kind)); err != nil {
		return fmt.Errorf("failed to update document: %w", err)
	}

	return nil
}

// ReplaceDocument writes the document version and its chunks in one transaction. An advisory lock on
// the source serializes concurrent ingests (including the very first one, when there is no row to
// lock yet); the stored version is re-read under the lock, and if another ingest already wrote the
// same version, nothing is changed.
func (r *repo) ReplaceDocument(ctx context.Context, doc *documentRow, chunks []chunkRow) (IngestResult, error) {
	var res IngestResult

	err := pgx.BeginFunc(ctx, r.db.GetWriteConnection(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, doc.Source); err != nil {
			return fmt.Errorf("failed to lock document source: %w", err)
		}

		existing, err := scanDocument(tx.QueryRow(ctx, selectDocument+" FOR UPDATE OF d", doc.Source), doc.Source)

		switch {
		case err == nil && doc.sameVersion(&existing):
			res = IngestResult{DocumentID: existing.ID, Status: UnchangedIngestStatus, Chunks: existing.ChunkCount}

			return nil
		case err == nil:
			res.Status = UpdatedIngestStatus
		case errors.Is(err, ErrNotFound):
			res.Status = CreatedIngestStatus
		default:
			return err
		}

		res.DocumentID, err = writeDocument(ctx, tx, doc, chunks)
		res.Chunks = len(chunks)

		return err
	})
	if err != nil {
		return IngestResult{}, fmt.Errorf("failed to replace document: %w", err)
	}

	return res, nil
}

func writeDocument(ctx context.Context, tx pgx.Tx, doc *documentRow, chunks []chunkRow) (int64, error) {
	const upsert = `
		INSERT INTO kb_documents (source, title, kind, checksum, embedding_model, chunker_version)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (source) DO UPDATE
		SET title = EXCLUDED.title, kind = EXCLUDED.kind, checksum = EXCLUDED.checksum,
		    embedding_model = EXCLUDED.embedding_model, chunker_version = EXCLUDED.chunker_version,
		    updated_at = now()
		RETURNING id`
	const insertChunk = `
		INSERT INTO kb_chunks (document_id, ord, content, embedding, tokens)
		VALUES ($1, $2, $3, $4::text::vector, $5)`

	var id int64

	err := tx.QueryRow(ctx, upsert,
		doc.Source, doc.Title, string(doc.Kind), doc.Checksum, doc.EmbeddingModel, doc.ChunkerVersion,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("failed to upsert document: %w", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM kb_chunks WHERE document_id = $1`, id); err != nil {
		return 0, fmt.Errorf("failed to delete old chunks: %w", err)
	}

	batch := &pgx.Batch{}
	for _, c := range chunks {
		batch.Queue(insertChunk, id, c.ord, c.content, c.embedding, c.tokens)
	}

	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return 0, fmt.Errorf("failed to insert chunks: %w", err)
	}

	return id, nil
}

func (r *repo) DeleteDocument(ctx context.Context, source string) error {
	const query = `DELETE FROM kb_documents WHERE source = $1`

	tag, err := r.db.GetWriteConnection().Exec(ctx, query, source)
	if err != nil {
		return fmt.Errorf("failed to delete document: %w", err)
	}

	if tag.RowsAffected() == 0 {
		return fmt.Errorf("document %q: %w", source, ErrNotFound)
	}

	return nil
}

// SearchChunks returns chunks of documents embedded by model, nearest to the vector by cosine distance.
// Empty (or nil) kinds match all documents.
//
// With filters an HNSW scan may return fewer than limit rows: hnsw.iterative_scan keeps scanning
// the index until enough rows pass them. relaxed_order may return rows slightly out of order,
// so the materialized CTE is sorted again.
func (r *repo) SearchChunks(
	ctx context.Context,
	vector, model string,
	kinds []string,
	limit int,
) ([]SearchResult, error) {
	const query = `
		WITH nearest AS MATERIALIZED (
			SELECT c.id, d.source, d.title, d.kind, c.content, c.embedding <=> $1::text::vector AS distance
			FROM kb_chunks c
			JOIN kb_documents d ON d.id = c.document_id
			WHERE d.embedding_model = $2
			  AND ($3::text[] IS NULL OR cardinality($3::text[]) = 0 OR d.kind = ANY($3::text[]))
			ORDER BY c.embedding <=> $1::text::vector
			LIMIT $4
		)
		SELECT id, source, title, kind, content, 1 - distance
		FROM nearest
		ORDER BY distance, id`

	var results []SearchResult

	err := pgx.BeginTxFunc(ctx, r.db.GetReadConnection(), pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL hnsw.iterative_scan = relaxed_order`); err != nil {
			return fmt.Errorf("failed to enable iterative scan: %w", err)
		}

		rows, err := tx.Query(ctx, query, vector, model, kinds, limit)
		if err != nil {
			return fmt.Errorf("failed to search chunks: %w", err)
		}

		results, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (SearchResult, error) {
			var res SearchResult
			err := row.Scan(&res.ChunkID, &res.Source, &res.Title, &res.Kind, &res.Content, &res.Score)

			return res, err
		})
		if err != nil {
			return fmt.Errorf("failed to read search results: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to search: %w", err)
	}

	return results, nil
}

// InsertCorrection saves a correction; an empty vector is stored as NULL (to be filled by reembed).
func (r *repo) InsertCorrection(
	ctx context.Context,
	data *SaveCorrectionData,
	vector, model string,
) (Correction, error) {
	const query = `
		INSERT INTO kb_corrections (agent, task_kind, original, corrected, comment, embedding, embedding_model)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, '')::vector, $7)
		RETURNING id, created_at`

	c := Correction{
		Agent:     data.Agent,
		TaskKind:  data.TaskKind,
		Original:  data.Original,
		Corrected: data.Corrected,
		Comment:   data.Comment,
	}

	err := r.db.GetWriteConnection().QueryRow(ctx, query,
		data.Agent, data.TaskKind, data.Original, data.Corrected, data.Comment, vector, model,
	).Scan(&c.ID, &c.CreatedAt)
	if err != nil {
		return Correction{}, fmt.Errorf("failed to insert correction: %w", err)
	}

	return c, nil
}

// FindSimilarCorrections is an exact search: the (agent, task_kind) index narrows the rows, distances
// are computed for all of them. Empty agent / taskKind match any.
func (r *repo) FindSimilarCorrections(
	ctx context.Context,
	vector, model string,
	filter *FindSimilarCorrectionsData,
	limit int,
) ([]Correction, error) {
	const query = `
		SELECT id, agent, task_kind, original, corrected, comment, created_at, 1 - (embedding <=> $1::text::vector)
		FROM kb_corrections
		WHERE embedding IS NOT NULL
		  AND embedding_model = $2
		  AND ($3 = '' OR agent = $3)
		  AND ($4 = '' OR task_kind = $4)
		ORDER BY embedding <=> $1::text::vector, id
		LIMIT $5`

	return r.queryCorrections(ctx, query, vector, model, filter.Agent, filter.TaskKind, limit)
}

// FindRecentCorrections is the fallback when the query cannot be embedded.
func (r *repo) FindRecentCorrections(ctx context.Context, filter *FindSimilarCorrectionsData, limit int) ([]Correction, error) {
	const query = `
		SELECT id, agent, task_kind, original, corrected, comment, created_at, 0::float8
		FROM kb_corrections
		WHERE ($1 = '' OR agent = $1)
		  AND ($2 = '' OR task_kind = $2)
		ORDER BY created_at DESC, id DESC
		LIMIT $3`

	return r.queryCorrections(ctx, query, filter.Agent, filter.TaskKind, limit)
}

func (r *repo) queryCorrections(ctx context.Context, query string, args ...any) ([]Correction, error) {
	rows, err := r.db.GetReadConnection().Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query corrections: %w", err)
	}

	corrections, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Correction, error) {
		var c Correction
		err := row.Scan(&c.ID, &c.Agent, &c.TaskKind, &c.Original, &c.Corrected, &c.Comment, &c.CreatedAt, &c.Score)

		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read corrections: %w", err)
	}

	return corrections, nil
}

// PendingCorrections returns corrections without a vector of the model, after afterID, oldest first.
func (r *repo) PendingCorrections(ctx context.Context, model string, afterID int64, limit int) ([]pendingEmbedding, error) {
	const query = `
		SELECT id, comment, original
		FROM kb_corrections
		WHERE (embedding IS NULL OR embedding_model <> $1) AND id > $2
		ORDER BY id
		LIMIT $3`

	rows, err := r.db.GetReadConnection().Query(ctx, query, model, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query pending corrections: %w", err)
	}

	pending, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pendingEmbedding, error) {
		var (
			p                 pendingEmbedding
			comment, original string
		)

		err := row.Scan(&p.id, &comment, &original)
		p.text = CorrectionEmbeddingText(comment, original)

		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read pending corrections: %w", err)
	}

	return pending, nil
}

func (r *repo) SetCorrectionEmbeddings(ctx context.Context, model string, ids []int64, vectors []string) error {
	const query = `UPDATE kb_corrections SET embedding = $2::text::vector, embedding_model = $3 WHERE id = $1`

	batch := &pgx.Batch{}
	for i, id := range ids {
		batch.Queue(query, id, vectors[i], model)
	}

	if err := r.db.GetWriteConnection().SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("failed to update correction embeddings: %w", err)
	}

	return nil
}

// DocumentsWithOtherModel returns ids of documents whose chunks were embedded by another model.
func (r *repo) DocumentsWithOtherModel(ctx context.Context, model string) ([]int64, error) {
	rows, err := r.db.GetReadConnection().Query(ctx,
		`SELECT id FROM kb_documents WHERE embedding_model <> $1 ORDER BY id`, model)
	if err != nil {
		return nil, fmt.Errorf("failed to query documents to reembed: %w", err)
	}

	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("failed to read documents to reembed: %w", err)
	}

	return ids, nil
}

func (r *repo) DocumentChunks(ctx context.Context, documentID int64) ([]pendingEmbedding, error) {
	rows, err := r.db.GetReadConnection().Query(ctx,
		`SELECT id, content FROM kb_chunks WHERE document_id = $1 ORDER BY ord`, documentID)
	if err != nil {
		return nil, fmt.Errorf("failed to query chunks: %w", err)
	}

	chunks, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pendingEmbedding, error) {
		var p pendingEmbedding
		err := row.Scan(&p.id, &p.text)

		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read chunks: %w", err)
	}

	return chunks, nil
}

// SetDocumentEmbeddings replaces the vectors of the document's chunks and its model in one transaction.
func (r *repo) SetDocumentEmbeddings(
	ctx context.Context,
	documentID int64,
	model string,
	chunkIDs []int64,
	vectors []string,
) error {
	err := pgx.BeginFunc(ctx, r.db.GetWriteConnection(), func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for i, id := range chunkIDs {
			batch.Queue(`UPDATE kb_chunks SET embedding = $2::text::vector WHERE id = $1 AND document_id = $3`,
				id, vectors[i], documentID)
		}

		batch.Queue(`UPDATE kb_documents SET embedding_model = $2, updated_at = now() WHERE id = $1`, documentID, model)

		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return fmt.Errorf("failed to update chunk embeddings: %w", err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to reembed document: %w", err)
	}

	return nil
}

// CountStaleChunking counts documents split by another chunker version: they need `kb ingest` again,
// because the original text is not stored.
func (r *repo) CountStaleChunking(ctx context.Context, version int) (int, error) {
	var n int

	err := r.db.GetReadConnection().QueryRow(ctx,
		`SELECT count(*) FROM kb_documents WHERE chunker_version <> $1`, version).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("failed to count documents to re-ingest: %w", err)
	}

	return n, nil
}
