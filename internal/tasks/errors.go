package tasks

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	ErrValidation = errors.New("validation error")
	ErrNotFound   = errors.New("not found")
	// ErrConflict is an action the task's status does not allow: an invalid transition, a decision on
	// a task already decided, or a run of a task that is not runnable.
	ErrConflict = errors.New("conflict")
	// ErrStaleVersion is a decision on a draft version that is not the latest one.
	ErrStaleVersion = errors.New("draft version is outdated")
	// ErrStaleDraft is a reply to a draft message that is not the latest draft of its task.
	ErrStaleDraft = fmt.Errorf("the reply is not to the latest draft: %w", ErrStaleVersion)
	// ErrDraftNotReady is a reply to a part of a draft that is still being sent (its last part, with the
	// buttons, is not stored yet).
	ErrDraftNotReady = errors.New("the draft is still being sent")

	// Validation errors by reason: each has its own message for the trainer (UserMessage).
	ErrEmptyRevision   = fmt.Errorf("revision text is empty: %w", ErrValidation)
	ErrUnknownVersion  = fmt.Errorf("no such draft version: %w", ErrValidation)
	ErrInvalidCallback = fmt.Errorf("invalid task callback data: %w", ErrValidation)
	ErrEmptyTask       = fmt.Errorf("task has neither text nor media: %w", ErrValidation)
	// ErrVideoRevision: a revision of a video draft by text is the next step (T3.4c).
	ErrVideoRevision = fmt.Errorf("a video draft cannot be revised by text yet: %w", ErrValidation)

	// errExecutorPanic marks a run whose executor panicked; it is retried like a temporary error.
	errExecutorPanic = errors.New("executor panicked")
)

// PermanentError is a run failure that retrying cannot fix (a refusal, an answer cut by max_tokens,
// a missing API key...): the run is given up at once. Message is shown to the trainer.
type PermanentError struct {
	Message string
	Err     error
}

func (e *PermanentError) Error() string {
	return fmt.Sprintf("%s: %v", e.Message, e.Err)
}

func (e *PermanentError) Unwrap() error {
	return e.Err
}

// DeferredError is a run that is not finished but goes on elsewhere (the media worker edits the video): the task
// goes back to the queue, runs again after After or when woken (Service.Wake), and the attempt is not counted.
type DeferredError struct {
	After time.Duration
}

func (e *DeferredError) Error() string {
	return fmt.Sprintf("run deferred for %s", e.After)
}

// TemporaryError is retried like any error; when the attempts run out, Message is shown to the trainer instead of
// the general text about an unavailable AI service.
type TemporaryError struct {
	Message string
	Err     error
}

func (e *TemporaryError) Error() string {
	return fmt.Sprintf("%s: %v", e.Message, e.Err)
}

func (e *TemporaryError) Unwrap() error {
	return e.Err
}

// UserMessage turns an error of an approval action into a short text for the trainer
// (for example, the answer to a button press).
func UserMessage(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrDraftNotReady):
		return "Черновик ещё отправляется — ответьте на него, когда придут кнопки."
	case errors.Is(err, ErrStaleDraft):
		return "Ответьте на последний черновик."
	case errors.Is(err, ErrStaleVersion):
		return "Это устаревшая версия черновика: действуйте по последней."
	case errors.Is(err, ErrNotFound):
		return "Задача не найдена."
	case errors.Is(err, ErrConflict):
		return "Задача уже обработана или сейчас в работе."
	case errors.Is(err, ErrEmptyRevision):
		return "Пустая правка: ответьте на черновик текстом правки."
	case errors.Is(err, ErrUnknownVersion):
		return "Такой версии черновика нет."
	case errors.Is(err, ErrInvalidCallback):
		return "Кнопка устарела или повреждена."
	case errors.Is(err, ErrEmptyTask):
		return "В задаче нет ни текста, ни вложений."
	case errors.Is(err, ErrVideoRevision):
		return VideoRevisionText
	case errors.Is(err, ErrValidation):
		return "Не хватает данных для задачи."
	default:
		return "Не получилось, попробуйте ещё раз."
	}
}

// failureReason is the trainer-facing reason of a given-up run, a sentence: the class of the error, never
// its text (the details go to the log and last_error).
func failureReason(err error, attempts int) string {
	var (
		permanent *PermanentError
		temporary *TemporaryError
	)

	switch {
	case errors.As(err, &permanent):
		return permanent.Message
	case errors.As(err, &temporary):
		return temporary.Message
	case errors.Is(err, context.DeadlineExceeded):
		return "Агент не уложился в отведённое время."
	case errors.Is(err, errExecutorPanic):
		return "Внутренняя ошибка при запуске агента."
	default:
		return fmt.Sprintf("Сервис ИИ временно недоступен: попыток было %d. Попробуйте позже.", attempts)
	}
}

// VideoRevisionText answers a revision of a video draft («Правки» or a reply to it): by text it is the next step.
const VideoRevisionText = "Правки текстом к видео появятся на следующем шаге. Пока можно отклонить черновик и " +
	"прислать исходник заново."

// failedTaskMessage tells the trainer that a task without a draft failed.
func failedTaskMessage(reason string) string {
	return "Не получилось выполнить задачу. " + reason
}

// failedRevisionNote is shown above the previous draft when its revision could not be applied.
func failedRevisionNote(reason string) string {
	return "Не получилось применить правку. " + reason + " Можно одобрить эту версию или дать правку ещё раз."
}
