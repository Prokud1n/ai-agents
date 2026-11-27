package editplan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

// maxAttempts of the model to submit an edit that passes the checks: the first failure goes back to it with the
// reason, the second ends the decision (InvalidOutcome).
const maxAttempts = 2

// hintKind is a soft check of a valid decision whose hint goes back to the model once (it does not take an attempt).
type hintKind int

const (
	endingHintKind hintKind = iota
	tailHintKind
	countHintKind
	fragmentHintKind
	hintKinds
)

// maxSubmits bounds the calls of submit_edit in a session: the attempts and every hint once. No hint is given past it,
// so the decision is accepted (or refused) and the run ends.
const maxSubmits = maxAttempts + int(hintKinds)

// Tool answers for the model.
const (
	acceptedAnswer = "Принято."
	decidedAnswer  = "Решение уже принято."
	retryHint      = " Исправь и вызови submit_edit ещё раз — это последняя попытка. Если не понял, что менять, " +
		"ответь decision unclear."
	exhaustedHint = " Попытки исчерпаны."
	// previousAcceptedFormat: the correction after a hint failed the last attempt, the decision before it stands.
	previousAcceptedFormat = "Принято прежнее решение: исправление не прошло проверку (%s)."
	noSessionText          = "submit_edit работает только в задаче монтажа."
	badInputText           = "Вход не разобран: нужен JSON-объект с полями из схемы submit_edit."
)

type sessionKey struct{}

// Session is one decision of the editor: the request, the model's attempts and the outcome. It travels in the run's
// context, so the shared tool (SubmitTool) knows which video it decides on.
type Session struct {
	req *Request

	mu       sync.Mutex
	attempts int
	submits  int
	outcome  *Outcome
	// hinted: the hint of the kind was given (once; the same decision again is accepted).
	hinted [hintKinds]bool
	// hintedOutcome is the last valid decision a hint was given on: when the model's correction fails the last
	// attempt, it is the decision (the lines of the hints are in its details).
	hintedOutcome *Outcome
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
	s.submits++

	var in Input

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	outcome, err := s.apply(decoder, &in)
	if err == nil {
		if hint := s.hint(outcome); hint != "" {
			// A hint, not a failed check: it does not take an attempt.
			s.attempts--
			s.hintedOutcome = outcome

			return "", agents.NewToolError(hint)
		}

		s.outcome = outcome

		return acceptedAnswer, nil
	}

	var check *CheckError
	if !errors.As(err, &check) {
		return "", err
	}

	if s.attempts >= maxAttempts {
		return s.lastAttempt(&in, check), nil
	}

	return "", agents.NewToolError("План не прошёл проверку: " + check.Message + "." + retryHint)
}

// lastAttempt ends the decision after the last attempt failed the check: accepted without the notes the reel would not
// show when they are all it failed by (withoutHidden), else the decision a hint was given on, else refused
// (InvalidOutcome; the hook title that passed its checks is kept: the first version gets it with the automatic cut).
// The answer goes to the model, the run ends.
func (s *Session) lastAttempt(in *Input, check *CheckError) string {
	reason := check.Message

	if check.hiddenNotes {
		accepted, failure := s.withoutHidden(in)
		if accepted != nil {
			s.outcome = accepted

			return hiddenAcceptedText
		}

		if failure != "" {
			reason = failure
		}
	}

	if s.hintedOutcome != nil {
		s.outcome = s.hintedOutcome

		return fmt.Sprintf(previousAcceptedFormat, reason)
	}

	s.outcome = &Outcome{Kind: InvalidOutcome, Reason: reason, Edit: roughcut.Edit{Hook: checkedHook(s.req, in)}}

	return "План не прошёл проверку: " + reason + "." + exhaustedHint
}

// hint is the text of the hints of the outcome not given yet (each once), marked as given; "" without one, or past
// maxSubmits. A hint is given on the last attempt too: it does not take one. Each hint ends with its own way to keep
// the decision; two hints at once share one at the end.
func (s *Session) hint(outcome *Outcome) string {
	if s.submits >= maxSubmits {
		return ""
	}

	pending := [hintKinds]struct{ text, retry string }{
		endingHintKind:   {outcome.endingHint, endingRetryHint},
		tailHintKind:     {outcome.tailHint, softRetryHint},
		countHintKind:    {outcome.countHint, softRetryHint},
		fragmentHintKind: {outcome.fragmentHint, softRetryHint},
	}

	var parts, retries []string

	for kind, h := range pending {
		if h.text == "" || s.hinted[kind] {
			continue
		}

		s.hinted[kind] = true
		parts, retries = append(parts, "Проверь "+h.text+"."), append(retries, h.retry)
	}

	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0] + retries[0]
	default:
		return strings.Join(parts, " ") + softRetryHint
	}
}

// withoutHidden is the decision of the last attempt accepted with the notes the reel would not show put or dropped,
// when they are all it failed by (T3.29): the trainer gets the reel selected, not the automatic cut. Otherwise nil and
// the check it fails then (the reason of the refusal).
func (s *Session) withoutHidden(in *Input) (*Outcome, string) {
	in.dropHidden = true

	outcome, err := Apply(s.req, in)

	var check *CheckError

	switch {
	case errors.As(err, &check):
		return nil, check.Message
	case err != nil:
		return nil, err.Error()
	case outcome.Kind != EditOutcome:
		return nil, ""
	default:
		return outcome, ""
	}
}

func (s *Session) apply(decoder *json.Decoder, in *Input) (*Outcome, error) {
	if err := decoder.Decode(in); err != nil {
		return nil, &CheckError{Message: badInputText}
	}

	return Apply(s.req, in)
}
