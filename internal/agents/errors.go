package agents

import "errors"

var (
	// ErrInvalidAgent wraps every problem found while loading agents/: the start fails with its text.
	ErrInvalidAgent = errors.New("invalid agent definition")
	// ErrAgentNotFound is returned for an unknown agent name.
	ErrAgentNotFound = errors.New("agent not found")
	// ErrIterationLimit is returned when the model keeps calling tools after maxToolIterations turns.
	ErrIterationLimit = errors.New("tool use iteration limit reached")
	// ErrInvalidRunData is returned for a run without input.
	ErrInvalidRunData = errors.New("invalid run data")
)
