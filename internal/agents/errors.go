package agents

import "errors"

var (
	// ErrInvalidAgent wraps every problem found while loading agents/: the start fails with its text.
	ErrInvalidAgent = errors.New("invalid agent definition")
	// ErrAgentNotFound is returned for an unknown agent name.
	ErrAgentNotFound = errors.New("agent not found")
	// ErrIterationLimit is returned when the model keeps calling tools after maxToolIterations turns.
	ErrIterationLimit = errors.New("tool use iteration limit reached")
	// ErrInvalidRunData is returned for a run without input, or with a history ending with an assistant
	// message and no new input.
	ErrInvalidRunData = errors.New("invalid run data")
	// ErrMaxTokens is returned with a partial Result when the answer was cut by max_tokens
	// (or by the context window).
	ErrMaxTokens = errors.New("answer was cut by the max_tokens limit")
	// ErrRefusal is returned with a partial Result when the model refused to answer.
	ErrRefusal = errors.New("model refused to answer")
	// ErrInvalidResponse is returned when the model response breaks the protocol, e.g. stop_reason
	// tool_use without tool_use blocks, or an unknown stop_reason.
	ErrInvalidResponse = errors.New("invalid model response")
)

// ToolError is an error whose message is meant for the model. A tool returns it when the model can fix
// the call (a wrong argument and so on); any other error reaches the model as a short generic message,
// and its details go only to the log.
type ToolError struct {
	Message string
}

func NewToolError(message string) *ToolError {
	return &ToolError{Message: message}
}

func (e *ToolError) Error() string {
	return e.Message
}
