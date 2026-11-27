package editplan

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

// pointsSpeech announces three rules and numbers them:
//
//	0–3 сегодня три важных правила | 4–6 первое держите спину | 7–9 второе дышите ровно |
//	10–13 и третье пейте воду | 14–16 пишите в комментариях
const pointsSpeech = "сегодня три важных правила. | первое держите спину. | второе дышите ровно. | " +
	"и третье пейте воду. | пишите в комментариях."

// countHint checks the announcement of the speech with the words removed by a selection (or not).
func countHint(t *testing.T, text string, remove []roughcut.WordRange, selected bool) (string, []string) {
	t.Helper()

	src := speech(text)
	req := &Request{TaskID: 7, Version: 1, Transcript: src}

	auto, err := roughcut.BuildPlanEdited(src, src.Duration, testSettings(), &roughcut.Edit{})
	require.NoError(t, err)

	req.Plan = &auto

	edit := roughcut.Edit{Remove: remove}

	plan, err := roughcut.BuildPlanEdited(src, src.Duration, testSettings(), &edit)
	require.NoError(t, err)

	hint := checkCounts(req, &edit, &plan, &selection{on: selected})

	return hint, edit.Details
}

// TestCheckCounts (T3.29): an announced number of points kept while numbered points are cut gets a hint and a line;
// all the points kept, the announcement cut with them, a number of times or of months, or a number without numbered
// points do not.
func TestCheckCounts(t *testing.T) {
	t.Parallel()

	const (
		timesSpeech  = "тренируйтесь три раза в неделю. | первое держите спину. | второе дышите ровно. | третье пейте воду."
		monthsSpeech = "за два месяца. | первое держите спину. | второе дышите ровно."
		plainSpeech  = "сегодня три важных правила. | держите спину. | дышите ровно. | пейте воду."
	)

	tests := []struct {
		name     string
		text     string
		remove   []roughcut.WordRange
		selected bool
		hint     []string
		detail   string
	}{
		{
			name: "the second point cut", text: pointsSpeech, remove: []roughcut.WordRange{{From: 7, To: 9}}, selected: true,
			hint: []string{
				"в ролике осталось объявление «три важных правила» (слова 1–3)", "оставлено 2 из 3", "вырезаны: слово 7 «второе»",
				"вырежи и объявление числа, и лишние пункты",
			},
			detail: "Структура: оставлено объявление «три важных правила» (слова 1–3), а пунктов с нумерацией — 2 из 3",
		},
		{
			name: "the numbering cut", text: pointsSpeech, selected: true,
			remove: []roughcut.WordRange{{From: 4, To: 4}, {From: 7, To: 7}, {From: 10, To: 11}},
			hint:   []string{"оставлено 0 из 3", "слово 4 «первое», слово 7 «второе», слово 11 «третье»"},
		},
		{name: "all the points kept", text: pointsSpeech, remove: []roughcut.WordRange{{From: 14, To: 16}}, selected: true},
		{
			name: "the announcement cut with the point", text: pointsSpeech, selected: true,
			remove: []roughcut.WordRange{{From: 0, To: 3}, {From: 7, To: 9}},
		},
		{name: "no selection", text: pointsSpeech, remove: []roughcut.WordRange{{From: 7, To: 9}}},
		{name: "three times a week", text: timesSpeech, remove: []roughcut.WordRange{{From: 9, To: 11}}, selected: true},
		{name: "two months", text: monthsSpeech, remove: []roughcut.WordRange{{From: 6, To: 8}}, selected: true},
		{name: "no numbered points", text: plainSpeech, remove: []roughcut.WordRange{{From: 6, To: 7}}, selected: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hint, details := countHint(t, tt.text, tt.remove, tt.selected)

			if tt.hint == nil {
				assert.Empty(t, hint)
				assert.Empty(t, details)

				return
			}

			for _, part := range tt.hint {
				assert.Contains(t, hint, part)
			}

			if tt.detail != "" {
				assert.Equal(t, []string{tt.detail}, details)
			}
		})
	}
}

// TestAnnounced: numbers of points, not of measures.
func TestAnnounced(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		text     string
		expected int
	}{
		{name: "a word", text: "три показателя", expected: 3},
		{name: "digits", text: "5 ошибок", expected: 5},
		{name: "with an adjective", text: "две простые вещи", expected: 2},
		{name: "times", text: "три раза"},
		{name: "a week", text: "две недели"},
		{name: "years", text: "пять лет"},
		{name: "a countdown", text: "два три"},
		{name: "one", text: "один показатель"},
		{name: "eleven", text: "11 ошибок"},
		{name: "the last word", text: "три"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			count, ok := announced(speech(tt.text).Words, 0)
			assert.Equal(t, tt.expected, count)
			assert.Equal(t, tt.expected > 0, ok)
		})
	}
}
