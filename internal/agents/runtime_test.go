package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/llmusage"
	"github.com/Prokud1n/ai-agents/internal/providers/anthropic"
)

const (
	testModel   = "claude-default-test"
	finalAnswer = "Done."
	toolUseID   = "toolu_1"
)

type fakeResponse struct {
	status int
	body   string
}

// fakeAPI replays scripted responses (the last one repeats) and keeps the decoded requests.
type fakeAPI struct {
	mu        sync.Mutex
	responses []fakeResponse
	requests  []anthropic.MessageRequest
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	body, _ := io.ReadAll(r.Body)

	var req anthropic.MessageRequest
	if err := json.Unmarshal(body, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	f.requests = append(f.requests, req)

	resp := f.responses[min(len(f.requests), len(f.responses))-1]

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("request-id", fmt.Sprintf("req_%d", len(f.requests)))
	w.WriteHeader(resp.status)
	_, _ = io.WriteString(w, resp.body)
}

func (f *fakeAPI) sentRequests() []anthropic.MessageRequest {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]anthropic.MessageRequest(nil), f.requests...)
}

type fakeRecorder struct {
	mu      sync.Mutex
	records []llmusage.RecordUsageData
}

func (f *fakeRecorder) Record(_ context.Context, data *llmusage.RecordUsageData) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.records = append(f.records, *data)

	return nil
}

const testUsage = `{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":2,"cache_creation_input_tokens":1}`

func finalResponse() fakeResponse {
	return fakeResponse{status: http.StatusOK, body: fmt.Sprintf(
		`{"id":"msg","model":%q,"role":"assistant","stop_reason":"end_turn",`+
			`"content":[{"type":"text","text":%q}],"usage":%s}`, testModel, finalAnswer, testUsage)}
}

func toolUseResponse(tool, input string) fakeResponse {
	return fakeResponse{status: http.StatusOK, body: fmt.Sprintf(
		`{"id":"msg","model":%q,"role":"assistant","stop_reason":"tool_use",`+
			`"content":[{"type":"text","text":"Let me check."},`+
			`{"type":"tool_use","id":%q,"name":%q,"input":%s}],"usage":%s}`, testModel, toolUseID, tool, input, testUsage)}
}

func errorResponse(status int) fakeResponse {
	return fakeResponse{status: status, body: `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`}
}

func newTestRuntime(t *testing.T, api *fakeAPI) (*Runtime, *fakeRecorder) {
	t.Helper()

	server := httptest.NewServer(api)
	t.Cleanup(server.Close)

	registry, err := LoadRegistry(validAgentsDir, testTools())
	require.NoError(t, err)

	log := slog.New(slog.DiscardHandler)
	client := anthropic.NewClient(&config.Anthropic{
		APIKey:         "test",
		BaseURL:        server.URL,
		Timeout:        5 * time.Second,
		MaxRetries:     2,
		RetryBaseDelay: time.Millisecond,
		RetryMaxDelay:  5 * time.Millisecond,
	}, log)
	recorder := &fakeRecorder{}

	return NewRuntime(registry, client, recorder, &config.LLM{DefaultModel: testModel, DefaultMaxTokens: 1024}, log),
		recorder
}

// lastToolResult returns the single tool_result block of the last message of a request.
func lastToolResult(t *testing.T, req *anthropic.MessageRequest) anthropic.ContentBlock {
	t.Helper()

	last := req.Messages[len(req.Messages)-1]
	require.Equal(t, anthropic.UserRole, last.Role)
	require.Len(t, last.Content, 1)
	require.Equal(t, anthropic.ToolResultContentType, last.Content[0].Type)

	return last.Content[0]
}

func TestRuntimeRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		responses []fakeResponse
		// requests is the number of HTTP requests expected; calls is the number of successful API calls.
		requests int
		calls    int
		// expectedToolResult is a substring of the tool_result sent in the last request (empty: no tool call).
		expectedToolResult string
		expectedToolError  bool
	}{
		{
			name:      "final answer without tools",
			responses: []fakeResponse{finalResponse()},
			requests:  1,
			calls:     1,
		},
		{
			name:               "tool call result goes back to the model",
			responses:          []fakeResponse{toolUseResponse(echoToolName, `{"text":"hi"}`), finalResponse()},
			requests:           2,
			calls:              2,
			expectedToolResult: "echo: hi",
		},
		{
			name:               "load_skill returns the skill body",
			responses:          []fakeResponse{toolUseResponse(LoadSkillToolName, `{"name":"plan"}`), finalResponse()},
			requests:           2,
			calls:              2,
			expectedToolResult: "PLAN BODY: three workouts a week.",
		},
		{
			name:               "unknown skill is a tool error",
			responses:          []fakeResponse{toolUseResponse(LoadSkillToolName, `{"name":"ghost"}`), finalResponse()},
			requests:           2,
			calls:              2,
			expectedToolResult: `"ghost"`,
			expectedToolError:  true,
		},
		{
			name:               "tool error goes to the model with is_error and the run continues",
			responses:          []fakeResponse{toolUseResponse(failToolName, `{}`), finalResponse()},
			requests:           2,
			calls:              2,
			expectedToolResult: errToolFailed.Error(),
			expectedToolError:  true,
		},
		{
			name:               "tool not allowed to the agent is an error",
			responses:          []fakeResponse{toolUseResponse(secretToolName, `{}`), finalResponse()},
			requests:           2,
			calls:              2,
			expectedToolResult: "not available to this agent",
			expectedToolError:  true,
		},
		{
			name:      "429 is retried",
			responses: []fakeResponse{errorResponse(http.StatusTooManyRequests), finalResponse()},
			requests:  2,
			calls:     1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := &fakeAPI{responses: tt.responses}
			runtime, recorder := newTestRuntime(t, api)

			result, err := runtime.Run(context.Background(), &RunAgentData{Agent: coachAgent, Input: "Hello"})
			require.NoError(t, err)

			assert.Equal(t, finalAnswer, result.Text)
			assert.Equal(t, anthropic.EndTurnStopReason, result.StopReason)
			assert.Equal(t, testModel, result.Model)
			assert.Equal(t, tt.calls, result.Calls)
			assert.Equal(t, llmusage.Tokens{
				Input: int64(10 * tt.calls), Output: int64(5 * tt.calls),
				CacheRead: int64(2 * tt.calls), CacheWrite: int64(tt.calls),
			}, result.Tokens)
			assert.Len(t, result.Messages, 2*tt.calls)

			require.Len(t, recorder.records, tt.calls)
			assert.Equal(t, coachAgent, recorder.records[0].Agent)
			assert.Equal(t, testModel, recorder.records[0].Model)
			assert.NotEmpty(t, recorder.records[0].RequestID)

			requests := api.sentRequests()
			require.Len(t, requests, tt.requests)

			if tt.expectedToolResult == "" {
				return
			}

			toolResult := lastToolResult(t, &requests[len(requests)-1])
			assert.Equal(t, toolUseID, toolResult.ToolUseID)
			assert.Contains(t, toolResult.Content, tt.expectedToolResult)
			assert.Equal(t, tt.expectedToolError, toolResult.IsError)
		})
	}
}

// TestRuntimeRequest checks what is sent to the model: system prompt, tools and the assistant turn sent back.
func TestRuntimeRequest(t *testing.T) {
	t.Parallel()

	api := &fakeAPI{responses: []fakeResponse{toolUseResponse(echoToolName, `{"text":"hi"}`), finalResponse()}}
	runtime, _ := newTestRuntime(t, api)

	_, err := runtime.Run(context.Background(), &RunAgentData{Agent: coachAgent, Input: "Hello"})
	require.NoError(t, err)

	requests := api.sentRequests()
	require.Len(t, requests, 2)

	first := requests[0]
	assert.Equal(t, testModel, first.Model)
	assert.Equal(t, 1024, first.MaxTokens)
	require.Len(t, first.System, 1)
	assert.Contains(t, first.System[0].Text, "You are a test coach.")
	assert.Contains(t, first.System[0].Text, "- `plan`: Plans a week of workouts.")
	assert.NotContains(t, first.System[0].Text, "PLAN BODY")

	names := make([]string, 0, len(first.Tools))
	for _, tool := range first.Tools {
		names = append(names, tool.Name)
	}

	assert.Equal(t, []string{LoadSkillToolName, echoToolName, failToolName}, names)

	// The assistant turn goes back as received, including the tool_use block.
	second := requests[1]
	require.Len(t, second.Messages, 3)

	assistant := second.Messages[1]
	assert.Equal(t, anthropic.AssistantRole, assistant.Role)
	require.Len(t, assistant.Content, 2)
	assert.Equal(t, toolUseID, assistant.Content[1].ID)
	assert.JSONEq(t, `{"text":"hi"}`, string(assistant.Content[1].Input))
}

func TestRuntimeRunErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		data      *RunAgentData
		responses []fakeResponse
		expected  error
		requests  int
	}{
		{
			name:      "iteration limit",
			data:      &RunAgentData{Agent: coachAgent, Input: "Loop"},
			responses: []fakeResponse{toolUseResponse(echoToolName, `{"text":"again"}`)},
			expected:  ErrIterationLimit,
			requests:  maxToolIterations,
		},
		{
			name:      "unknown agent",
			data:      &RunAgentData{Agent: "ghost", Input: "Hi"},
			responses: []fakeResponse{finalResponse()},
			expected:  ErrAgentNotFound,
		},
		{
			name:      "empty input",
			data:      &RunAgentData{Agent: coachAgent},
			responses: []fakeResponse{finalResponse()},
			expected:  ErrInvalidRunData,
		},
		{
			name:      "retries are exhausted",
			data:      &RunAgentData{Agent: coachAgent, Input: "Hi"},
			responses: []fakeResponse{errorResponse(http.StatusServiceUnavailable)},
			expected:  &anthropic.APIError{},
			requests:  3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := &fakeAPI{responses: tt.responses}
			runtime, _ := newTestRuntime(t, api)

			result, err := runtime.Run(context.Background(), tt.data)

			if apiErr, ok := tt.expected.(*anthropic.APIError); ok {
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
			} else {
				require.ErrorIs(t, err, tt.expected)
			}

			assert.Nil(t, result)
			assert.Len(t, api.sentRequests(), tt.requests)
		})
	}
}

func TestRuntimeRunKeepsHistory(t *testing.T) {
	t.Parallel()

	api := &fakeAPI{responses: []fakeResponse{finalResponse()}}
	runtime, _ := newTestRuntime(t, api)

	history := []anthropic.Message{
		{Role: anthropic.UserRole, Content: []anthropic.ContentBlock{anthropic.TextBlock("First")}},
		{Role: anthropic.AssistantRole, Content: []anthropic.ContentBlock{anthropic.TextBlock("Answer")}},
	}

	result, err := runtime.Run(context.Background(), &RunAgentData{
		Agent: plainAgent, Messages: history, Input: "Second", Model: "claude-override", MaxTokens: 77,
	})
	require.NoError(t, err)

	assert.Len(t, history, 2, "caller history must not be modified")
	require.Len(t, result.Messages, 4)
	assert.Equal(t, "Second", result.Messages[2].Content[0].Text)

	requests := api.sentRequests()
	require.Len(t, requests, 1)
	assert.Equal(t, "claude-override", requests[0].Model)
	assert.Equal(t, 77, requests[0].MaxTokens)
	assert.Empty(t, requests[0].Tools)
	assert.Equal(t, "You are a plain agent.", requests[0].System[0].Text)
}
