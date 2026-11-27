package editplan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

// maxAttempts of the model to submit an edit that passes the checks: the first failure goes back to it with the
// reason, the second ends the decision (InvalidOutcome).
const maxAttempts = 2

// Tool answers for the model.
const (
	acceptedAnswer = "Принято."
	decidedAnswer  = "Решение уже принято."
	retryHint      = " Исправь и вызови submit_edit ещё раз — это последняя попытка. Если не понял, что менять, " +
		"ответь decision unclear."
	exhaustedHint = " Попытки исчерпаны."
	noSessionText = "submit_edit работает только в задаче монтажа."
	badInputText  = "Вход не разобран: нужен JSON-объект с полями из схемы submit_edit."
)

type sessionKey struct{}

// Session is one decision of the editor: the request, the model's attempts and the outcome. It travels in the run's
// context, so the shared tool (SubmitTool) knows which video it decides on.
type Session struct {
	req *Request

	mu       sync.Mutex
	attempts int
	outcome  *Outcome
	// endingHinted: the hint about the ending of a selection was given (once; the same decision again is accepted).
	endingHinted bool
}

func NewSession(req *Request) *Session {
	return &Session{req: req}
}

// WithSession is the context of the run that decides in the session.
func WithSession(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

func sessionFrom(ctx context.Context) (*Session, bool) {
	s, ok := ctx.Value(sessionKey{}).(*Session)

	return s, ok && s != nil
}

// Outcome is the decision made in the session; false while the model has not decided.
func (s *Session) Outcome() (*Outcome, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.outcome, s.outcome != nil
}

// submit checks the model's input and records the decision; an invalid input is a tool error with the reason. The
// second invalid input ends the decision: its answer is not an error, so the run ends (SubmitTool is final).
func (s *Session) submit(raw json.RawMessage) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.outcome != nil {
		return decidedAnswer, nil
	}

	s.attempts++

	var in Input

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	outcome, err := s.apply(decoder, &in)
	if err == nil {
		if outcome.endingHint != "" && !s.endingHinted && s.attempts < maxAttempts {
			// A hint, not a failed check: it does not take an attempt.
			s.endingHinted = true
			s.attempts--

			return "", agents.NewToolError("Проверь " + outcome.endingHint + "." + endingRetryHint)
		}

		s.outcome = outcome

		return acceptedAnswer, nil
	}

	var check *CheckError
	if !errors.As(err, &check) {
		return "", err
	}

	if s.attempts >= maxAttempts {
		// The hook title that passed its checks is kept: the first version gets it with the automatic cut.
		s.outcome = &Outcome{Kind: InvalidOutcome, Reason: check.Message, Edit: roughcut.Edit{Hook: checkedHook(s.req, &in)}}

		return "План не прошёл проверку: " + check.Message + "." + exhaustedHint, nil
	}

	return "", agents.NewToolError("План не прошёл проверку: " + check.Message + "." + retryHint)
}

func (s *Session) apply(decoder *json.Decoder, in *Input) (*Outcome, error) {
	if err := decoder.Decode(in); err != nil {
		return nil, &CheckError{Message: badInputText}
	}

	return Apply(s.req, in)
}
