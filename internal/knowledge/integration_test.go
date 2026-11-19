package knowledge

import (
	"context"
	"errors"
	"hash/fnv"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/caarlos0/env/v11"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx/fxtest"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/migrator"
	"github.com/Prokud1n/ai-agents/internal/providers/embeddings"
	"github.com/Prokud1n/ai-agents/internal/providers/postgres"
)

// Integration tests need PostgreSQL with pgvector; they run when INTEGRATION=1 (`make test-integration`),
// with connection settings from POSTGRES_*. Each test creates its own database, applies all migrations
// to it and drops it at the end, so the data of the configured database is never touched.
// CI runs them in the compose-smoke job against the compose stack.
const (
	integrationEnv  = "INTEGRATION"
	nutritionQuery  = "как мы говорим о питании"
	reelsScriptTask = "reels-script"
	testModelA      = "test-hash-a"
	testModelB      = "test-hash-b"
)

var errEmbeddingsDown = errors.New("embeddings are down")

// hashEmbedder is a deterministic bag-of-words embedder: every word is hashed into one dimension,
// so texts sharing words are close by cosine. Good enough to check the SQL and the ranking.
type hashEmbedder struct {
	model string
}

func (e hashEmbedder) Model() string {
	return e.model
}

func (hashEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vectors := make([][]float32, 0, len(texts))

	for _, text := range texts {
		vec := make([]float32, embeddings.Dimensions)

		for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}) {
			h := fnv.New32a()
			_, _ = h.Write([]byte(word))
			vec[h.Sum32()%embeddings.Dimensions]++
		}

		var norm float64
		for _, x := range vec {
			norm += float64(x * x)
		}

		if norm > 0 {
			scale := float32(1 / math.Sqrt(norm))
			for i := range vec {
				vec[i] *= scale
			}
		}

		vectors = append(vectors, vec)
	}

	return vectors, nil
}

// failingEmbedder stands for an embeddings API that is down.
type failingEmbedder struct {
	model string
}

func (e failingEmbedder) Model() string {
	return e.model
}

func (failingEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, errEmbeddingsDown
}

// newIntegrationDB creates a fresh database with all migrations applied and returns a client for it.
func newIntegrationDB(t *testing.T) *postgres.Client {
	t.Helper()

	if os.Getenv(integrationEnv) != "1" {
		t.Skipf("set %s=1 to run against PostgreSQL with pgvector", integrationEnv)
	}

	var cfg config.Postgres
	require.NoError(t, env.Parse(&cfg))

	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	admin, err := pgx.Connect(ctx, cfg.DSN())
	require.NoError(t, err)

	name := "kb_it_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	ident := pgx.Identifier{name}.Sanitize()

	_, err = admin.Exec(ctx, "CREATE DATABASE "+ident)
	require.NoError(t, err)

	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)"); err != nil {
			t.Errorf("failed to drop test database %s: %v", name, err)
		}

		if err := admin.Close(context.Background()); err != nil {
			t.Errorf("failed to close admin connection: %v", err)
		}
	})

	testCfg := cfg
	testCfg.Database = name
	require.NoError(t, migrator.NewMigrator(&testCfg, log).Up(ctx))

	lc := fxtest.NewLifecycle(t)
	db, err := postgres.NewClient(lc, &testCfg, log)
	require.NoError(t, err)
	lc.RequireStart()
	t.Cleanup(lc.RequireStop)

	return db
}

func newIntegrationService(db *postgres.Client, e embedder) *Service {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	return NewService(newRepo(db), e, log)
}

func nutritionDoc() IngestDocumentData {
	return IngestDocumentData{
		Source: voiceSource,
		Kind:   BrandKind,
		Text: "# Голос бренда\n\n## Питание\n\nКак мы говорим о питании: без запретов и чувства вины, " +
			"на ты, с заботой.\n\n## Тренировки\n\nТри тренировки в неделю, техника важнее веса.",
	}
}

func TestIntegrationIngestAndSearch(t *testing.T) {
	t.Parallel()

	svc := newIntegrationService(newIntegrationDB(t), hashEmbedder{model: testModelA})
	ctx := context.Background()

	nutrition := nutritionDoc()
	products := IngestDocumentData{
		Source: "products.md",
		Kind:   ProductKind,
		Text:   "# Продукты\n\nМарафон 21 день: ежедневные тренировки дома и чат поддержки.",
	}

	created, err := svc.Ingest(ctx, nutrition)
	require.NoError(t, err)
	assert.Equal(t, CreatedIngestStatus, created.Status)
	assert.Equal(t, 2, created.Chunks)

	_, err = svc.Ingest(ctx, products)
	require.NoError(t, err)

	// Same text: nothing is re-embedded, only the title changes.
	nutrition.Title = "Голос"
	unchanged, err := svc.Ingest(ctx, nutrition)
	require.NoError(t, err)
	assert.Equal(t, UnchangedIngestStatus, unchanged.Status)
	assert.Equal(t, created.DocumentID, unchanged.DocumentID)
	assert.Equal(t, 2, unchanged.Chunks)

	// nil kinds: no filter.
	results, err := svc.Search(ctx, SearchChunksData{Query: nutritionQuery, Limit: 3, Kinds: nil})
	require.NoError(t, err)
	require.Len(t, results, 3)
	assert.Equal(t, nutrition.Source, results[0].Source)
	assert.Equal(t, "Голос", results[0].Title)
	assert.Contains(t, results[0].Content, "Голос бренда > Питание")

	for i := 1; i < len(results); i++ {
		assert.GreaterOrEqual(t, results[i-1].Score, results[i].Score, "results are sorted by score")
	}

	// The kind filter excludes the brand document; iterative scan still fills the result.
	onlyProducts, err := svc.Search(ctx, SearchChunksData{Query: nutritionQuery, Kinds: []Kind{ProductKind}})
	require.NoError(t, err)
	require.Len(t, onlyProducts, 1)
	assert.Equal(t, ProductKind, onlyProducts[0].Kind)

	toolText, err := svc.SearchTool(ctx, SearchToolInput{Query: nutritionQuery, Limit: 1})
	require.NoError(t, err)
	assert.Contains(t, toolText, "[1] "+nutrition.Source)

	// Changed text: chunks are replaced.
	nutrition.Text = "# Голос бренда\n\nКороткая версия: говорим о питании спокойно."
	updated, err := svc.Ingest(ctx, nutrition)
	require.NoError(t, err)
	assert.Equal(t, UpdatedIngestStatus, updated.Status)
	assert.Equal(t, created.DocumentID, updated.DocumentID)
	assert.Equal(t, 1, updated.Chunks)

	require.NoError(t, svc.Delete(ctx, products.Source))
	require.ErrorIs(t, svc.Delete(ctx, products.Source), ErrNotFound)
}

// Concurrent ingests of one source are serialized by the advisory lock: one creates the document,
// the others see the same version under the lock and change nothing.
func TestIntegrationConcurrentIngest(t *testing.T) {
	t.Parallel()

	svc := newIntegrationService(newIntegrationDB(t), hashEmbedder{model: testModelA})

	const workers = 5

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		statuses = map[IngestStatus]int{}
		errs     []error
	)

	for range workers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			res, err := svc.Ingest(context.Background(), nutritionDoc())

			mu.Lock()
			defer mu.Unlock()

			statuses[res.Status]++
			errs = append(errs, err)
		}()
	}

	wg.Wait()

	require.NoError(t, errors.Join(errs...))

	assert.Equal(t, map[IngestStatus]int{CreatedIngestStatus: 1, UnchangedIngestStatus: workers - 1}, statuses)

	doc, err := svc.repo.GetDocumentBySource(context.Background(), nutritionDoc().Source)
	require.NoError(t, err)
	assert.Equal(t, 2, doc.ChunkCount)
}

// Vectors of different models are not compared: search sees only the current model's documents,
// re-ingest or reembed moves a document to the new model.
func TestIntegrationModelChange(t *testing.T) {
	t.Parallel()

	db := newIntegrationDB(t)
	ctx := context.Background()
	oldModel := newIntegrationService(db, hashEmbedder{model: testModelA})
	newModel := newIntegrationService(db, hashEmbedder{model: testModelB})

	_, err := oldModel.Ingest(ctx, nutritionDoc())
	require.NoError(t, err)

	results, err := newModel.Search(ctx, SearchChunksData{Query: nutritionQuery})
	require.NoError(t, err)
	assert.Empty(t, results, "chunks of another model are not searched")

	res, err := newModel.Reembed(ctx)
	require.NoError(t, err)
	assert.Equal(t, ReembedResult{Documents: 1, Chunks: 2}, res)

	results, err = newModel.Search(ctx, SearchChunksData{Query: nutritionQuery})
	require.NoError(t, err)
	require.NotEmpty(t, results)
	assert.Equal(t, nutritionDoc().Source, results[0].Source)

	// The old model now has to re-ingest: the stored version is of another model.
	again, err := oldModel.Ingest(ctx, nutritionDoc())
	require.NoError(t, err)
	assert.Equal(t, UpdatedIngestStatus, again.Status)
}

func TestIntegrationCorrections(t *testing.T) {
	t.Parallel()

	svc := newIntegrationService(newIntegrationDB(t), hashEmbedder{model: testModelA})
	ctx := context.Background()

	saved, err := svc.SaveCorrection(ctx, &SaveCorrectionData{
		Agent:     testAgent,
		TaskKind:  reelsScriptTask,
		Original:  "Худеем к лету: убираем сладкое и хлеб!",
		Corrected: "Готовимся к лету с удовольствием: добавляем белок и овощи.",
		Comment:   "без запретов и диетной культуры",
	})
	require.NoError(t, err)
	assert.Positive(t, saved.ID)
	assert.False(t, saved.CreatedAt.IsZero())

	second, err := svc.SaveCorrection(ctx, &SaveCorrectionData{
		Agent:     testAgent,
		TaskKind:  reelsScriptTask,
		Original:  "Приседания каждый день по сто раз",
		Corrected: "Приседания три раза в неделю с отдыхом",
	})
	require.NoError(t, err)

	similar, err := svc.FindSimilarCorrections(ctx, FindSimilarCorrectionsData{
		Query:    "убираем сладкое к лету",
		Agent:    testAgent,
		TaskKind: reelsScriptTask,
		Limit:    2,
	})
	require.NoError(t, err)
	require.Len(t, similar, 2)
	assert.Equal(t, saved.ID, similar[0].ID)
	assert.Equal(t, "без запретов и диетной культуры", similar[0].Comment)

	recent, err := svc.FindSimilarCorrections(ctx, FindSimilarCorrectionsData{Agent: testAgent, Limit: 1})
	require.NoError(t, err)
	require.Len(t, recent, 1)
	assert.Equal(t, second.ID, recent[0].ID)

	other, err := svc.FindSimilarCorrections(ctx, FindSimilarCorrectionsData{Query: "сладкое", Agent: "designer"})
	require.NoError(t, err)
	assert.Empty(t, other)
}

// While embeddings are down a correction is still saved (without a vector) and found among recent
// ones; ingest fails and stores nothing. Reembed fills the vector in once embeddings are back.
func TestIntegrationEmbeddingsUnavailable(t *testing.T) {
	t.Parallel()

	db := newIntegrationDB(t)
	ctx := context.Background()
	down := newIntegrationService(db, failingEmbedder{model: testModelA})
	up := newIntegrationService(db, hashEmbedder{model: testModelA})

	saved, err := down.SaveCorrection(ctx, &SaveCorrectionData{
		Agent:     testAgent,
		TaskKind:  reelsScriptTask,
		Original:  "Худеем к лету!",
		Corrected: "Двигаемся с удовольствием",
	})
	require.NoError(t, err)

	fallback, err := down.FindSimilarCorrections(ctx, FindSimilarCorrectionsData{Query: "худеем", Agent: testAgent})
	require.NoError(t, err)
	require.Len(t, fallback, 1)
	assert.Equal(t, saved.ID, fallback[0].ID)

	similar, err := up.FindSimilarCorrections(ctx, FindSimilarCorrectionsData{Query: "худеем к лету", Agent: testAgent})
	require.NoError(t, err)
	assert.Empty(t, similar, "a correction without a vector is not found by similarity")

	_, err = down.Ingest(ctx, nutritionDoc())
	require.ErrorIs(t, err, errEmbeddingsDown)

	_, err = up.repo.GetDocumentBySource(ctx, nutritionDoc().Source)
	require.ErrorIs(t, err, ErrNotFound)

	res, err := up.Reembed(ctx)
	require.NoError(t, err)
	assert.Equal(t, ReembedResult{Corrections: 1}, res)

	similar, err = up.FindSimilarCorrections(ctx, FindSimilarCorrectionsData{Query: "худеем к лету", Agent: testAgent})
	require.NoError(t, err)
	require.Len(t, similar, 1)
	assert.Equal(t, saved.ID, similar[0].ID)
	assert.Positive(t, similar[0].Score)

	again, err := up.Reembed(ctx)
	require.NoError(t, err)
	assert.Equal(t, ReembedResult{}, again, "nothing left to reembed")
}
