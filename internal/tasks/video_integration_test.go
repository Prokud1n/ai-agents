package tasks

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/pgtest"
)

const (
	testEditor   = "editor"
	testVideoKey = "incoming/-100123/60/uniq.mov"
	testReelKey  = "edits/-100123/60/uniq-v1-reel.mp4"
	testReel2Key = "edits/-100123/60/uniq-v2-reel.mp4"
	testReelText = "Черновик рилс готов."
	testRevision = "убери первые 3 секунды"
	testWait     = time.Minute
)

// TestIntegrationDeferredVideoDraft: the editor's run waits for the media worker without holding a worker or
// counting attempts (deferred, then woken), then makes a video version; the draft carries the video key, a revision
// by text makes version 2 with its own video (the run gets the revision text), and the rejection closes the video
// draft.
func TestIntegrationDeferredVideoDraft(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, pgtest.Connect(t),
		fakeResult{err: &DeferredError{After: testWait}},
		fakeResult{err: &DeferredError{After: testWait}},
		fakeResult{text: testReelText, mediaKey: testReelKey},
		fakeResult{text: "Новая версия рилс.", mediaKey: testReel2Key},
	)
	ctx := context.Background()

	data := testTaskData(60)
	data.Agent, data.Kind, data.Input, data.Media = testEditor, "reels-edit", "подпись к видео", []string{testVideoKey}

	task, _, err := env.service.Create(ctx, data)
	require.NoError(t, err)

	checkDeferred(t, env, task.ID)

	// Woken by the worker's result before the poll interval: due at once.
	// A wake for another agent changes nothing (the channel is the editor's).
	require.NoError(t, env.service.Wake(ctx, task.ID, testAgent))
	at, ok := env.queue.Scheduled(task.ID)
	require.True(t, ok)
	assert.Equal(t, env.clock.Now().Add(testWait), at, "not woken for another agent")

	require.NoError(t, env.service.Wake(ctx, task.ID, testEditor))
	at, ok = env.queue.Scheduled(task.ID)
	require.True(t, ok)
	assert.Equal(t, env.clock.Now(), at)

	require.NoError(t, env.service.Run(ctx, task.ID)) // deferred again
	env.clock.Advance(testWait)
	require.NoError(t, env.service.Run(ctx, task.ID)) // the result

	task, err = env.service.Get(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, AwaitingApprovalStatus, task.Status)
	assert.Equal(t, 1, task.Version)

	v1, err := env.service.GetVersion(ctx, task.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, testReelKey, v1.MediaKey)
	assert.Equal(t, testReelText, v1.Content)

	drafts := env.notifier.Drafts()
	require.Len(t, drafts, 1)
	assert.Equal(t, testReelKey, drafts[0].MediaKey)
	assert.Equal(t, testReelText, drafts[0].Text)

	checkVideoDecisions(t, env, &task)
}

// checkDeferred: the first run is deferred: queued again for the poll interval, the attempt not counted; the
// executor got what the editor needs.
func checkDeferred(t *testing.T, env *testEnv, taskID int64) {
	t.Helper()

	ctx := context.Background()

	require.NoError(t, env.service.Run(ctx, taskID))

	task, err := env.service.Get(ctx, taskID)
	require.NoError(t, err)
	assert.Equal(t, QueuedStatus, task.Status)
	assert.Equal(t, 0, task.Attempts, "waiting for the worker is not an attempt")
	assert.Empty(t, task.LastError)

	at, ok := env.queue.Scheduled(taskID)
	require.True(t, ok)
	assert.Equal(t, env.clock.Now().Add(testWait), at)

	runs := env.executor.Data()
	require.Len(t, runs, 1)
	assert.Equal(t, 1, runs[0].Version)
	assert.Equal(t, []string{testVideoKey}, runs[0].Media)
	assert.Equal(t, "подпись к видео", runs[0].Input)
	assert.Equal(t, int64(60), runs[0].Source.MessageID)

	// Not due yet: a message delivered early is skipped.
	require.NoError(t, env.service.Run(ctx, taskID))
	assert.Len(t, env.executor.Data(), 1)
}

// checkVideoDecisions: a reply to a video draft is a revision: the next run makes version 2 from the revision text,
// version 1 keeps its video; the new video draft can be rejected.
func checkVideoDecisions(t *testing.T, env *testEnv, task *Task) {
	t.Helper()

	ctx := context.Background()

	_, err := env.service.RequestRevisionByReply(ctx, &RequestRevisionByReplyData{
		ChatID: task.Source.ChatID, ReplyToMessageID: 1001, MessageID: 61, UserID: 555, Text: testRevision,
	})
	require.NoError(t, err)
	env.waitBackground(t) // the revised draft is closed

	queued, err := env.service.Get(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, QueuedStatus, queued.Status, "the revision is queued")

	require.NoError(t, env.service.Run(ctx, task.ID))

	runs := env.executor.Data()
	last := runs[len(runs)-1]
	assert.Equal(t, 2, last.Version)
	assert.Equal(t, testRevision, last.Revision, "the editor gets the revision text")
	assert.Equal(t, []string{testVideoKey}, last.Media)

	v1, err := env.service.GetVersion(ctx, task.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, testReelKey, v1.MediaKey, "version 1 keeps its video")

	v2, err := env.service.GetVersion(ctx, task.ID, 2)
	require.NoError(t, err)
	assert.Equal(t, testReel2Key, v2.MediaKey)

	_, err = env.service.Reject(ctx, &RejectTaskData{TaskID: task.ID, Version: 2, UserID: 555})
	require.NoError(t, err)

	env.waitBackground(t)

	closed := env.notifier.Closed()
	require.Len(t, closed, 2)
	assert.Equal(t, ReviseAction, closed[0].Decision)
	assert.True(t, closed[0].Video)
	assert.Equal(t, RejectAction, closed[1].Decision)
	assert.Equal(t, 2, closed[1].Version)

	require.NoError(t, env.service.Wake(ctx, task.ID, testEditor), "waking a decided task changes nothing")
}
