package mediaworker

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/editplan"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/subtitles"
)

const (
	// proposeNotes: the hook, two items of a list on the words «держите» (2) and «прямая» (4) and a callout on «дышите»
	// (8) of the synthetic talking head.
	proposeNotes = `{"decision": "edit", "reason": "ролик про планку", "hook_title": "` + hookText + `",
		"screen_notes": [
			{"kind": "list_item", "text": "1/ держите локти", "word": 2},
			{"kind": "list_item", "text": "2/ прямая линия", "word": 4},
			{"kind": "callout", "text": "дышите спокойно", "word": 8}
		]}`
	removeNotes    = `{"decision": "edit", "reason": "убери пункты", "screen_notes": []}`
	notesRevision  = "убери пункты"
	notesHandle    = "@prokudina.ana"
	notesShownAt   = 3.0 // the first item, after the hook (2.5 s)
	notesAfterAt   = 12.0
	notesBandLines = 1.25
	// cardShade: the end card darkens the frame to less than this share of its brightness before the card.
	cardShade = 0.5
	// cardBefore and cardIn: seconds before the end of the reel: before the card, and with the card faded in.
	cardBefore = 4.0
	cardIn     = 0.3
	// cardBand: rows of the frame with no text, compared before and under the card.
	cardBandFrom = 1100
	cardBandTo   = 1200
)

// notesModel is the editor's model behind the real decider: it proposes the hook and the screen notes of the first
// version and removes the notes by the revision.
type notesModel struct {
	mu      sync.Mutex
	prompts []string
}

func (m *notesModel) Run(ctx context.Context, data *agents.RunAgentData) (*agents.Result, error) {
	m.mu.Lock()
	m.prompts = append(m.prompts, data.Input)
	m.mu.Unlock()

	input := proposeNotes
	if strings.Contains(data.Input, "<trainer_revision>") {
		input = removeNotes
	}

	answer, err := editplan.NewSubmitTool().Execute(ctx, json.RawMessage(input))
	if err != nil {
		return nil, err
	}

	return &agents.Result{Text: answer}, nil
}

func (m *notesModel) Prompts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]string(nil), m.prompts...)
}

// TestIntegrationScreenNotes: a short talking head without a caption stops for the hook; the model proposes the hook,
// two items of a list and a callout (through the real decider and its checks), the worker draws them at the top after
// the hook with the trainer's handle and the end card (REELS_HANDLE goes in the job); the description names them. The
// revision «убери пункты» makes version 2 without them from the stored transcript — the speech recognition is not
// called again — the cut and the hook the same.
func TestIntegrationScreenNotes(t *testing.T) {
	t.Parallel()

	model := &notesModel{}
	env, speech := startModelEnv(t, model, notesHandle)
	first := env.data(45, "")

	v1 := env.run(t, first)
	assert.Equal(t, int32(1), speech.calls.Load())
	require.Len(t, model.Prompts(), 1)
	assert.Contains(t, model.Prompts()[0], "Пункты и цифры на экране: если тренер перечисляет")
	assert.Contains(t, v1.Text, "Заголовок: «"+hookText+"».")
	assert.Contains(t, v1.Text, "На экране: 2 пункта и 1 выноска.")

	files1, err := editjob.Files(env.files, env.source, 1)
	require.NoError(t, err)
	assert.Contains(t, string(env.read(t, files1.Decisions)), "На экране: 3")

	revision := *first
	revision.Version, revision.Revision = 2, notesRevision

	v2 := env.run(t, &revision)
	assert.Equal(t, int32(1), speech.calls.Load(), "the revision is not transcribed again")
	require.Len(t, model.Prompts(), 2)
	assert.Contains(t, model.Prompts()[1], "- пункт «1/ держите локти», с 0:03.5 исходника")
	assert.Contains(t, v2.Text, "Убрал пункты и цифры с экрана.")

	files2, err := editjob.Files(env.files, env.source, 2)
	require.NoError(t, err)

	var plan roughcut.Plan
	require.NoError(t, json.Unmarshal(env.read(t, files2.Plan), &plan))
	require.NotNil(t, plan.Edit)
	assert.Empty(t, plan.Edit.ScreenNotes)
	assert.Equal(t, hookText, plan.Edit.Hook, "the hook stays")

	duration := env.reelDuration(t, v1.MediaKey)
	assert.InDelta(t, duration, env.reelDuration(t, v2.MediaKey), 0.05, "the cut is the same")

	checkNotesDrawn(t, env.read(t, v1.MediaKey), env.read(t, v2.MediaKey), duration)
}

// checkNotesDrawn compares the reels with and without the notes in the band at the top: different while the first
// item is shown, the same long after the callout; both darken under the end card.
func checkNotesDrawn(t *testing.T, withNotes, plain []byte, duration float64) {
	t.Helper()

	style := subtitles.WordsStyle()
	from := style.Layout.MarginTop
	to := from + int(notesBandLines*float64(style.Hook.Size))

	assert.Greater(t, meanDiff(reelFrame(t, withNotes, notesShownAt), reelFrame(t, plain, notesShownAt), from, to),
		hookDiff, "the list is drawn in the reel with it only")
	assert.Less(t, meanDiff(reelFrame(t, withNotes, notesAfterAt), reelFrame(t, plain, notesAfterAt), from, to),
		sameDiff, "after the notes the reels are the same")

	before := bandMean(reelFrame(t, plain, duration-cardBefore), cardBandFrom, cardBandTo)
	card := bandMean(reelFrame(t, plain, duration-cardIn), cardBandFrom, cardBandTo)
	assert.Less(t, card, before*cardShade, "the end card darkens the last seconds")
}

// bandMean is the mean brightness of the rows [from, to) of a frame.
func bandMean(frame []byte, from, to int) float64 {
	total := 0
	for _, v := range frame[from*subtitles.FrameWidth : to*subtitles.FrameWidth] {
		total += int(v)
	}

	return float64(total) / float64((to-from)*subtitles.FrameWidth)
}
