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
	"unicode/utf8"

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
	manyFragments  = 20
	longFragment   = 2000
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

func fragments(count, runes int) []knowledge.SearchResult {
	results := make([]knowledge.SearchResult, 0, count)
	for i := range count {
		results = append(results, knowledge.SearchResult{
			Source:  fmt.Sprintf("post/%02d.md", i),
			Title:   "Пост",
			Kind:    knowledge.PostKind,
			Content: strings.Repeat("ж", runes),
			Score:   0.5,
		})
	}

	return results
}

func TestTruncateResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		input         []knowledge.SearchResult
		expectedRunes int // max runes of one fragment after cutting
		truncated     bool
	}{
		{name: "nothing", input: nil},
		{name: "short fragment kept", input: fragments(1, longFragment), expectedRunes: longFragment},
		{name: "huge fragment cut", input: fragments(1, maxOutputRunes*2), expectedRunes: maxFragmentRunes, truncated: true},
		{
			name:          "many fragments share the budget",
			input:         fragments(manyFragments, longFragment),
			expectedRunes: maxOutputRunes / manyFragments,
			truncated:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			original := make([]string, 0, len(tt.input))
			for i := range tt.input {
				original = append(original, tt.input[i].Content)
			}

			cut, truncated := truncateResults(tt.input)
			assert.Equal(t, tt.truncated, truncated)
			require.Len(t, cut, len(tt.input))

			total := 0

			for i := range cut {
				runes := utf8.RuneCountInString(cut[i].Content)
				total += runes

				assert.LessOrEqual(t, runes, tt.expectedRunes)
				assert.Equal(t, tt.input[i].Source, cut[i].Source)
				assert.Equal(t, original[i], tt.input[i].Content, "the input is not modified")

				if tt.truncated {
					assert.True(t, strings.HasSuffix(cut[i].Content, truncatedMark))
				}
			}

			assert.LessOrEqual(t, total, maxOutputRunes)
		})
	}
}

func TestFormatResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       []knowledge.SearchResult
		expected    []string
		notExpected []string
	}{
		{
			name:  "source and kind of every fragment",
			input: []knowledge.SearchResult{voiceResult()},
			expected: []string{
				"Найдено фрагментов: 1.",
				"[1] brand/voice.md — «Голос бренда» (brand, score 0.83)\nПитание",
			},
			notExpected: []string{truncatedFooter},
		},
		{
			name:     "cut fragments are reported",
			input:    fragments(manyFragments, longFragment),
			expected: []string{"Найдено фрагментов: 20.", "[20] post/19.md", "(post, score 0.50)", truncatedMark, truncatedFooter},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := FormatResults(tt.input)

			for _, s := range tt.expected {
				assert.Contains(t, got, s)
			}

			for _, s := range tt.notExpected {
				assert.NotContains(t, got, s)
			}
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
		logged     bool
	}{
		{
			name: "found", searcher: &fakeSearcher{results: []knowledge.SearchResult{voiceResult()}}, configured: true,
			input: validInput, expected: "[1] brand/voice.md", searches: 1,
		},
		{
			name: "nothing found is not an error", searcher: &fakeSearcher{}, configured: true,
			input: validInput, expected: "ничего не найдено по запросу «" + nutritionQuery + "»", searches: 1,
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
				assert.Contains(t, results[0].Content, "(brand,")
				assert.Contains(t, results[1].Content, "query пустой")
			}
		})
	}
}
