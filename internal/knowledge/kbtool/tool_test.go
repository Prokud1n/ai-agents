package kbtool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/knowledge"
	"github.com/Prokud1n/ai-agents/internal/providers/embeddings"
)

const (
	voiceSource    = "brand/voice.md"
	nutritionQuery = "как мы говорим о питании"
	validInput     = `{"query":"как мы говорим о питании"}`
	testTimeout    = 20 * time.Millisecond
)

var errDatabaseDown = errors.New("connection refused")

type fakeSearcher struct {
	mu      sync.Mutex
	results []knowledge.SearchResult
	err     error
	block   bool // wait for the context to end, like a hanging embeddings API
	calls   []knowledge.SearchChunksData
}

func (f *fakeSearcher) Search(ctx context.Context, data knowledge.SearchChunksData) ([]knowledge.SearchResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, data)
	f.mu.Unlock()

	if f.block {
		<-ctx.Done()

		return nil, fmt.Errorf("failed to embed search query: %w", ctx.Err())
	}

	return f.results, f.err
}

func (f *fakeSearcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.calls)
}

type fakeStatus bool

func (s fakeStatus) Configured() bool {
	return bool(s)
}

func voiceResult() knowledge.SearchResult {
	return knowledge.SearchResult{
		ChunkID: 1, Source: voiceSource, Title: "Голос бренда", Kind: knowledge.BrandKind,
		Content: "Питание\n\nГоворим о питании без запретов и чувства вины.", Score: 0.83,
	}
}

func TestModuleRegistersTool(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.DiscardHandler)

	var tools []agents.Tool

	app := fxtest.New(t,
		fx.NopLogger,
		fx.Supply(
			knowledge.NewService(nil, nil, log),
			embeddings.NewClient(&config.OpenAI{}, &config.Embeddings{}, log),
			log,
		),
		Module,
		fx.Invoke(fx.Annotate(func(group []agents.Tool) { tools = group }, fx.ParamTags(agents.ToolsGroup))),
	)
	app.RequireStart()
	app.RequireStop()

	require.Len(t, tools, 1)
	// agents/_example and agents' TestExampleAgent refer to the tool by this name.
	assert.Equal(t, "search_knowledge_base", tools[0].Name())
	assert.True(t, json.Valid(tools[0].InputSchema()))

	// Without OPENAI_API_KEY the tool stays registered and reports itself unavailable.
	tool, ok := tools[0].(*SearchTool)
	require.True(t, ok)
	require.ErrorIs(t, tool.Available(), embeddings.ErrNotConfigured)

	registry, err := agents.LoadRegistry(testAgentsDir, testSkillsDir, tools)
	require.NoError(t, err)

	writer, err := registry.Get(writerAgent)
	require.NoError(t, err)
	assert.Equal(t, []string{Name}, writer.Tools)
}

func TestInputSchemaMatchesLimits(t *testing.T) {
	t.Parallel()

	var schema struct {
		Required   []string `json:"required"`
		Properties struct {
			Limit struct {
				Minimum int `json:"minimum"`
				Maximum int `json:"maximum"`
			} `json:"limit"`
			Kinds struct {
				Items struct {
					Enum []knowledge.Kind `json:"enum"`
				} `json:"items"`
			} `json:"kinds"`
		} `json:"properties"`
	}

	require.NoError(t, json.Unmarshal([]byte(inputSchema), &schema))
	assert.Equal(t, []string{"query"}, schema.Required)
	assert.Equal(t, 1, schema.Properties.Limit.Minimum)
	assert.Equal(t, knowledge.MaxLimit, schema.Properties.Limit.Maximum)
	assert.Equal(t, knowledge.Kinds, schema.Properties.Kinds.Items.Enum)
	assert.Contains(t, description, "источник")
}

func TestParseInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected knowledge.SearchChunksData
		errText  string // non-empty: a ToolError containing it
	}{
		{
			name:     "default limit",
			input:    `{"query":"  питание  "}`,
			expected: knowledge.SearchChunksData{Query: "питание", Limit: knowledge.DefaultLimit, Kinds: []knowledge.Kind{}},
		},
		{
			name:  "limit and kinds",
			input: `{"query":"марафон","limit":20,"kinds":["product","post"]}`,
			expected: knowledge.SearchChunksData{
				Query: "марафон", Limit: knowledge.MaxLimit, Kinds: []knowledge.Kind{knowledge.ProductKind, knowledge.PostKind},
			},
		},
		{name: "broken json", input: `{"query":`, errText: "Аргументы должны быть объектом"},
		{name: "unknown field", input: `{"query":"a","top_k":3}`, errText: "top_k"},
		{name: "empty query", input: `{"query":"   "}`, errText: "query пустой"},
		{name: "no query", input: `{}`, errText: "query пустой"},
		{name: "long query", input: fmt.Sprintf(`{"query":%q}`, strings.Repeat("я", maxQueryRunes+1)), errText: "слишком длинный"},
		{name: "zero limit", input: `{"query":"a","limit":0}`, errText: "limit должен быть от 1 до 20"},
		{name: "limit above max", input: `{"query":"a","limit":21}`, errText: "получено 21"},
		{name: "negative limit", input: `{"query":"a","limit":-1}`, errText: "limit должен быть"},
		{name: "unknown kind", input: `{"query":"a","kinds":["recipe"]}`, errText: `"recipe"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data, err := parseInput(json.RawMessage(tt.input))
			if tt.errText == "" {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, data)

				return
			}

			var toolErr *agents.ToolError
			require.ErrorAs(t, err, &toolErr)
			assert.Contains(t, toolErr.Message, tt.errText)
		})
	}
}

func TestSearchToolExecute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		searcher   *fakeSearcher
		configured bool
		ctx        func() context.Context
		input      string
		expected   string // the result, or the ToolError message, contains it
		toolErr    bool
		plainErr   bool // an error that is not a ToolError
		searches   int
		logged     bool   // an error is logged
		logText    string // the log contains it
	}{
		{
			name: "found", searcher: &fakeSearcher{results: []knowledge.SearchResult{voiceResult()}}, configured: true,
			input: validInput, expected: `<fragment index="1" source="brand/voice.md"`, searches: 1,
		},
		{
			name: "nothing found is not an error", searcher: &fakeSearcher{}, configured: true,
			input: validInput, expected: "ничего не найдено по запросу «" + nutritionQuery + "»", searches: 1,
			logText: `"msg":"kbtool: nothing found","kinds":[],"query_runes":24`,
		},
		{
			name: "knowledge base not configured", searcher: &fakeSearcher{}, configured: false,
			input: validInput, expected: "База знаний сейчас недоступна", toolErr: true,
		},
		{
			name: "invalid arguments", searcher: &fakeSearcher{}, configured: true,
			input: `{"query":"a","limit":50}`, expected: "limit должен быть", toolErr: true,
		},
		{
			name: "validation error of the service", configured: true,
			searcher: &fakeSearcher{err: fmt.Errorf("search query is empty: %w", knowledge.ErrValidation)},
			input:    validInput, expected: "Неверные аргументы", toolErr: true, searches: 1,
		},
		{
			name: "database down", searcher: &fakeSearcher{err: errDatabaseDown}, configured: true,
			input: validInput, expected: internalErrorText, toolErr: true, searches: 1, logged: true,
		},
		{
			name: "embeddings key missing at call time", configured: true,
			searcher: &fakeSearcher{err: fmt.Errorf("failed to embed search query: %w", embeddings.ErrNotConfigured)},
			input:    validInput, expected: unavailableText, toolErr: true, searches: 1,
		},
		{
			name: "timeout", searcher: &fakeSearcher{block: true}, configured: true,
			input: validInput, expected: "не ответила за " + testTimeout.String(), toolErr: true, searches: 1, logged: true,
		},
		{
			name: "cancelled run", searcher: &fakeSearcher{block: true}, configured: true,
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()

				return ctx
			},
			input: validInput, plainErr: true, searches: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var logs bytes.Buffer

			tool := NewSearchTool(tt.searcher, fakeStatus(tt.configured), slog.New(slog.NewJSONHandler(&logs, nil)))
			tool.timeout = testTimeout

			ctx := context.Background()
			if tt.ctx != nil {
				ctx = tt.ctx()
			}

			got, err := tool.Execute(ctx, json.RawMessage(tt.input))

			var toolErr *agents.ToolError

			switch {
			case tt.toolErr:
				require.ErrorAs(t, err, &toolErr)
				assert.Contains(t, toolErr.Message, tt.expected)
			case tt.plainErr:
				require.Error(t, err)
				require.NotErrorAs(t, err, &toolErr)
			default:
				require.NoError(t, err)
				assert.Contains(t, got, tt.expected)
			}

			assert.Equal(t, tt.searches, tt.searcher.callCount())
			assert.Equal(t, tt.logged, strings.Contains(logs.String(), `"level":"ERROR"`))
			assert.Contains(t, logs.String(), tt.logText)
			assert.NotContains(t, logs.String(), nutritionQuery, "the query text is not logged")
		})
	}
}

// The agent loop with a scripted model: the tool result reaches the model, a bad call comes back with
// is_error and the run goes on; an agent without the tool in agent.md does not get it.
func TestAgentRunWithSearchTool(t *testing.T) {
	t.Parallel()

	searcher := &fakeSearcher{results: []knowledge.SearchResult{voiceResult()}}
	tool := NewSearchTool(searcher, fakeStatus(true), slog.New(slog.DiscardHandler))

	tests := []struct {
		name          string
		agent         string
		toolCalls     []string
		expectedTools []string
		expectedError []bool // is_error of every tool_result, in order
	}{
		{
			name:          "agent with the tool",
			agent:         writerAgent,
			toolCalls:     []string{validInput, `{"query":""}`},
			expectedTools: []string{Name},
			expectedError: []bool{false, true},
		},
		{name: "agent without the tool", agent: plainAgent, expectedTools: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			llm := &fakeLLM{toolCalls: tt.toolCalls}
			runtime := newTestRuntime(t, []agents.Tool{tool}, llm)

			result, err := runtime.Run(context.Background(), &agents.RunAgentData{Agent: tt.agent, Input: "Напиши пост про питание"})
			require.NoError(t, err)
			assert.Equal(t, finalAnswer, result.Text)

			requests := llm.sentRequests()
			require.Len(t, requests, len(tt.toolCalls)+1)
			assert.Equal(t, tt.expectedTools, toolNames(&requests[0]))

			results := toolResults(requests)
			require.Len(t, results, len(tt.expectedError))

			for i, isError := range tt.expectedError {
				assert.Equal(t, isError, results[i].IsError)
			}

			if len(results) > 0 {
				assert.Contains(t, results[0].Content, voiceSource)
				assert.Contains(t, results[0].Content, `kind="brand"`)
				assert.Contains(t, results[1].Content, "query пустой")
			}
		})
	}
}
