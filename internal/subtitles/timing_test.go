package subtitles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTimeCues(t *testing.T) {
	t.Parallel()

	span := func(start, end float64) cue { return cue{Start: start, End: end} }

	tests := []struct {
		name     string
		input    []cue
		expected [][2]float64
	}{
		{name: "lead-in and hold", input: []cue{span(1, 2.5)}, expected: [][2]float64{{0.95, 2.6}}},
		{name: "never before zero", input: []cue{span(0.02, 1.5)}, expected: [][2]float64{{0, 1.6}}},
		{name: "short subtitle is extended", input: []cue{span(1, 1.2)}, expected: [][2]float64{{0.95, 1.75}}},
		{
			name:     "short subtitle is not extended over the next",
			input:    []cue{span(1, 1.2), span(1.5, 2.5)},
			expected: [][2]float64{{0.95, 1.45}, {1.45, 2.6}},
		},
		{
			name:     "small gap is closed",
			input:    []cue{span(1, 2), span(2.3, 3.5)},
			expected: [][2]float64{{0.95, 2.25}, {2.25, 3.6}},
		},
		{
			name:     "real pause stays",
			input:    []cue{span(1, 2), span(3, 4)},
			expected: [][2]float64{{0.95, 2.1}, {2.95, 4.1}},
		},
		{
			name:     "overlapping words do not overlap subtitles",
			input:    []cue{span(1, 2.4), span(2.2, 3)},
			expected: [][2]float64{{0.95, 2.15}, {2.15, 3.1}},
		},
		{
			name:     "same start keeps both visible",
			input:    []cue{span(1, 1.5), span(1, 2)},
			expected: [][2]float64{{0.95, 0.96}, {0.96, 2.1}},
		},
		{
			name:     "word stretched over a pause is cut",
			input:    []cue{span(1, 9)},
			expected: [][2]float64{{0.95, 4.55}},
		},
		{name: "rounded to centiseconds", input: []cue{span(1.2345, 2.3456)}, expected: [][2]float64{{1.18, 2.45}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := timeCues(tt.input)
			require.Len(t, got, len(tt.expected))

			for i, c := range got {
				assert.InDelta(t, tt.expected[i][0], c.Start, 1e-9, "start of %d", i)
				assert.InDelta(t, tt.expected[i][1], c.End, 1e-9, "end of %d", i)
			}
		})
	}
}

// TestCueTimingFollowsSpeech checks the acceptance criterion on a whole transcript: every subtitle appears within
// 0.1 s before its first word, stays until its last word ends (or the next subtitle starts), and never overlaps
// the next one.
func TestCueTimingFollowsSpeech(t *testing.T) {
	t.Parallel()

	const tolerance = 0.1

	style := PhraseStyle()
	result := speech(
		"Привет! Сегодня делаем присед со штангой.",
		"Делаем 3 подхода по 10 повторений, держим спину ровной и не заваливаем колени внутрь.",
		"Если тяжело — уменьшите вес, это нормально.",
	)

	tokens := markAccents(tokensFromTranscript(result), nil, nil)
	limits := layoutLimits{maxLines: style.Layout.MaxLines, maxLineChars: style.lineChars()}
	raw := buildCues(groupCues(buildUnits(tokens, limits.maxLineChars), limits), &style, false)
	timed := timeCues(raw)

	require.Len(t, timed, len(raw))
	require.Greater(t, len(timed), 3)

	for i, c := range timed {
		assert.GreaterOrEqual(t, c.Start, raw[i].Start-tolerance, "cue %d appears too early", i)
		assert.LessOrEqual(t, c.Start, raw[i].Start, "cue %d appears after its first word", i)
		assert.Greater(t, c.End, c.Start, "cue %d is empty", i)

		if i+1 < len(timed) {
			assert.LessOrEqual(t, c.End, timed[i+1].Start, "cue %d overlaps the next", i)
			assert.GreaterOrEqual(t, c.End, min(raw[i].End, timed[i+1].Start), "cue %d ends before its speech", i)
		} else {
			assert.GreaterOrEqual(t, c.End, raw[i].End, "last cue ends before its speech")
		}
	}
}
