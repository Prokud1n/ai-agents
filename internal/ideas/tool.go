package ideas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/Prokud1n/ai-agents/internal/agents"
)

// SubmitToolName is the tool the marketer answers an idea with.
const SubmitToolName = "submit_idea"

// maxAttempts of the model to submit a card that passes the checks: the first failure goes back to it with the
// reason, the second ends the run without a card.
const maxAttempts = 2

// Tool answers for the model.
const (
	acceptedAnswer = "Карточка принята."
	submittedText  = "Карточка уже принята."
	retryHint      = " Исправь и вызови submit_idea ещё раз — это последняя попытка."
	rejectedPrefix = "Карточка не прошла проверку: "
	exhaustedHint  = " Попытки исчерпаны."
	noSessionText  = "submit_idea работает только в задаче идеи (команда /idea)."
	badInputText   = "Вход не разобран: нужен JSON-объект с полями из схемы submit_idea."
)

type sessionKey struct{}

// Session is one card of the marketer: the model's attempts and the accepted card. It travels in the run's context,
// so the shared tool knows that the run makes an idea card.
type Session struct {
	mu       sync.Mutex
	attempts int
	card     *Card
	// failed: the attempts are exhausted without a valid card.
	failed bool
}

func NewSession() *Session {
	return &Session{}
}

// WithSession is the context of the run that makes the card of the session.
func WithSession(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

func sessionFrom(ctx context.Context) (*Session, bool) {
	s, ok := ctx.Value(sessionKey{}).(*Session)

	return s, ok && s != nil
}

// Card is the accepted card; false while there is none.
func (s *Session) Card() (Card, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.card == nil {
		return Card{}, false
	}

	return *s.card, true
}

// toolInput is the Go form of the tool schema.
type toolInput struct {
	Title      string `json:"title"`
	Summary    string `json:"summary"`
	Format     string `json:"format"`
	FormatNote string `json:"format_note"`
	Hook       string `json:"hook"`
}

// submit checks the model's input and records the card; an invalid input is a tool error with the reason. The second
// invalid input ends the run: its answer is not an error, so the final tool ends it.
func (s *Session) submit(raw json.RawMessage) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.card != nil || s.failed {
		return submittedText, nil
	}

	s.attempts++

	card, err := parseCard(raw)
	if err == nil {
		s.card = &card

		return acceptedAnswer, nil
	}

	var check *CheckError
	if !errors.As(err, &check) {
		return "", err
	}

	if s.attempts >= maxAttempts {
		s.failed = true

		return rejectedPrefix + check.Message + "." + exhaustedHint, nil
	}

	return "", agents.NewToolError(rejectedPrefix + check.Message + "." + retryHint)
}

func parseCard(raw json.RawMessage) (Card, error) {
	var in toolInput

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&in); err != nil {
		return Card{}, &CheckError{Message: badInputText}
	}

	return ValidateCard(&Card{
		Title: in.Title, Summary: in.Summary, Format: Format(in.Format), FormatNote: in.FormatNote, Hook: in.Hook,
	})
}

// SubmitTool is submit_idea: the marketer's card of an idea, checked and kept in the run's session (WithSession).
// Outside a session (another task of the marketer, cmd/agent-run) it answers that it works only for /idea. It is a
// final tool: once it answers without an error the run ends.
type SubmitTool struct{}

func NewSubmitTool() *SubmitTool {
	return &SubmitTool{}
}

func (*SubmitTool) Name() string {
	return SubmitToolName
}

func (*SubmitTool) Description() string {
	return "Записывает карточку идеи тренера для бэклога: заголовок, о чём ролик или пост, формат и хук. " +
		"Только для задачи (idea) — команды /idea. Вызывай ровно один раз; если карточка не прошла проверку, " +
		"исправь её по ответу и вызови ещё раз."
}

func (*SubmitTool) InputSchema() json.RawMessage {
	return json.RawMessage(inputSchema)
}

// EndsRun: the card is in the session, the model's next turn is not needed.
func (*SubmitTool) EndsRun() bool {
	return true
}

func (*SubmitTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	session, ok := sessionFrom(ctx)
	if !ok {
		return "", agents.NewToolError(noSessionText)
	}

	return session.submit(input)
}

// inputSchema of submit_idea; toolInput is its Go form.
const inputSchema = `{
  "type": "object",
  "properties": {
    "title": {
      "type": "string",
      "minLength": 1,
      "maxLength": 80,
      "description": "Короткий заголовок идеи: 2–8 слов, без кавычек, эмодзи и разметки."
    },
    "summary": {
      "type": "string",
      "minLength": 1,
      "maxLength": 600,
      "description": "О чём ролик или пост: 1–3 предложения по сути идеи тренера. Только то, что есть в идее и в базе знаний; факты, цифры и истории не выдумывай."
    },
    "format": {
      "type": "string",
      "enum": ["talk", "workout", "carousel", "other"],
      "description": "talk — разговорный рилс (тренер говорит в кадр); workout — рилс-тренировка (показ упражнений); carousel — карусель; other — другое (назови в format_note)."
    },
    "format_note": {
      "type": "string",
      "maxLength": 60,
      "description": "Только для other: какой формат, например «сторис с опросом». Для остальных не указывай."
    },
    "hook": {
      "type": "string",
      "minLength": 1,
      "maxLength": 200,
      "description": "Первая фраза ролика или первого слайда: живая, разговорная, такая, чтобы досмотрели."
    }
  },
  "required": ["title", "summary", "format", "hook"],
  "additionalProperties": false
}`
