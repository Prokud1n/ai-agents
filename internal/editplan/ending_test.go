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

// endingSpeech is a talking head of two sections, a proof in the last one, the call and an introduction after it:
//
//	0–2 страх номер один | 3–6 вдруг это не работает | 7–8 работает отлично | 9–11 страх номер два |
//	12–14 а мне помогут | 15–23 у меня клиенты в трёх городах и все довольны | 24–28 пиши в комментариях свои
//	страхи | 29–32 меня зовут настя подписывайся
const endingSpeech = "страх номер один. | вдруг это не работает. | работает отлично. | страх номер два. | " +
	"а мне помогут. | у меня клиенты в трёх городах и все довольны. | пиши в комментариях свои страхи. | " +
	"меня зовут настя подписывайся."

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
		`{"kind": "section", "text": "страх 1", "word": 0}, {"kind": "section", "text": "страх 2", "word": 9}]}`, remove)
}

// TestSubmitEndingHint (T3.22): a selection that cut the proof of the last section and kept the introduction after the
// call gets a hint back once, without taking an attempt; the same decision again is accepted with a line in the
// details. Cutting the introduction, or keeping the proof, is accepted at once.
func TestSubmitEndingHint(t *testing.T) {
	t.Parallel()

	var (
		proofCut = endingInput(`{"from": 15, "to": 23}`)
		bothCut  = endingInput(`{"from": 15, "to": 23}, {"from": 29, "to": 32}`)
		broken   = `{"decision": "edit", "reason": "x", "remove_words": [{"from": 90, "to": 91}]}`
		detail   = "Финал: вырезан конец последнего раздела (слова 15–23), оставлено представление после призыва " +
			"(слова 29–32)"
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
		{name: "the proof kept", inputs: []string{endingInput(`{"from": 3, "to": 6}`)}, kind: EditOutcome},
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
		json.RawMessage(endingInput(`{"from": 15, "to": 23}`)))
	require.Error(t, err)

	for _, s := range []string{
		"вырезан конец последнего раздела — слова 15–23 «у меня клиенты в трёх городах",
		"оставлено представление — слова 29–32 «меня зовут настя подписывайся»",
		"кульминация", "отправь то же решение ещё раз",
	} {
		assert.Contains(t, err.Error(), s)
	}
}

// TestEndingWithoutSections: without a section heading the ending is not judged.
func TestEndingWithoutSections(t *testing.T) {
	t.Parallel()

	outcome, err := Apply(endingRequest(t), &Input{Decision: EditDecision, Reason: testReason,
		Remove: []roughcut.WordRange{{From: 15, To: 23}}})
	require.NoError(t, err)
	assert.Empty(t, outcome.endingHint)
}
