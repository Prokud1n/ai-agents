package editplan

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

// endingSpeech is a talking head of two sections, a story in the last one, the call and an introduction after it:
//
//	0–2 ошибка номер один | 3–5 бежать каждый день | 6–7 нужен отдых | 8–10 ошибка номер два | 11–12 не разминаться |
//	13–20 я сама так порвала связку и лечилась полгода | 21–25 пишите в комментариях свои ошибки |
//	26–29 меня зовут ира подписывайтесь
const endingSpeech = "ошибка номер один. | бежать каждый день. | нужен отдых. | ошибка номер два. | " +
	"не разминаться. | я сама так порвала связку и лечилась полгода. | пишите в комментариях свои ошибки. | " +
	"меня зовут ира подписывайтесь."

// endingShare: the target of the selection is the reel shortened by about this share, so it is selected.
const endingShare = 1.12

// endingRequest is the first version of endingSpeech longer than its target.
func endingRequest(t *testing.T) *Request {
	t.Helper()

	src := speech(endingSpeech)

	plan, err := roughcut.BuildPlanEdited(src, src.Duration, testSettings(), &roughcut.Edit{})
	require.NoError(t, err)

	req := &Request{TaskID: 7, Version: 1, Transcript: src, Plan: &plan,
		TargetSeconds: math.Round(plan.Duration / endingShare)}
	require.True(t, req.Selecting(), "the reel of %.1f s is selected", plan.Duration)

	return req
}

// endingInput is a selection that removes the ranges, with the two sections.
func endingInput(remove string) string {
	return fmt.Sprintf(`{"decision": "edit", "reason": "отбор", "remove_words": [%s], "screen_notes": [`+
		`{"kind": "section", "text": "ошибка 1", "word": 0}, {"kind": "section", "text": "ошибка 2", "word": 8}]}`, remove)
}

// TestSubmitEndingHint (T3.22): a selection that cut the proof of the last section and kept the introduction after the
// call gets a hint back once, without taking an attempt; the same decision again is accepted with a line in the
// details. Cutting the introduction, or keeping the proof, is accepted at once.
func TestSubmitEndingHint(t *testing.T) {
	t.Parallel()

	var (
		proofCut = endingInput(`{"from": 13, "to": 20}`)
		bothCut  = endingInput(`{"from": 13, "to": 20}, {"from": 26, "to": 29}`)
		broken   = `{"decision": "edit", "reason": "x", "remove_words": [{"from": 90, "to": 91}]}`
		detail   = "Финал: вырезан конец последнего раздела (слова 13–20), оставлено представление после призыва " +
			"(слова 26–29)"
	)

	tests := []struct {
		name       string
		inputs     []string
		toolErrors int
		kind       OutcomeKind
		detail     bool
	}{
		{name: "the same decision after the hint", inputs: []string{proofCut, proofCut}, toolErrors: 1, kind: EditOutcome, detail: true},
		{name: "the introduction cut after the hint", inputs: []string{proofCut, bothCut}, toolErrors: 1, kind: EditOutcome},
		{name: "the introduction cut at once", inputs: []string{bothCut}, kind: EditOutcome},
		{name: "the proof kept", inputs: []string{endingInput(`{"from": 3, "to": 5}`)}, kind: EditOutcome},
		{
			name: "the hint takes no attempt", inputs: []string{proofCut, broken, bothCut}, toolErrors: 2,
			kind: EditOutcome,
		},
		{name: "a failed check first: accepted with the line", inputs: []string{broken, proofCut}, toolErrors: 1, kind: EditOutcome, detail: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			session := NewSession(endingRequest(t))
			assert.Equal(t, tt.toolErrors, submitAll(t, session, tt.inputs))

			outcome, ok := session.Outcome()
			require.True(t, ok)
			assert.Equal(t, tt.kind, outcome.Kind, outcome.Reason)

			if tt.detail {
				assert.Contains(t, outcome.Edit.Details, detail)
			} else {
				assert.NotContains(t, outcome.Edit.Details, detail)
			}
		})
	}
}

// submitAll submits the inputs in the session one by one and counts the tool errors.
func submitAll(t *testing.T, session *Session, inputs []string) int {
	t.Helper()

	ctx := WithSession(t.Context(), session)
	tool := NewSubmitTool()
	toolErrors := 0

	for _, input := range inputs {
		if _, err := tool.Execute(ctx, json.RawMessage(input)); err != nil {
			var toolErr *agents.ToolError
			require.ErrorAs(t, err, &toolErr)

			toolErrors++
		}
	}

	return toolErrors
}

// TestSubmitEndingHintText: the hint names the cut proof and the kept introduction.
func TestSubmitEndingHintText(t *testing.T) {
	t.Parallel()

	session := NewSession(endingRequest(t))
	_, err := NewSubmitTool().Execute(WithSession(t.Context(), session),
		json.RawMessage(endingInput(`{"from": 13, "to": 20}`)))
	require.Error(t, err)

	for _, s := range []string{
		"вырезан конец последнего раздела — слова 13–20 «я сама так порвала связку",
		"оставлено представление — слова 26–29 «меня зовут ира подписывайтесь»",
		"кульминация", "отправь то же решение ещё раз",
	} {
		assert.Contains(t, err.Error(), s)
	}
}

// TestEndingWithoutSections: without a section heading the ending is not judged.
func TestEndingWithoutSections(t *testing.T) {
	t.Parallel()

	outcome, err := Apply(endingRequest(t), &Input{Decision: EditDecision, Reason: testReason,
		Remove: []roughcut.WordRange{{From: 13, To: 20}}})
	require.NoError(t, err)
	assert.Empty(t, outcome.endingHint)
}

// TestEndingOnlyFirstVersion: the hint about the ending is for the selection of a first version; a revision is the
// trainer's own request.
func TestEndingOnlyFirstVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		version  int
		expected bool
	}{
		{name: "the first version", version: 1, expected: true},
		{name: "the second version", version: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := endingRequest(t)
			req.Version = tt.version
			words := req.Transcript.Words
			remove := []roughcut.WordRange{{From: 13, To: 20}}

			plan, err := roughcut.BuildPlanEdited(req.Transcript, req.Plan.SourceDuration, req.Plan.Settings,
				&roughcut.Edit{Remove: remove})
			require.NoError(t, err)

			edit := &roughcut.Edit{Remove: remove, ScreenNotes: []roughcut.ScreenNote{
				{Kind: roughcut.SectionNote, Text: sectionOne, Start: words[0].Start},
				{Kind: roughcut.SectionNote, Text: sectionTwo, Start: words[8].Start},
			}}
			sel := &selection{on: true, keep: plan.Keep}

			assert.Equal(t, tt.expected, checkEnding(req, &Input{Remove: remove}, edit, sel) != "")
		})
	}
}
