package teamchat

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

const (
	testReelKey        = "edits/-1001000/100/u-v1-reel.mp4"
	testReelContent    = "reel-bytes"
	testProductionID   = int64(9)
	testReelText       = "Черновик рилс готов: было 1:00, стало 0:45."
	testSourceLinkText = "Исходник: https://t.me/c/1000/7/100"
)

var errReelSend = errors.New("telegram is down")

// fakeVideoSender records the sent videos with the content of the file at the moment of sending.
type fakeVideoSender struct {
	sent    []gateway.OutgoingMedia
	content []string
	err     error
}

func (f *fakeVideoSender) SendMedia(_ context.Context, _ gateway.Role, m *gateway.OutgoingMedia) (int64, error) {
	data, err := os.ReadFile(m.Path)
	if err != nil {
		return 0, err
	}

	f.sent = append(f.sent, *m)
	f.content = append(f.content, string(data))

	return testDraftID, f.err
}

func videoDraft() *tasks.Draft {
	return &tasks.Draft{
		TaskID: testTaskID, Version: 1, Agent: string(gateway.EditorRole), ChatID: testChatID, TopicID: testTopicID,
		ReplyToMessageID: testMessageID, Text: testReelText, MediaKey: testReelKey,
		Buttons: tasks.DraftButtons(testTaskID, 1),
	}
}

func TestNotifierSendsVideoDraft(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		production      int64
		expectedTopic   int64
		expectedReplyTo int64
		expectedCaption string
	}{
		{
			name: "to «Продакшн» with a link to the source", production: testProductionID,
			expectedTopic: testProductionID, expectedCaption: testReelText + paragraphBreak + testSourceLinkText,
		},
		{
			name:          "without «Продакшн»: a reply to the source in its topic",
			expectedTopic: testTopicID, expectedReplyTo: testMessageID, expectedCaption: testReelText,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			outbox := filepath.Join(t.TempDir(), "outbox")
			videos := &fakeVideoSender{}
			n := NewNotifier(&fakeSender{}, slog.New(slog.DiscardHandler))
			n.videos = &videoDrafts{
				sender: videos, files: &fakeStore{reels: map[string][]byte{testReelKey: []byte(testReelContent)}},
				outboxDir: outbox, maxFileSize: 1 << 20, productionTopicID: tt.production,
			}

			sent, err := n.SendDraft(t.Context(), videoDraft())
			require.NoError(t, err)
			assert.Equal(t, tasks.SentDraft{MessageID: testDraftID}, sent)

			require.Len(t, videos.sent, 1)
			m := videos.sent[0]
			assert.Equal(t, gateway.VideoMediaType, m.Type)
			assert.Equal(t, outbox, filepath.Dir(m.Path), "sent from the outbox")
			assert.Equal(t, testReelContent, videos.content[0])
			assert.Equal(t, tt.expectedTopic, m.TopicID)
			assert.Equal(t, tt.expectedReplyTo, m.ReplyTo)
			assert.Equal(t, tt.expectedCaption, m.Caption)
			assert.Equal(t, draftButtons(1), m.Buttons)
			assert.Equal(t, reelWidth, m.Width)
			assert.Equal(t, reelHeight, m.Height)

			entries, err := os.ReadDir(outbox)
			require.NoError(t, err)
			assert.Empty(t, entries, "the reel is removed from the outbox after sending")
		})
	}
}

func TestNotifierVideoDraftErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		store  *fakeStore
		sender *fakeVideoSender
	}{
		{name: "the reel is not in S3", store: &fakeStore{}, sender: &fakeVideoSender{}},
		{
			name:   "Telegram fails: the outbox is cleaned anyway",
			store:  &fakeStore{reels: map[string][]byte{testReelKey: []byte(testReelContent)}},
			sender: &fakeVideoSender{err: errReelSend},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			outbox := t.TempDir()
			n := NewNotifier(&fakeSender{}, slog.New(slog.DiscardHandler))
			n.videos = &videoDrafts{sender: tt.sender, files: tt.store, outboxDir: outbox, maxFileSize: 1 << 20}

			_, err := n.SendDraft(t.Context(), videoDraft())
			require.Error(t, err)

			entries, err := os.ReadDir(outbox)
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}

	_, err := NewNotifier(&fakeSender{}, slog.New(slog.DiscardHandler)).SendDraft(t.Context(), videoDraft())
	require.ErrorIs(t, err, errNoVideoSender, "a video draft without the video sender is an error, not a text")
}

// TestNotifierClosesVideoDraftInProduction: the decision answers the video draft in «Продакшн», where it was sent.
func TestNotifierClosesVideoDraftInProduction(t *testing.T) {
	t.Parallel()

	sender := &fakeSender{}
	n := NewNotifier(sender, slog.New(slog.DiscardHandler))
	n.videos = &videoDrafts{productionTopicID: testProductionID}

	require.NoError(t, n.CloseDraft(t.Context(), &tasks.CloseDraftData{
		TaskID: testTaskID, Version: 1, Agent: string(gateway.EditorRole), ChatID: testChatID, TopicID: testTopicID,
		MessageID: testDraftID, Decision: tasks.ApproveAction, Video: true,
	}))

	require.Len(t, sender.texts, 1)
	assert.Equal(t, testProductionID, sender.texts[0].Msg.TopicID)
	assert.Equal(t, testDraftID, sender.texts[0].Msg.ReplyTo)
	assert.Equal(t, approvedText, sender.texts[0].Msg.Text)
}

func TestFitCaption(t *testing.T) {
	t.Parallel()

	link := "\n\nИсходник: https://t.me/c/1000/7/100"

	tests := []struct {
		name  string
		text  string
		whole bool
	}{
		{name: "short: as it is", text: testReelText, whole: true},
		{name: "long: cut, the link kept", text: strings.Repeat("слово ", 400)},
		{name: "emoji count twice", text: strings.Repeat("💪", 600)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := fitCaption(tt.text, link, maxVideoCaption)

			assert.LessOrEqual(t, utf16Len(got), maxVideoCaption)
			assert.True(t, strings.HasSuffix(got, link), "the link to the source stays")

			if tt.whole {
				assert.Equal(t, tt.text+link, got)
			} else {
				assert.Contains(t, got, "…")
			}
		})
	}
}

func TestMessageLink(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		chatID   int64
		topicID  int64
		expected string
	}{
		{name: "message in a topic", chatID: -1001234567890, topicID: 8, expected: "https://t.me/c/1234567890/8/100"},
		{name: "General", chatID: -1001234567890, expected: "https://t.me/c/1234567890/100"},
		{name: "not a supergroup", chatID: -12345, topicID: 8},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, messageLink(tt.chatID, tt.topicID, testMessageID))
		})
	}
}

// TestCleanOutbox: on start the reels a killed app left are removed; other files and directories stay.
func TestCleanOutbox(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, name := range []string{"media-123.mp4", "cover.png"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
	}

	require.NoError(t, os.Mkdir(filepath.Join(dir, "media-dir"), 0o700))

	cleanOutbox(t.Context(), dir, slog.New(slog.DiscardHandler))
	cleanOutbox(t.Context(), filepath.Join(dir, "missing"), slog.New(slog.DiscardHandler))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}

	assert.ElementsMatch(t, []string{"cover.png", "media-dir"}, names)
}
