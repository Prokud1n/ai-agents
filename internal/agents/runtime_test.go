package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

const (
	checkText = "Let me check."
	textBlock = `{"type":"text","text":"Let me check."}`
	userInput = "Hello"
)

func response(stopReason anthropic.StopReason, content string) fakeResponse {
	return fakeResponse{status: http.StatusOK, body: fmt.Sprintf(
		`{"id":"msg","model":%q,"role":"assistant","stop_reason":%q,"content":[%s],"usage":%s}`,
		testModel, stopReason, content, testUsage)}
}

func finalResponse() fakeResponse {
	return response(anthropic.EndTurnStopReason, fmt.Sprintf(`{"type":"text","text":%q}`, finalAnswer))
}

func toolUseBlock(id, tool, input string) string {
	return fmt.Sprintf(`{"type":"tool_use","id":%q,"name":%q,"input":%s}`, id, tool, input)
}

func toolUseResponse(tool, input string) fakeResponse {
	return response(anthropic.ToolUseStopReason, textBlock+","+toolUseBlock(toolUseID, tool, input))
}

func errorResponse(status int) fakeResponse {
	return fakeResponse{status: status, body: `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`}
}

func newTestRuntime(t *testing.T, api *fakeAPI) (*Runtime, *fakeRecorder) {
	t.Helper()

	server := httptest.NewServer(api)
	t.Cleanup(server.Close)

	registry := loadTestRegistry(t)

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
			name:               "load_skill adds the agent's override to a shared skill",
			responses:          []fakeResponse{toolUseResponse(LoadSkillToolName, `{"name":"tone"}`), finalResponse()},
			requests:           2,
			calls:              2,
			expectedToolResult: toneBody + paragraphBreak + overrideHeading + paragraphBreak + coachOverride,
		},
		{
			name:               "unknown skill is a tool error",
			responses:          []fakeResponse{toolUseResponse(LoadSkillToolName, `{"name":"`+ghostSkill+`"}`), finalResponse()},
			requests:           2,
			calls:              2,
			expectedToolResult: `unknown skill "ghost", available skills: plan, tone`,
			expectedToolError:  true,
		},
		{
			name:               "tool error goes to the model with is_error and the run continues",
			responses:          []fakeResponse{toolUseResponse(failToolName, `{}`), finalResponse()},
			requests:           2,
			calls:              2,
			expectedToolResult: `tool "fail" failed with an internal error`,
			expectedToolError:  true,
		},
		{
			name:               "tool not allowed to the agent is an error",
			responses:          []fakeResponse{toolUseResponse(secretToolName, `{}`), finalResponse()},
			requests:           2,
			calls:              2,
			expectedToolResult: `tool "secret" is not available to this agent`,
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

			result, err := runtime.Run(context.Background(), &RunAgentData{Agent: coachAgent, Input: userInput})
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
			assert.Equal(t, testModel, recorder.records[0].ResponseModel)
			assert.NotEmpty(t, recorder.records[0].RequestID)

			requests := api.sentRequests()
			require.Len(t, requests, tt.requests)

			if tt.expectedToolResult == "" {
				return
			}

			toolResult := lastToolResult(t, &requests[len(requests)-1])
			assert.Equal(t, toolUseID, toolResult.ToolUseID)
			assert.Equal(t, tt.expectedToolResult, toolResult.Content)
			assert.NotContains(t, toolResult.Content, errToolFailed.Error(), "error details stay in the log")
			assert.Equal(t, tt.expectedToolError, toolResult.IsError)
		})
	}
}

// TestRuntimeRequest checks what is sent to the model: system prompt, tools and the assistant turn sent back.
func TestRuntimeRequest(t *testing.T) {
	t.Parallel()

	api := &fakeAPI{responses: []fakeResponse{toolUseResponse(echoToolName, `{"text":"hi"}`), finalResponse()}}
	runtime, _ := newTestRuntime(t, api)

	_, err := runtime.Run(context.Background(), &RunAgentData{Agent: coachAgent, Input: userInput})
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
	assert.NotContains(t, first.System[0].Text, coachOverride)
	assert.Contains(t, first.System[0].Text, SafetyRules)

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
			name: "history ends with an assistant message and no input (a prefill)",
			data: &RunAgentData{Agent: coachAgent, Messages: []anthropic.Message{
				{Role: anthropic.UserRole, Content: []anthropic.ContentBlock{anthropic.TextBlock(userInput)}},
				{Role: anthropic.AssistantRole, Content: []anthropic.ContentBlock{anthropic.TextBlock(checkText)}},
			}},
			responses: []fakeResponse{finalResponse()},
			expected:  ErrInvalidRunData,
		},
		{
			name:      "tool_use stop reason without tool_use blocks",
			data:      &RunAgentData{Agent: coachAgent, Input: userInput},
			responses: []fakeResponse{response(anthropic.ToolUseStopReason, textBlock)},
			expected:  ErrInvalidResponse,
			requests:  1,
		},
		{
			name:      "unknown agent",
			data:      &RunAgentData{Agent: "ghost", Input: userInput},
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
			data:      &RunAgentData{Agent: coachAgent, Input: userInput},
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
	require.Len(t, requests[0].Tools, 1, "plain has skills but no tools: only load_skill")
	assert.Equal(t, LoadSkillToolName, requests[0].Tools[0].Name)
	assert.True(t, strings.HasPrefix(requests[0].System[0].Text, "You are a plain agent."+paragraphBreak+SafetyRules))
}

// TestRuntimeRunStopReasons: a cut or refused answer returns an error with the partial result, and its
// history is safe for the next run: no tool_use is left without a tool_result.
func TestRuntimeRunStopReasons(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		responses []fakeResponse
		expected  error
		// expectedText is the partial answer; expectedMessages is the length of the returned history.
		expectedText     string
		expectedMessages int
		requests         int
	}{
		{
			name: "max_tokens with a cut tool_use drops the tool_use",
			responses: []fakeResponse{
				response(anthropic.MaxTokensStopReason, textBlock+","+toolUseBlock(toolUseID, echoToolName, `{}`)),
			},
			expected:         ErrMaxTokens,
			expectedText:     checkText,
			expectedMessages: 2,
			requests:         1,
		},
		{
			name:             "max_tokens with only a tool_use drops the whole turn",
			responses:        []fakeResponse{response(anthropic.MaxTokensStopReason, toolUseBlock(toolUseID, echoToolName, `{}`))},
			expected:         ErrMaxTokens,
			expectedMessages: 1,
			requests:         1,
		},
		{
			name:             "context window exceeded is reported as max_tokens",
			responses:        []fakeResponse{response(anthropic.ContextWindowStopReason, textBlock)},
			expected:         ErrMaxTokens,
			expectedText:     checkText,
			expectedMessages: 2,
			requests:         1,
		},
		{
			name:             "refusal",
			responses:        []fakeResponse{response(anthropic.RefusalStopReason, `{"type":"text","text":"I can't help."}`)},
			expected:         ErrRefusal,
			expectedText:     "I can't help.",
			expectedMessages: 2,
			requests:         1,
		},
		{
			name:             "unknown stop reason",
			responses:        []fakeResponse{response(anthropic.StopReason("something_new"), textBlock)},
			expected:         ErrInvalidResponse,
			expectedText:     checkText,
			expectedMessages: 2,
			requests:         1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := &fakeAPI{responses: tt.responses}
			runtime, recorder := newTestRuntime(t, api)

			result, err := runtime.Run(context.Background(), &RunAgentData{Agent: coachAgent, Input: userInput})

			require.ErrorIs(t, err, tt.expected)
			require.NotNil(t, result)
			assert.Equal(t, tt.expectedText, result.Text)
			assert.Equal(t, 1, result.Calls)
			require.Len(t, result.Messages, tt.expectedMessages)
			assert.Len(t, recorder.records, 1, "a cut answer is billed and recorded")
			assert.Len(t, api.sentRequests(), tt.requests)

			for _, msg := range result.Messages {
				for _, block := range msg.Content {
					assert.NotEqual(t, anthropic.ToolUseContentType, block.Type)
				}
			}
		})
	}
}

func TestRuntimeRunPauseTurn(t *testing.T) {
	t.Parallel()

	api := &fakeAPI{responses: []fakeResponse{response(anthropic.PauseTurnStopReason, textBlock), finalResponse()}}
	runtime, _ := newTestRuntime(t, api)

	result, err := runtime.Run(context.Background(), &RunAgentData{Agent: coachAgent, Input: userInput})
	require.NoError(t, err)

	assert.Equal(t, finalAnswer, result.Text)
	assert.Equal(t, 2, result.Calls)

	requests := api.sentRequests()
	require.Len(t, requests, 2)
	// The paused turn is sent back as is, with no user message after it.
	second := requests[1]
	require.Len(t, second.Messages, 2)
	assert.Equal(t, anthropic.AssistantRole, second.Messages[1].Role)
	assert.Equal(t, checkText, second.Messages[1].Content[0].Text)
}

func TestRuntimeRunTwoToolCalls(t *testing.T) {
	t.Parallel()

	api := &fakeAPI{responses: []fakeResponse{
		response(anthropic.ToolUseStopReason, toolUseBlock("toolu_a", echoToolName, `{"text":"one"}`)+","+
			toolUseBlock("toolu_b", failToolName, `{}`)),
		finalResponse(),
	}}
	runtime, _ := newTestRuntime(t, api)

	_, err := runtime.Run(context.Background(), &RunAgentData{Agent: coachAgent, Input: userInput})
	require.NoError(t, err)

	requests := api.sentRequests()
	require.Len(t, requests, 2)

	last := requests[1].Messages[2]
	require.Equal(t, anthropic.UserRole, last.Role)
	require.Len(t, last.Content, 2)
	assert.Equal(t, "toolu_a", last.Content[0].ToolUseID)
	assert.Equal(t, "echo: one", last.Content[0].Content)
	assert.False(t, last.Content[0].IsError)
	assert.Equal(t, "toolu_b", last.Content[1].ToolUseID)
	assert.True(t, last.Content[1].IsError)
}

// cancellingCreator cancels the run's context right after a successful call, as a client disconnect would.
type cancellingCreator struct {
	cancel context.CancelFunc
}

func (c *cancellingCreator) CreateMessage(
	context.Context,
	*anthropic.MessageRequest,
) (*anthropic.MessageResponse, error) {
	c.cancel()

	return &anthropic.MessageResponse{
		Model:      testModel,
		StopReason: anthropic.EndTurnStopReason,
		Content:    []anthropic.ContentBlock{anthropic.TextBlock(finalAnswer)},
		Usage:      anthropic.Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

type ctxRecorder struct {
	ctxErr      error
	hasDeadline bool
}

func (r *ctxRecorder) Record(ctx context.Context, _ *llmusage.RecordUsageData) error {
	r.ctxErr = ctx.Err()
	_, r.hasDeadline = ctx.Deadline()

	return nil
}

func TestRuntimeRecordsUsageAfterCancel(t *testing.T) {
	t.Parallel()

	registry := loadTestRegistry(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recorder := &ctxRecorder{}
	runtime := NewRuntime(registry, &cancellingCreator{cancel: cancel}, recorder,
		&config.LLM{DefaultModel: testModel, DefaultMaxTokens: 1024}, slog.New(slog.DiscardHandler))

	_, err := runtime.Run(ctx, &RunAgentData{Agent: plainAgent, Input: userInput})
	require.NoError(t, err)

	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.NoError(t, recorder.ctxErr, "usage is recorded with a context that is not cancelled")
	assert.True(t, recorder.hasDeadline, "usage recording has its own timeout")
}

// ErrIterationLimit comes with a partial result: the history up to the last tool results and the tokens.
func TestRuntimeRunIterationLimit(t *testing.T) {
	t.Parallel()

	api := &fakeAPI{responses: []fakeResponse{toolUseResponse(echoToolName, `{"text":"again"}`)}}
	runtime, recorder := newTestRuntime(t, api)

	result, err := runtime.Run(context.Background(), &RunAgentData{Agent: coachAgent, Input: userInput})

	require.ErrorIs(t, err, ErrIterationLimit)
	require.NotNil(t, result)
	assert.Equal(t, maxToolIterations, result.Calls)
	assert.Equal(t, checkText, result.Text)
	assert.Equal(t, anthropic.ToolUseStopReason, result.StopReason)
	assert.Equal(t, int64(10*maxToolIterations), result.Tokens.Input)
	assert.Len(t, recorder.records, maxToolIterations)
	assert.Len(t, api.sentRequests(), maxToolIterations)

	// user input, then an assistant tool_use and a user tool_result per turn: every tool_use is answered.
	require.Len(t, result.Messages, 1+2*maxToolIterations)
	last := result.Messages[len(result.Messages)-1]
	assert.Equal(t, anthropic.UserRole, last.Role)
	require.Len(t, last.Content, 1)
	assert.Equal(t, anthropic.ToolResultContentType, last.Content[0].Type)
}
