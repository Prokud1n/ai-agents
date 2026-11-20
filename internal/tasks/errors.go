package tasks

import (
	"context"
	"errors"
	"fmt"
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

	// Validation errors by reason: each has its own message for the trainer (UserMessage).
	ErrEmptyRevision   = fmt.Errorf("revision text is empty: %w", ErrValidation)
	ErrUnknownVersion  = fmt.Errorf("no such draft version: %w", ErrValidation)
	ErrInvalidCallback = fmt.Errorf("invalid task callback data: %w", ErrValidation)
	ErrEmptyTask       = fmt.Errorf("task has neither text nor media: %w", ErrValidation)

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

// UserMessage turns an error of an approval action into a short text for the trainer
// (for example, the answer to a button press).
func UserMessage(err error) string {
	switch {
	case err == nil:
		return ""
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
	case errors.Is(err, ErrValidation):
		return "Не хватает данных для задачи."
	default:
		return "Не получилось, попробуйте ещё раз."
	}
}

// failureReason is the trainer-facing reason of a given-up run, a sentence: the class of the error, never
// its text (the details go to the log and last_error).
func failureReason(err error, attempts int) string {
	var permanent *PermanentError

	switch {
	case errors.As(err, &permanent):
		return permanent.Message
	case errors.Is(err, context.DeadlineExceeded):
		return "Агент не уложился в отведённое время."
	case errors.Is(err, errExecutorPanic):
		return "Внутренняя ошибка при запуске агента."
	default:
		return fmt.Sprintf("Сервис ИИ временно недоступен: попыток было %d. Попробуйте позже.", attempts)
	}
}

// failedTaskMessage tells the trainer that a task without a draft failed.
func failedTaskMessage(reason string) string {
	return "Не получилось выполнить задачу. " + reason
}

// failedRevisionNote is shown above the previous draft when its revision could not be applied.
func failedRevisionNote(reason string) string {
	return "Не получилось применить правку. " + reason + " Можно одобрить эту версию или дать правку ещё раз."
}
