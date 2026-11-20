package kbtool

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/knowledge"
	"github.com/Prokud1n/ai-agents/internal/pgtest"
	"github.com/Prokud1n/ai-agents/internal/providers/embeddings"
)

const (
	hashModel         = "test-hash"
	embeddingsTimeout = 5 * time.Second
	productsSource    = "product/marathon.md"
)

// hashVector is a deterministic bag-of-words embedding: every word is hashed into one dimension, so texts
// sharing words are close by cosine (the same idea as the knowledge integration tests).
func hashVector(text string) []float32 {
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

	return vec
}

type embeddingItem struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

// fakeEmbeddingsAPI answers like the OpenAI Embeddings API with hash vectors, so the real embeddings client
// and fx wiring are used.
func fakeEmbeddingsAPI(t *testing.T) string {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		data := make([]embeddingItem, 0, len(req.Input))
		for i, text := range req.Input {
			data = append(data, embeddingItem{Index: i, Embedding: hashVector(text)})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(server.Close)

	return server.URL
}

// TestIntegrationAgentSearchesKnowledgeBase runs the agent loop with a scripted model against a seeded
// database: the tool comes from the fx group, searches with the real knowledge service and returns
// fragments with sources and kinds; a bad call reaches the model as is_error and the run goes on.
func TestIntegrationAgentSearchesKnowledgeBase(t *testing.T) {
	t.Parallel()

	db := pgtest.Connect(t)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	var (
		svc   *knowledge.Service
		tools []agents.Tool
	)

	app := fxtest.New(t,
		fx.NopLogger,
		fx.Supply(
			db,
			&config.OpenAI{APIKey: "test", BaseURL: fakeEmbeddingsAPI(t)},
			&config.Embeddings{Model: hashModel, BatchSize: 100, Timeout: embeddingsTimeout},
			log,
		),
		embeddings.Module,
		knowledge.Module,
		Module,
		fx.Populate(&svc),
		fx.Invoke(fx.Annotate(func(group []agents.Tool) { tools = group }, fx.ParamTags(agents.ToolsGroup))),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)

	ctx := context.Background()

	_, err := svc.Ingest(ctx, knowledge.IngestDocumentData{
		Source: voiceSource,
		Title:  "Голос бренда",
		Kind:   knowledge.BrandKind,
		Text: "# Голос бренда\n\n## Питание\n\nКак мы говорим о питании: без запретов и чувства вины, на ты, " +
			"с заботой.\n\n## Тренировки\n\nТри тренировки в неделю, техника важнее веса.",
	})
	require.NoError(t, err)

	_, err = svc.Ingest(ctx, knowledge.IngestDocumentData{
		Source: productsSource,
		Kind:   knowledge.ProductKind,
		Text:   "# Марафон\n\nМарафон 21 день: питание без запретов, тренировки дома и чат поддержки.",
	})
	require.NoError(t, err)

	llm := &fakeLLM{toolCalls: []string{
		`{"query":"как мы говорим о питании","limit":2,"kinds":["brand"]}`,
		`{"query":"марафон питание","kinds":["recipe"]}`,
		`{"query":"марафон 21 день","kinds":["product"]}`,
	}}
	runtime := newTestRuntime(t, tools, llm)

	result, err := runtime.Run(ctx, &agents.RunAgentData{Agent: writerAgent, Input: "Напиши пост про питание"})
	require.NoError(t, err)
	assert.Equal(t, finalAnswer, result.Text)

	requests := llm.sentRequests()
	require.Len(t, requests, len(llm.toolCalls)+1)
	assert.Equal(t, []string{Name}, toolNames(&requests[0]))

	results := toolResults(requests)
	require.Len(t, results, len(llm.toolCalls))

	brand := results[0]
	assert.False(t, brand.IsError)
	assert.Contains(t, brand.Content, "Найдено фрагментов: 2.")
	assert.Contains(t, brand.Content, `<fragment index="1" source="`+voiceSource+`" title="Голос бренда" kind="brand"`)
	assert.Contains(t, brand.Content, "Голос бренда > Питание")
	assert.NotContains(t, brand.Content, productsSource, "the kinds filter excludes products")

	assert.True(t, results[1].IsError)
	assert.Contains(t, results[1].Content, `"recipe"`)

	product := results[2]
	assert.False(t, product.IsError)
	assert.Contains(t, product.Content, `<fragment index="1" source="`+productsSource+`"`)
	assert.Contains(t, product.Content, `kind="product"`)
}
