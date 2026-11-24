package teamchat

import (
	"context"
	"io"
	"os"
	"sync"
	"time"

	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/ideas"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/pilot"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

const (
	testChatID  = int64(-1001000)
	testTopicID = int64(7)
	// testSourcesTopicID is «Исходники» in the integration tests' gateway config.
	testSourcesTopicID = int64(8)
	testMessageID      = int64(100)
	testUserID         = int64(42)
	testTaskID         = int64(5)
	testDraftID        = int64(900)
	testCallbackID     = "cb-1"
	partLimit          = 100 // fakeSender splits longer texts in two parts
)

// fakeService records the calls of the handler; the errors are returned by the matching methods.
type fakeService struct {
	mu sync.Mutex

	created   []tasks.CreateTaskData
	approved  []tasks.ApproveTaskData
	rejected  []tasks.RejectTaskData
	revisions []tasks.RequestRevisionByReplyData
	since     []time.Time

	exists     bool // Create finds an existing task
	createErr  error
	approveErr error
	rejectErr  error
	reviseErr  error
	statusErr  error
	report     tasks.StatusReport

	// The draft FindByDraftMessage finds: by default version 2 of the test task, awaiting approval.
	draftStatus  tasks.Status
	draftVersion int
	findErr      error
	// draftUnsaved: GetVersion finds the version without stored draft messages.
	draftUnsaved bool
	// draftMediaKey makes the found draft a video draft.
	draftMediaKey string
	// draftKind is the kind of the found draft's task.
	draftKind string
}

func (f *fakeService) GetVersion(_ context.Context, taskID int64, version int) (tasks.Version, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	v := tasks.Version{TaskID: taskID, Version: version}
	if !f.draftUnsaved {
		v.DraftMessageID = new(testDraftID + 1)
	}

	return v, nil
}

func (f *fakeService) FindByDraftMessage(_ context.Context, _, _ int64) (tasks.Task, tasks.Version, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.findErr != nil {
		return tasks.Task{}, tasks.Version{}, f.findErr
	}

	status, version := f.draftStatus, f.draftVersion
	if status == "" {
		status = tasks.AwaitingApprovalStatus
	}

	if version == 0 {
		version = 2
	}

	return tasks.Task{ID: testTaskID, Kind: f.draftKind, Status: status, Version: version},
		tasks.Version{TaskID: testTaskID, Version: version, MediaKey: f.draftMediaKey}, nil
}

func (f *fakeService) Create(_ context.Context, data *tasks.CreateTaskData) (tasks.Task, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.created = append(f.created, *data)
	if f.createErr != nil {
		return tasks.Task{}, false, f.createErr
	}

	return tasks.Task{ID: testTaskID, Agent: data.Agent}, !f.exists, nil
}

func (f *fakeService) Approve(_ context.Context, data *tasks.ApproveTaskData) (tasks.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.approved = append(f.approved, *data)

	return tasks.Task{ID: data.TaskID}, f.approveErr
}

func (f *fakeService) Reject(_ context.Context, data *tasks.RejectTaskData) (tasks.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.rejected = append(f.rejected, *data)

	return tasks.Task{ID: data.TaskID}, f.rejectErr
}

func (f *fakeService) RequestRevisionByReply(
	_ context.Context,
	data *tasks.RequestRevisionByReplyData,
) (tasks.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.revisions = append(f.revisions, *data)

	return tasks.Task{ID: testTaskID}, f.reviseErr
}

func (f *fakeService) StatusReport(_ context.Context, since time.Time) (tasks.StatusReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.since = append(f.since, since)

	return f.report, f.statusErr
}

// fakeIdeas is the backlog for the handler: it records the calls and returns the set results.
type fakeIdeas struct {
	mu sync.Mutex

	saved  []int64
	closed []closeCall
	limits []int

	idea      ideas.Idea // SaveApproved and Close return it
	existed   bool       // SaveApproved finds the idea saved before
	saveErr   error
	list      []ideas.Idea
	total     int
	listErr   error
	closeErr  error
	closeIdea *ideas.Idea // Close returns it instead of idea (with closeErr: the idea as it is)
}

type closeCall struct {
	ID     int64
	Status ideas.Status
}

func (f *fakeIdeas) SaveApproved(_ context.Context, taskID int64) (ideas.Idea, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.saved = append(f.saved, taskID)
	if f.saveErr != nil {
		return ideas.Idea{}, false, f.saveErr
	}

	return f.idea, !f.existed, nil
}

func (f *fakeIdeas) ListNew(_ context.Context, limit int) ([]ideas.Idea, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.limits = append(f.limits, limit)

	return f.list, f.total, f.listErr
}

func (f *fakeIdeas) Close(_ context.Context, id int64, status ideas.Status) (ideas.Idea, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.closed = append(f.closed, closeCall{ID: id, Status: status})

	if f.closeIdea != nil {
		return *f.closeIdea, f.closeErr
	}

	idea := f.idea
	idea.ID, idea.Status = id, status

	return idea, f.closeErr
}

type sentText struct {
	Role gateway.Role
	Msg  gateway.OutgoingMessage
}

type callbackAnswer struct {
	Role gateway.Role
	ID   string
	Text string
}

type reaction struct {
	Role      gateway.Role
	MessageID int64
	Emoji     string
}

type buttonsEdit struct {
	Role      gateway.Role
	MessageID int64
	Buttons   [][]gateway.Button
}

// fakeSender records what would be sent to Telegram.
type fakeSender struct {
	mu sync.Mutex

	texts     []sentText
	answers   []callbackAnswer
	reactions []reaction
	edits     []buttonsEdit
	textEdits []textEdit
	pins      []pinCall

	sendErr     error
	reactErr    error
	editErr     error
	partsErr    error
	editTextErr error
	pinErr      error
	// nextID, when set, numbers the sent messages from it instead of answering testDraftID.
	nextID int64
}

type textEdit struct {
	Role      gateway.Role
	MessageID int64
	Text      string
}

type pinCall struct {
	Role      gateway.Role
	MessageID int64
	Unpin     bool
}

func (f *fakeSender) EditText(
	_ context.Context,
	role gateway.Role,
	messageID int64,
	text string,
	_ [][]gateway.Button,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.textEdits = append(f.textEdits, textEdit{Role: role, MessageID: messageID, Text: text})

	return f.editTextErr
}

func (f *fakeSender) PinMessage(_ context.Context, role gateway.Role, messageID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.pins = append(f.pins, pinCall{Role: role, MessageID: messageID})

	return f.pinErr
}

func (f *fakeSender) UnpinMessage(_ context.Context, role gateway.Role, messageID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.pins = append(f.pins, pinCall{Role: role, MessageID: messageID, Unpin: true})

	return nil
}

func (f *fakeSender) SendText(_ context.Context, role gateway.Role, msg *gateway.OutgoingMessage) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.texts = append(f.texts, sentText{Role: role, Msg: *msg})
	if f.sendErr != nil {
		return 0, f.sendErr
	}

	if f.nextID != 0 {
		f.nextID++

		return f.nextID, nil
	}

	return testDraftID, nil
}

// SendTextParts sends the text as one part, or as two when it is longer than partLimit; with partsErr the
// first part is "sent" and the second fails.
func (f *fakeSender) SendTextParts(ctx context.Context, role gateway.Role, msg *gateway.OutgoingMessage) ([]int64, error) {
	if f.partsErr != nil {
		return []int64{testDraftID - 1}, f.partsErr
	}

	id, err := f.SendText(ctx, role, msg)
	if err != nil {
		return nil, err
	}

	if len([]rune(msg.Text)) > partLimit {
		return []int64{id - 1, id}, nil
	}

	return []int64{id}, nil
}

func (f *fakeSender) AnswerCallback(_ context.Context, role gateway.Role, callbackID, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.answers = append(f.answers, callbackAnswer{Role: role, ID: callbackID, Text: text})

	return f.sendErr
}

func (f *fakeSender) React(_ context.Context, role gateway.Role, messageID int64, emoji string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.reactions = append(f.reactions, reaction{Role: role, MessageID: messageID, Emoji: emoji})

	return f.reactErr
}

func (f *fakeSender) EditButtons(_ context.Context, role gateway.Role, messageID int64, buttons [][]gateway.Button) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.edits = append(f.edits, buttonsEdit{Role: role, MessageID: messageID, Buttons: buttons})

	return f.editErr
}

// reply is a text answering the test message in the test topic.
func reply(role gateway.Role, text string) sentText {
	return sentText{Role: role, Msg: gateway.OutgoingMessage{TopicID: testTopicID, ReplyTo: testMessageID, Text: text}}
}

func messageEvent(text string) *gateway.Event {
	return &gateway.Event{
		Type:       gateway.MessageEventType,
		ReceivedBy: gateway.MarketerRole,
		Target:     gateway.MarketerRole,
		ChatID:     testChatID,
		TopicID:    testTopicID,
		Topic:      gateway.PlanningTopic,
		MessageID:  testMessageID,
		From:       gateway.User{ID: testUserID},
		Text:       text,
	}
}

func commandEvent(cmd gateway.Command, args string) *gateway.Event {
	event := messageEvent(args)
	event.Type = gateway.CommandEventType
	event.Command = cmd

	return event
}

func callbackEvent(role gateway.Role, data string) *gateway.Event {
	return &gateway.Event{
		Type:       gateway.CallbackEventType,
		ReceivedBy: role,
		Target:     role,
		ChatID:     testChatID,
		TopicID:    testTopicID,
		MessageID:  testDraftID,
		From:       gateway.User{ID: testUserID},
		Callback:   &gateway.Callback{ID: testCallbackID, Data: data},
	}
}

// source is the task source of the test message.
func source() tasks.Source {
	return tasks.Source{ChatID: testChatID, TopicID: testTopicID, MessageID: testMessageID}
}

// testKeyPrefix is the key prefix of fakeStore: the keys are real media keys.
const testKeyPrefix = "incoming"

// fakeStore records the saved files and their content; err fails every save. Download writes the content of
// reels[key] to a new file in dir (downloadErr fails it).
type fakeStore struct {
	mu sync.Mutex

	saved       [][]byte
	files       []media.File
	err         error
	reels       map[string][]byte
	downloads   []string
	downloadErr error
}

func (f *fakeStore) Download(_ context.Context, key, dir string, _ int64) (*media.LocalFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.downloads = append(f.downloads, key)
	if f.downloadErr != nil {
		return nil, f.downloadErr
	}

	data, ok := f.reels[key]
	if !ok {
		return nil, media.ErrFileNotFound
	}

	file, err := os.CreateTemp(dir, "media-*.mp4")
	if err != nil {
		return nil, err
	}

	if _, err := file.Write(data); err != nil {
		_ = file.Close()

		return nil, err
	}

	if err := file.Close(); err != nil {
		return nil, err
	}

	return &media.LocalFile{Path: file.Name(), Size: int64(len(data))}, nil
}

func (f *fakeStore) Save(_ context.Context, file *media.File, r io.ReadSeeker) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.files = append(f.files, *file)
	if f.err != nil {
		return "", f.err
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}

	f.saved = append(f.saved, data)

	return media.Key(testKeyPrefix, file)
}

func (f *fakeStore) Files() []media.File {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]media.File(nil), f.files...)
}

func (f *fakeStore) Saved() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([][]byte(nil), f.saved...)
}

// fakePilot is the pilot store of the handler: it records the saved feedback and greetings.
type fakePilot struct {
	mu sync.Mutex

	feedback  []pilot.SaveFeedbackData
	greetings map[string]pilot.OnboardingMessage
	since     []time.Time

	exists  bool // SaveFeedback finds the feedback of the message saved before
	saveErr error
	metrics pilot.Metrics
	metErr  error
}

func (f *fakePilot) SaveFeedback(_ context.Context, data *pilot.SaveFeedbackData) (pilot.Feedback, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.feedback = append(f.feedback, *data)
	if f.saveErr != nil {
		return pilot.Feedback{}, false, f.saveErr
	}

	return pilot.Feedback{
		ID: int64(len(f.feedback)), AuthorID: data.AuthorID, AuthorName: data.AuthorName, Text: data.Text,
		Truncated: data.Truncated, TaskID: data.TaskID, TaskVersion: data.TaskVersion, Topic: data.Topic,
		TopicID: data.TopicID, ChatID: data.ChatID, MessageID: data.MessageID,
	}, !f.exists, nil
}

func (f *fakePilot) Metrics(_ context.Context, since, _ time.Time) (pilot.Metrics, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.since = append(f.since, since)

	return f.metrics, f.metErr
}

func (f *fakePilot) OnboardingMessage(_ context.Context, chatID int64, topic string) (pilot.OnboardingMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	msg, ok := f.greetings[topic]
	if !ok || msg.ChatID != chatID {
		return pilot.OnboardingMessage{}, pilot.ErrNotFound
	}

	return msg, nil
}

func (f *fakePilot) SaveOnboardingMessage(_ context.Context, msg *pilot.OnboardingMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.greetings == nil {
		f.greetings = map[string]pilot.OnboardingMessage{}
	}

	f.greetings[msg.Topic] = *msg

	return nil
}

// fakeTech records the reports to «Техничка».
type fakeTech struct {
	mu      sync.Mutex
	reports []string
	err     error
}

func (f *fakeTech) Report(_ context.Context, _, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.reports = append(f.reports, text)

	return f.err
}
