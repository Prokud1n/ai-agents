package editplan

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

// storySpeech is a talking head with a story, a link said as a line of its own and the call:
//
//	0–2 сегодня про бег | 3–6 моя ученица боялась начать | 7–13 через месяц она пробежала первые десять километров |
//	14–15 ну что | 16–19 пишите вопросы в комментариях
const storySpeech = "сегодня про бег. | моя ученица боялась начать. | " +
	"через месяц она пробежала первые десять километров. | ну что. | пишите вопросы в комментариях."

// storyTail is the end of the story: «десять километров».
const storyTail = "Конец фразы в чистке: слова 12–13 «десять километров»"

// storyRequest is the first version of storySpeech without a caption, only cleaned by the editor.
func storyRequest(t *testing.T) *Request {
	t.Helper()

	src := speech(storySpeech)

	plan, err := roughcut.BuildPlanEdited(src, src.Duration, testSettings(), &roughcut.Edit{})
	require.NoError(t, err)

	return &Request{TaskID: 7, Version: 1, Transcript: src, Plan: &plan, Cleanup: true}
}

// TestCheckPhraseEnds (T3.26): cleanup_words that cut the last words of a kept sentence (the end of a story) get a
// hint; a link said as a line of its own, cut whole, and words cut inside a sentence do not.
func TestCheckPhraseEnds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cleanup []roughcut.WordRange
		version int
		hint    bool
	}{
		{name: "the end of the story with the link after it", cleanup: []roughcut.WordRange{{From: 12, To: 15}}, hint: true},
		{name: "the end of the story alone", cleanup: []roughcut.WordRange{{From: 12, To: 13}}, hint: true},
		{name: "the link as a line of its own", cleanup: []roughcut.WordRange{{From: 14, To: 15}}},
		{name: "a whole sentence", cleanup: []roughcut.WordRange{{From: 3, To: 6}}},
		{name: "words inside a sentence", cleanup: []roughcut.WordRange{{From: 11, To: 11}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			outcome, err := Apply(storyRequest(t), &Input{
				Decision: EditDecision, Reason: testReason, CleanupWords: tt.cleanup,
			})
			require.NoError(t, err)

			if !tt.hint {
				assert.Empty(t, outcome.tailHint)
				assert.NotContains(t, outcome.Edit.Details, storyTail)

				return
			}

			assert.Contains(t, outcome.tailHint, "слова 12–13 «десять километров»")
			assert.Contains(t, outcome.tailHint, "верни конец фразы")
			assert.Contains(t, outcome.Edit.Details, storyTail)
		})
	}
}

// TestCheckPhraseEndsRepeatInRow: the same words said again at the end of a sentence are a repeat in a row, not its
// end.
func TestCheckPhraseEndsRepeatInRow(t *testing.T) {
	t.Parallel()

	src := speech("сегодня про бег. | держите темп держите темп. | пишите вопросы в комментариях.")

	plan, err := roughcut.BuildPlanEdited(src, src.Duration, testSettings(), &roughcut.Edit{})
	require.NoError(t, err)

	req := &Request{TaskID: 7, Version: 1, Transcript: src, Plan: &plan, Cleanup: true}

	outcome, err := Apply(req, &Input{
		Decision: EditDecision, Reason: testReason, CleanupWords: []roughcut.WordRange{{From: 5, To: 6}},
	})
	require.NoError(t, err)
	assert.Empty(t, outcome.tailHint)
}

// TestSubmitTailHint (T3.26): the hint about the cut end of a sentence comes back once without taking an attempt; the
// same decision again is accepted with the line in the details, the end brought back is accepted at once.
func TestSubmitTailHint(t *testing.T) {
	t.Parallel()

	const (
		tailCut = `{"decision": "edit", "reason": "чистка", "cleanup_words": [{"from": 12, "to": 15}]}`
		linkCut = `{"decision": "edit", "reason": "чистка", "cleanup_words": [{"from": 14, "to": 15}]}`
		broken  = `{"decision": "edit", "reason": "x", "remove_words": [{"from": 90, "to": 91}]}`
	)

	tests := []struct {
		name       string
		inputs     []string
		toolErrors int
		detail     bool
	}{
		{name: "the same decision after the hint", inputs: []string{tailCut, tailCut}, toolErrors: 1, detail: true},
		{name: "the end brought back after the hint", inputs: []string{tailCut, linkCut}, toolErrors: 1},
		{name: "the link only", inputs: []string{linkCut}},
		{name: "the hint takes no attempt", inputs: []string{tailCut, broken, linkCut}, toolErrors: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			session := NewSession(storyRequest(t))
			assert.Equal(t, tt.toolErrors, submitAll(t, session, tt.inputs))

			outcome, ok := session.Outcome()
			require.True(t, ok)
			assert.Equal(t, EditOutcome, outcome.Kind, outcome.Reason)

			if tt.detail {
				assert.Contains(t, outcome.Edit.Details, storyTail)
			} else {
				assert.NotContains(t, outcome.Edit.Details, storyTail)
			}
		})
	}

	_, err := NewSubmitTool().Execute(WithSession(t.Context(), NewSession(storyRequest(t))), json.RawMessage(tailCut))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "отправь то же решение ещё раз")
}
