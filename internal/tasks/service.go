package tasks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/knowledge"
	"github.com/Prokud1n/ai-agents/internal/llmusage"
)

const (
	// sideEffectTimeout bounds the writes that finish a run (and the draft delivery after it): they are made
	// even when the worker's context is cancelled on stop, so a finished run is not lost.
	sideEffectTimeout = 5 * time.Second
	// deliveryLease holds a claimed draft delivery: another sender may take it over only after the lease.
	deliveryLease = 2 * time.Minute
	// sendDraftTimeout bounds one SendDraft call, well within the lease.
	sendDraftTimeout = 30 * time.Second
	// storeMessageAttempts: storing the id of a sent draft is retried, so a short database hiccup does not
	// make the draft look unsent (it would be sent again after the lease).
	storeMessageAttempts = 3
	storeMessagePause    = 200 * time.Millisecond
	// correctionTimeout bounds saving a correction to the knowledge base (embeddings included) in the
	// background after an approval.
	correctionTimeout = time.Minute
	// correctionTaskKind is the task kind of a correction from a task without a kind.
	correctionTaskKind = "task"
)

type enqueuer interface {
	Enqueue(ctx context.Context, taskID int64) error
	EnqueueAt(ctx context.Context, taskID int64, at time.Time) error
}

type correctionSaver interface {
	SaveCorrection(ctx context.Context, data *knowledge.SaveCorrectionData) (knowledge.Correction, error)
}

type usageSummarizer interface {
	UsageSummary(ctx context.Context, since time.Time) (llmusage.Summary, error)
}

// Service creates tasks, runs them and applies the trainer's decisions.
type Service struct {
	cfg         config.Tasks
	repo        *repo
	queue       enqueuer
	executor    Executor
	notifier    Notifier
	corrections correctionSaver
	usage       usageSummarizer
	log         *slog.Logger
	now         func() time.Time
	// background tracks work that outlives a request (saving corrections); see WaitBackground.
	background sync.WaitGroup
}

// NewService fails on an invalid TASKS_* config, so a typo stops the start.
func NewService(
	cfg *config.Tasks,
	r *repo,
	queue enqueuer,
	executor Executor,
	notifier Notifier,
	corrections correctionSaver,
	usage usageSummarizer,
	log *slog.Logger,
) (*Service, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	return &Service{
		cfg:         *cfg,
		repo:        r,
		queue:       queue,
		executor:    executor,
		notifier:    notifier,
		corrections: corrections,
		usage:       usage,
		log:         log,
		now:         time.Now,
	}, nil
}

func validateConfig(cfg *config.Tasks) error {
	switch {
	case cfg.Workers < 1:
		return fmt.Errorf("TASKS_WORKERS must be at least 1: %w", ErrValidation)
	case cfg.MaxAttempts < 1 || cfg.DeliveryAttempts < 1:
		return fmt.Errorf("TASKS_MAX_ATTEMPTS and TASKS_DELIVERY_ATTEMPTS must be at least 1: %w", ErrValidation)
	case cfg.RetryBaseDelay <= 0 || cfg.RetryMaxDelay < cfg.RetryBaseDelay:
		return fmt.Errorf("TASKS_RETRY_BASE_DELAY must be positive and not above TASKS_RETRY_MAX_DELAY: %w", ErrValidation)
	case cfg.Timeout <= 0 || cfg.ShutdownGrace <= 0 || cfg.SweepInterval <= 0:
		return fmt.Errorf("TASKS_TIMEOUT, TASKS_SHUTDOWN_GRACE and TASKS_SWEEP_INTERVAL must be positive: %w", ErrValidation)
	case strings.TrimSpace(cfg.Stream) == "":
		return fmt.Errorf("TASKS_STREAM is empty: %w", ErrValidation)
	}

	return nil
}

// staleAfter is when a run is considered dead (a live run ends within the timeout), and when a queued
// task is considered lost by the queue.
func (s *Service) staleAfter() time.Duration {
	return s.cfg.Timeout + claimGrace
}

// finalContext is for the writes that must happen even when ctx is cancelled (stop).
func finalContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), sideEffectTimeout)
}

// Create creates a queued task, or returns the task already created from the same source message
// (created = false). A task that could not be queued is picked up by the sweeper.
func (s *Service) Create(ctx context.Context, data *CreateTaskData) (task Task, created bool, err error) {
	if err := validateCreateData(data); err != nil {
		return Task{}, false, err
	}

	task, created, err = s.repo.Create(ctx, data, s.now())
	if err != nil || !created {
		return task, false, err
	}

	s.log.InfoContext(ctx, "tasks: created task", "task_id", task.ID, "agent", task.Agent, "kind", task.Kind)
	s.enqueue(ctx, task.ID)

	return task, true, nil
}

func validateCreateData(data *CreateTaskData) error {
	switch {
	case strings.TrimSpace(data.Agent) == "":
		return fmt.Errorf("task agent is empty: %w", ErrValidation)
	case strings.TrimSpace(data.Input) == "" && len(data.Media) == 0:
		return ErrEmptyTask
	case data.Source.ChatID == 0 || data.Source.MessageID == 0:
		return fmt.Errorf("task source message is not set: %w", ErrValidation)
	}

	return nil
}

// enqueue puts the task into the stream; a failure is logged: the sweeper finds the task in PostgreSQL.
func (s *Service) enqueue(ctx context.Context, taskID int64) {
	if err := s.queue.Enqueue(ctx, taskID); err != nil {
		s.log.ErrorContext(ctx, "tasks: failed to enqueue task, the sweeper will queue it", "error", err, "task_id", taskID)
	}
}

func (s *Service) Get(ctx context.Context, id int64) (Task, error) {
	return s.repo.Get(ctx, id)
}

func (s *Service) GetVersion(ctx context.Context, taskID int64, version int) (Version, error) {
	return s.repo.GetVersion(ctx, taskID, version)
}

// FindByDraftMessage finds the task and version of a draft message (for replies to drafts).
func (s *Service) FindByDraftMessage(ctx context.Context, chatID, messageID int64) (Task, Version, error) {
	if messageID <= 0 {
		return Task{}, Version{}, fmt.Errorf("draft message %d: %w", messageID, ErrNotFound)
	}

	taskID, version, err := s.repo.FindByDraftMessage(ctx, chatID, messageID)
	if errors.Is(err, ErrNotFound) {
		return Task{}, Version{}, s.notFoundDraft(ctx, chatID, messageID, err)
	}

	if err != nil {
		return Task{}, Version{}, err
	}

	task, err := s.repo.Get(ctx, taskID)
	if err != nil {
		return Task{}, Version{}, err
	}

	v, err := s.repo.GetVersion(ctx, taskID, version)
	if err != nil {
		return Task{}, Version{}, err
	}

	return task, v, nil
}

// notFoundDraft tells a message that was a draft once (replaced by a resent draft, or a duplicate) from
// an unknown one: the former is ErrStaleDraft.
func (s *Service) notFoundDraft(ctx context.Context, chatID, messageID int64, notFound error) error {
	old, err := s.repo.IsOldDraftMessage(ctx, chatID, messageID)
	if err != nil {
		return err
	}

	if old {
		return fmt.Errorf("message %d in chat %d is an old draft: %w", messageID, chatID, ErrStaleDraft)
	}

	return notFound
}

// Approve approves the draft version. A repeated press or a press on an older version changes nothing
// and returns ErrConflict or ErrStaleVersion (see UserMessage). An approved revision is saved to the
// knowledge base as the trainer's correction.
func (s *Service) Approve(ctx context.Context, data *ApproveTaskData) (Task, error) {
	task, err := s.repo.Update(ctx, data.TaskID, UpdateToApproved(data.Version, s.now()))
	if err != nil {
		return Task{}, err
	}

	s.log.InfoContext(ctx, "tasks: approved", "task_id", task.ID, "version", data.Version, "user_id", data.UserID)
	s.closeDraft(ctx, &task, data.Version, ApproveAction)

	if data.Version > 1 {
		s.saveCorrectionInBackground(ctx, &task, data.Version)
	}

	return task, nil
}

// saveCorrectionInBackground saves the approved revision without making the button press wait for
// embeddings; the work survives the request's context.
func (s *Service) saveCorrectionInBackground(ctx context.Context, task *Task, version int) {
	approved := *task
	bgCtx := context.WithoutCancel(ctx)

	s.background.Go(func() {
		ctx, cancel := context.WithTimeout(bgCtx, correctionTimeout)
		defer cancel()

		if err := s.saveCorrection(ctx, &approved, version); err != nil {
			s.log.ErrorContext(ctx, "tasks: failed to save correction to the knowledge base",
				"error", err, "task_id", approved.ID, "version", version)
		}
	})
}

// WaitBackground waits for background work (corrections being saved), at most until ctx is done.
func (s *Service) WaitBackground(ctx context.Context) error {
	select {
	case <-waitDone(&s.background):
		return nil
	case <-ctx.Done():
		return fmt.Errorf("failed to wait for background task work: %w", ctx.Err())
	}
}

// Reject rejects the draft version.
func (s *Service) Reject(ctx context.Context, data *RejectTaskData) (Task, error) {
	reason := strings.TrimSpace(data.Reason)

	task, err := s.repo.Update(ctx, data.TaskID, UpdateToRejected(data.Version, data.UserID, reason, s.now()))
	if err != nil {
		return Task{}, err
	}

	s.log.InfoContext(ctx, "tasks: rejected", "task_id", task.ID, "version", data.Version, "user_id", data.UserID)
	s.closeDraft(ctx, &task, data.Version, RejectAction)

	return task, nil
}

// RequestRevision stores the revision and queues the task in one transaction: the next run makes a new
// version from the previous one and the revision text.
func (s *Service) RequestRevision(ctx context.Context, data *RequestRevisionData) (Task, error) {
	text := strings.TrimSpace(data.Text)

	task, err := s.repo.Update(ctx, data.TaskID, UpdateToRevisionQueued(data.Version, data.UserID, text, s.now()))
	if err != nil {
		return Task{}, err
	}

	s.log.InfoContext(ctx, "tasks: revision requested", "task_id", task.ID, "version", data.Version, "user_id", data.UserID)
	s.enqueue(ctx, task.ID)
	s.closeDraft(ctx, &task, data.Version, ReviseAction)

	return task, nil
}

// RequestRevisionByReply is a revision sent as a reply to a draft message.
func (s *Service) RequestRevisionByReply(ctx context.Context, data *RequestRevisionByReplyData) (Task, error) {
	task, v, err := s.FindByDraftMessage(ctx, data.ChatID, data.ReplyToMessageID)
	if err != nil {
		return Task{}, err
	}

	if v.Version < task.Version {
		return Task{}, fmt.Errorf("task %d: reply to version %d, the latest is %d: %w",
			task.ID, v.Version, task.Version, ErrStaleDraft)
	}

	return s.RequestRevision(ctx, &RequestRevisionData{
		TaskID: task.ID, Version: v.Version, UserID: data.UserID, Text: data.Text,
	})
}

// closeDraft removes the buttons of the decided draft. Failures are only logged: the decision is stored.
func (s *Service) closeDraft(ctx context.Context, task *Task, version int, decision Action) {
	v, err := s.repo.GetVersion(ctx, task.ID, version)
	if err != nil {
		s.log.ErrorContext(ctx, "tasks: failed to get decided version", "error", err, "task_id", task.ID)

		return
	}

	if v.DraftMessageID == nil || *v.DraftMessageID <= 0 {
		return
	}

	err = s.notifier.CloseDraft(ctx, &CloseDraftData{
		TaskID: task.ID, Version: version, Agent: task.Agent, ChatID: task.Source.ChatID,
		MessageID: *v.DraftMessageID, Decision: decision,
	})
	if err != nil {
		s.log.ErrorContext(ctx, "tasks: failed to close draft", "error", err, "task_id", task.ID, "version", version)
	}
}

// saveCorrection stores an approved revision in the knowledge base: the version the trainer revised,
// the approved version made from it and the revision text. Only approved versions are saved, so the
// knowledge base gets no unconfirmed examples.
func (s *Service) saveCorrection(ctx context.Context, task *Task, version int) error {
	original, err := s.repo.GetVersion(ctx, task.ID, version-1)
	if err != nil {
		return err
	}

	approved, err := s.repo.GetVersion(ctx, task.ID, version)
	if err != nil {
		return err
	}

	revision, err := s.repo.LatestFeedback(ctx, task.ID, version-1, ReviseFeedbackKind)
	if err != nil {
		return err
	}

	_, err = s.corrections.SaveCorrection(ctx, &knowledge.SaveCorrectionData{
		Agent:     task.Agent,
		TaskKind:  correctionKind(task),
		Original:  original.Content,
		Corrected: approved.Content,
		Comment:   revision.Text,
	})
	if err != nil {
		return fmt.Errorf("failed to save correction: %w", err)
	}

	return nil
}

// correctionKind is the task kind of a correction, which requires one.
func correctionKind(task *Task) string {
	if task.Kind == "" {
		return correctionTaskKind
	}

	return task.Kind
}

// deliver sends the latest draft of the task if this caller claims its delivery (the worker that made it,
// or the sweeper for a draft that was not sent). A failed send is retried with backoff; after
// TASKS_DELIVERY_ATTEMPTS the task fails. Errors are logged: the sweeper retries.
//
// The guarantee is at least once: if the message was sent but its id could not be stored (or the send
// timed out after Telegram took it), the draft is sent again after the lease.
func (s *Service) deliver(ctx context.Context, taskID int64, version int) {
	claimCtx, cancel := finalContext(ctx)
	defer cancel()

	d, claimed, err := s.repo.ClaimDelivery(claimCtx, taskID, version, s.now(), deliveryLease)
	if err != nil {
		s.log.ErrorContext(claimCtx, "tasks: failed to claim draft delivery", "error", err, "task_id", taskID)

		return
	}

	if !claimed {
		return
	}

	if d.Attempt > s.cfg.DeliveryAttempts { // the last attempt's failure was not stored (the process stopped)
		s.failDelivery(claimCtx, &d, errors.New("delivery attempts exhausted"))

		return
	}

	messageID, err := s.sendDraft(ctx, &d)
	if err != nil {
		s.retryDelivery(claimCtx, &d, err)

		return
	}

	s.storeDraftMessage(ctx, &d, messageID)
}

func (s *Service) sendDraft(ctx context.Context, d *delivery) (int64, error) {
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sendDraftTimeout)
	defer cancel()

	messageID, err := s.notifier.SendDraft(sendCtx, &Draft{
		TaskID:           d.Task.ID,
		Version:          d.Version,
		Agent:            d.Task.Agent,
		ChatID:           d.Task.Source.ChatID,
		TopicID:          d.Task.Source.TopicID,
		ReplyToMessageID: d.Task.Source.MessageID,
		Text:             d.Content,
		Note:             d.Note,
		Buttons:          DraftButtons(d.Task.ID, d.Version),
	})
	if err != nil {
		return 0, fmt.Errorf("failed to send draft: %w", err)
	}

	return messageID, nil
}

// storeDraftMessage stores the id of the sent message, with retries. If another sender stored its
// message first, this message is a duplicate: its buttons are removed and it is remembered as old.
func (s *Service) storeDraftMessage(ctx context.Context, d *delivery, messageID int64) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), storeMessageAttempts*sideEffectTimeout)
	defer cancel()

	var (
		stored bool
		err    error
	)

	for attempt := range storeMessageAttempts {
		if attempt > 0 {
			sleep(ctx, storeMessagePause)
		}

		writeCtx, cancelWrite := finalContext(ctx)
		stored, err = s.repo.SetDraftMessageID(writeCtx, d.Task.ID, d.Version, messageID)

		cancelWrite()

		if err == nil {
			break
		}
	}

	switch {
	case err != nil:
		s.log.ErrorContext(ctx, "tasks: failed to store draft message id, the draft may be sent again",
			"error", err, "task_id", d.Task.ID, "version", d.Version, "message_id", messageID)
	case !stored && messageID > 0:
		s.closeDuplicate(ctx, d, messageID)
	}
}

func (s *Service) closeDuplicate(ctx context.Context, d *delivery, messageID int64) {
	s.log.WarnContext(ctx, "tasks: draft was sent twice, closing the duplicate",
		"task_id", d.Task.ID, "version", d.Version, "message_id", messageID)

	if err := s.repo.AddOldDraftMessage(ctx, d.Task.ID, d.Version, messageID); err != nil {
		s.log.ErrorContext(ctx, "tasks: failed to remember duplicate draft", "error", err, "task_id", d.Task.ID)
	}

	err := s.notifier.CloseDraft(ctx, &CloseDraftData{
		TaskID: d.Task.ID, Version: d.Version, Agent: d.Task.Agent, ChatID: d.Task.Source.ChatID,
		MessageID: messageID, Duplicate: true,
	})
	if err != nil {
		s.log.ErrorContext(ctx, "tasks: failed to close duplicate draft", "error", err, "task_id", d.Task.ID)
	}
}

func (s *Service) retryDelivery(ctx context.Context, d *delivery, sendErr error) {
	if d.Attempt >= s.cfg.DeliveryAttempts {
		s.failDelivery(ctx, d, sendErr)

		return
	}

	next := s.now().Add(Backoff(d.Attempt, s.cfg.RetryBaseDelay, s.cfg.RetryMaxDelay))

	s.log.WarnContext(ctx, "tasks: failed to send draft, will retry",
		"error", sendErr, "task_id", d.Task.ID, "version", d.Version, "attempt", d.Attempt, "next_attempt", next)

	if err := s.repo.DelayDelivery(ctx, d.Task.ID, d.Version, next); err != nil {
		s.log.ErrorContext(ctx, "tasks: failed to delay draft delivery", "error", err, "task_id", d.Task.ID)
	}
}

// failDelivery fails the task whose draft could not be delivered; the trainer is told if the notifier
// works for failures at all.
func (s *Service) failDelivery(ctx context.Context, d *delivery, sendErr error) {
	reason := fmt.Sprintf("failed to deliver draft version %d after %d attempts: %v", d.Version, d.Attempt, sendErr)

	task, err := s.repo.Update(ctx, d.Task.ID, UpdateToDeliveryFailed(d.Version, reason, s.now()))
	if err != nil {
		s.log.ErrorContext(ctx, "tasks: failed to fail undelivered task", "error", err, "task_id", d.Task.ID)

		return
	}

	s.log.ErrorContext(ctx, "tasks: task failed: draft could not be delivered",
		"error", sendErr, "task_id", task.ID, "version", d.Version, "attempts", d.Attempt)
	s.notifyFailure(ctx, &task, failedTaskMessage("Черновик не удалось отправить."))
}

func (s *Service) notifyFailure(ctx context.Context, task *Task, message string) {
	err := s.notifier.NotifyFailure(ctx, &Failure{
		TaskID:           task.ID,
		Agent:            task.Agent,
		ChatID:           task.Source.ChatID,
		TopicID:          task.Source.TopicID,
		ReplyToMessageID: task.Source.MessageID,
		Message:          message,
	})
	if err != nil {
		s.log.ErrorContext(ctx, "tasks: failed to notify about failed task", "error", err, "task_id", task.ID)
	}
}

// StatusReport returns tasks by status and LLM usage since the moment.
func (s *Service) StatusReport(ctx context.Context, since time.Time) (StatusReport, error) {
	counts, err := s.repo.CountByStatus(ctx)
	if err != nil {
		return StatusReport{}, err
	}

	usage, err := s.usage.UsageSummary(ctx, since)
	if err != nil {
		return StatusReport{}, fmt.Errorf("failed to get llm usage: %w", err)
	}

	return StatusReport{Since: since, Tasks: statusCounts(counts), Usage: usage}, nil
}

// statusCounts lists every status in the life cycle order, with zeros for missing ones.
func statusCounts(counts map[Status]int64) []StatusCount {
	result := make([]StatusCount, 0, len(Statuses))

	for _, status := range Statuses {
		result = append(result, StatusCount{Status: status, Count: counts[status]})
	}

	return result
}
