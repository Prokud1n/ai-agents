package tasks

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/knowledge"
	"github.com/Prokud1n/ai-agents/internal/llmusage"
	"github.com/Prokud1n/ai-agents/internal/providers/postgres"
)

var errTemporary = errors.New("claude api is overloaded")

const (
	testAgent    = "copywriter"
	testVersion1 = "Версия один"
	testVersion2 = "Версия два"
	staleRunID   = "old"
)

func testConfig() config.Tasks {
	return config.Tasks{
		Workers:          2,
		MaxAttempts:      3,
		RetryBaseDelay:   30 * time.Second,
		RetryMaxDelay:    10 * time.Minute,
		Timeout:          15 * time.Minute,
		ShutdownGrace:    10 * time.Second,
		SweepInterval:    time.Minute,
		DeliveryAttempts: 3,
		Stream:           "tasks",
	}
}

// fakeExecutor answers with the queued results in order (the last one repeats) and records prompts.
type fakeExecutor struct {
	mu      sync.Mutex
	results []fakeResult
	prompts []string
	hook    func() // called on every run, e.g. to cancel the worker's context mid-run
}

type fakeResult struct {
	text      string
	err       error
	panicWith string // the executor panics with it
}

func (e *fakeExecutor) Execute(_ context.Context, data *ExecuteData) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.prompts = append(e.prompts, data.Prompt)

	if e.hook != nil {
		e.hook()
	}

	if len(e.results) == 0 {
		return "draft", nil
	}

	r := e.results[0]
	if len(e.results) > 1 {
		e.results = e.results[1:]
	}

	if r.panicWith != "" {
		panic(r.panicWith)
	}

	return r.text, r.err
}

func (e *fakeExecutor) SetResults(hook func(), results ...fakeResult) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.hook = hook
	e.results = results
}

func (e *fakeExecutor) Prompts() []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	return append([]string(nil), e.prompts...)
}

// fakeNotifier records what was sent; message ids start at 1000.
type fakeNotifier struct {
	mu        sync.Mutex
	drafts    []Draft
	closed    []CloseDraftData
	failures  []Failure
	sendErr   error
	sendCalls int
	onSend    func() // called on every successful send, e.g. to let another sender win the race
	// parts: every draft is sent in two parts; the first part's ID is the draft's ID + partIDOffset.
	parts bool
}

const partIDOffset = 500

func (n *fakeNotifier) SendDraft(_ context.Context, draft *Draft) (SentDraft, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.sendCalls++

	if n.sendErr != nil {
		return SentDraft{}, n.sendErr
	}

	n.drafts = append(n.drafts, *draft)

	if n.onSend != nil {
		n.onSend()
	}

	id := int64(1000 + len(n.drafts))

	sent := SentDraft{MessageID: id}
	if n.parts {
		sent.PartIDs = []int64{id + partIDOffset}
	}

	return sent, nil
}

func (n *fakeNotifier) CloseDraft(_ context.Context, data *CloseDraftData) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.closed = append(n.closed, *data)

	return nil
}

func (n *fakeNotifier) NotifyFailure(_ context.Context, failure *Failure) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.failures = append(n.failures, *failure)

	return nil
}

func (n *fakeNotifier) Drafts() []Draft {
	n.mu.Lock()
	defer n.mu.Unlock()

	return append([]Draft(nil), n.drafts...)
}

func (n *fakeNotifier) Closed() []CloseDraftData {
	n.mu.Lock()
	defer n.mu.Unlock()

	return append([]CloseDraftData(nil), n.closed...)
}

func (n *fakeNotifier) Failures() []Failure {
	n.mu.Lock()
	defer n.mu.Unlock()

	return append([]Failure(nil), n.failures...)
}

func (n *fakeNotifier) OnSend(hook func()) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.onSend = hook
}

func (n *fakeNotifier) SendCalls() int {
	n.mu.Lock()
	defer n.mu.Unlock()

	return n.sendCalls
}

func (n *fakeNotifier) SetSendErr(err error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.sendErr = err
}

// memQueue records enqueued and scheduled task ids.
type memQueue struct {
	mu        sync.Mutex
	enqueued  []int64
	scheduled map[int64]time.Time
	schedules map[int64]int // EnqueueAt calls by task
	err       error
}

func (q *memQueue) Enqueue(_ context.Context, taskID int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.err != nil {
		return q.err
	}

	q.enqueued = append(q.enqueued, taskID)

	return nil
}

func (q *memQueue) EnqueueAt(_ context.Context, taskID int64, at time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.err != nil {
		return q.err
	}

	if q.scheduled == nil {
		q.scheduled = make(map[int64]time.Time)
		q.schedules = make(map[int64]int)
	}

	q.scheduled[taskID] = at
	q.schedules[taskID]++

	return nil
}

func (q *memQueue) Enqueued() []int64 {
	q.mu.Lock()
	defer q.mu.Unlock()

	return append([]int64(nil), q.enqueued...)
}

func (q *memQueue) Scheduled(taskID int64) (time.Time, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	at, ok := q.scheduled[taskID]

	return at, ok
}

func (q *memQueue) Schedules(taskID int64) int {
	q.mu.Lock()
	defer q.mu.Unlock()

	return q.schedules[taskID]
}

func (q *memQueue) SetErr(err error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.err = err
}

type fakeCorrections struct {
	mu    sync.Mutex
	saved []knowledge.SaveCorrectionData
}

func (c *fakeCorrections) SaveCorrection(_ context.Context, data *knowledge.SaveCorrectionData) (knowledge.Correction, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.saved = append(c.saved, *data)

	return knowledge.Correction{ID: int64(len(c.saved))}, nil
}

func (c *fakeCorrections) Saved() []knowledge.SaveCorrectionData {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]knowledge.SaveCorrectionData(nil), c.saved...)
}

type fakeUsage struct {
	summary llmusage.Summary
}

func (u *fakeUsage) UsageSummary(_ context.Context, since time.Time) (llmusage.Summary, error) {
	s := u.summary
	s.Since = since

	return s, nil
}

// clock is a settable time source for the service.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// testEnv is a service over a real database with fakes around it.
type testEnv struct {
	service     *Service
	executor    *fakeExecutor
	notifier    *fakeNotifier
	queue       *memQueue
	corrections *fakeCorrections
	clock       *clock
}

func newTestEnv(t *testing.T, db *postgres.Client, results ...fakeResult) *testEnv {
	t.Helper()

	cfg := testConfig()
	env := &testEnv{
		executor:    &fakeExecutor{results: results},
		notifier:    &fakeNotifier{},
		queue:       &memQueue{},
		corrections: &fakeCorrections{},
		clock:       &clock{now: testNow},
	}

	service, err := NewService(&cfg, newRepo(db), env.queue, env.executor, env.notifier, env.corrections,
		&fakeUsage{summary: llmusage.Summary{Total: llmusage.SummaryRow{Calls: 4, CostNano: 1_500_000_000}}},
		slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	service.now = env.clock.Now
	env.service = service

	return env
}

// waitBackground waits for corrections being saved after an approval.
func (env *testEnv) waitBackground(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, env.service.WaitBackground(ctx))
}

func testTaskData(messageID int64) *CreateTaskData {
	return &CreateTaskData{
		Agent:    testAgent,
		Kind:     "reels-script",
		Input:    "Сценарий про утреннюю зарядку",
		Media:    []string{"voice/1.ogg"},
		Source:   Source{ChatID: -100123, TopicID: 7, MessageID: messageID},
		AuthorID: 555,
	}
}
