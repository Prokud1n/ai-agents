package editplan

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

// revisionStory is a revision of storySpeech (nothing cut by the previous version).
func revisionStory(t *testing.T) *Request {
	t.Helper()

	req := storyRequest(t)
	req.Version, req.Cleanup = 2, false

	return req
}

// TestCheckFragments (T3.29): a revision that cuts a sentence but its last word (or its first) gets a hint and a line;
// a sentence cut whole, a few words cut inside a sentence or the first version do not.
func TestCheckFragments(t *testing.T) {
	t.Parallel()

	const detail = "Обрывок фразы в правке: слово 13 «километров»"

	tests := []struct {
		name     string
		version  int
		remove   []roughcut.WordRange
		hint     []string
		expected []string
	}{
		{
			name: "all but the last word", version: 2, remove: []roughcut.WordRange{{From: 7, To: 12}},
			hint: []string{
				"от предложения «через месяц она пробежала первые десять…» (слова 7–13) осталось только слово 13 " +
					"«километров»",
				"вырежи и эти слова или верни предложение целиком",
			},
			expected: []string{detail},
		},
		{
			name: "all but the first two words", version: 2, remove: []roughcut.WordRange{{From: 9, To: 13}},
			hint:     []string{"осталось только слова 7–8 «через месяц»"},
			expected: []string{"Обрывок фразы в правке: слова 7–8 «через месяц»"},
		},
		{name: "the sentence whole", version: 2, remove: []roughcut.WordRange{{From: 7, To: 13}}},
		{name: "words inside a sentence", version: 2, remove: []roughcut.WordRange{{From: 11, To: 12}}},
		{name: "the first version", version: 1, remove: []roughcut.WordRange{{From: 7, To: 12}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := revisionStory(t)
			req.Version = tt.version

			outcome, err := Apply(req, &Input{Decision: EditDecision, Reason: testReason, Remove: tt.remove})
			require.NoError(t, err)

			if tt.hint == nil {
				assert.Empty(t, outcome.fragmentHint)
				assert.NotContains(t, outcome.Edit.Details, detail)

				return
			}

			for _, part := range tt.hint {
				assert.Contains(t, outcome.fragmentHint, part)
			}

			assert.Equal(t, tt.expected, outcome.Edit.Details)
		})
	}
}

// TestSubmitFragmentHint (T3.29): the hint comes back once; the same decision is accepted, the fragment cut too.
func TestSubmitFragmentHint(t *testing.T) {
	t.Parallel()

	const (
		fragment = `{"decision": "edit", "reason": "короче", "remove_words": [{"from": 7, "to": 12}]}`
		whole    = `{"decision": "edit", "reason": "короче", "remove_words": [{"from": 7, "to": 13}]}`
		broken   = `{"decision": "edit", "reason": "x", "remove_words": [{"from": 90, "to": 91}]}`
	)

	tests := []struct {
		name       string
		inputs     []string
		toolErrors int
		left       bool
	}{
		{name: sameAfterHint, inputs: []string{fragment, fragment}, toolErrors: 1, left: true},
		{name: "the fragment cut after the hint", inputs: []string{fragment, whole}, toolErrors: 1},
		{
			name:   "the correction after the hint fails the last attempt: the hinted decision stands",
			inputs: []string{broken, fragment, broken}, toolErrors: 2, left: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			session := NewSession(revisionStory(t))
			assert.Equal(t, tt.toolErrors, submitAll(t, session, tt.inputs))

			outcome, ok := session.Outcome()
			require.True(t, ok)
			assert.Equal(t, EditOutcome, outcome.Kind, outcome.Reason)
			assert.Equal(t, tt.left, len(outcome.Edit.Details) > 0)
		})
	}
}
