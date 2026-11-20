package teamchat

import (
	"context"
	"sync"
	"time"

	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

const (
	testChatID    = int64(-1001000)
	testTopicID   = int64(7)
	testMessageID = int64(100)
	testUserID    = int64(42)
	testTaskID    = int64(5)
	testDraftID   = int64(900)
	partLimit     = 100 // fakeSender splits longer texts in two parts
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
}

func (f *fakeService) GetVersion(_ context.Context, taskID int64, version int) (tasks.Version, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	v := tasks.Version{TaskID: taskID, Version: version}
	if !f.draftUnsaved {
		v.DraftMessageID = new(int64(testDraftID + 1))
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

	return tasks.Task{ID: testTaskID, Status: status, Version: version},
		tasks.Version{TaskID: testTaskID, Version: version}, nil
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

	sendErr  error
	reactErr error
	editErr  error
	partsErr error
}

func (f *fakeSender) SendText(_ context.Context, role gateway.Role, msg *gateway.OutgoingMessage) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.texts = append(f.texts, sentText{Role: role, Msg: *msg})
	if f.sendErr != nil {
		return 0, f.sendErr
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
		Callback:   &gateway.Callback{ID: "cb-1", Data: data},
	}
}

// source is the task source of the test message.
func source() tasks.Source {
	return tasks.Source{ChatID: testChatID, TopicID: testTopicID, MessageID: testMessageID}
}
