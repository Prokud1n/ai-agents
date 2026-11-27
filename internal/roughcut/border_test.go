package roughcut

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRemovedBorder (T3.30): where the sound of removed words stops on the side of a kept word. Whisper's edge stays
// when it is quiet next to it; a removed word sounding past its edge ends at the quiet stretch before the kept word
// (the closure of a stop inside it does not end it), and with the sound unbroken at its quietest window.
func TestRemovedBorder(t *testing.T) {
	t.Parallel()

	const dip = -35.0 // quieter than the speech, over the threshold of speech (-44)

	tests := []struct {
		name     string
		spans    [][3]float64
		edge     float64
		limit    float64
		d        float64
		expected float64
	}{
		{
			name:  "quiet next to Whisper's start: the start",
			spans: [][3]float64{{0.2, 1.0, testSpeech}, {1.15, 1.6, testSpeech}},
			edge:  1.15, limit: 1.0, d: backward, expected: 1.15,
		},
		{
			name:  "a removed word sounding 0.1 s before its start: its sound",
			spans: [][3]float64{{0.2, 1.0, testSpeech}, {1.05, 1.6, testSpeech}},
			edge:  1.15, limit: 1.0, d: backward, expected: 1.05,
		},
		{
			name:  "a removed word sounding 20 ms after its end: its sound",
			spans: [][3]float64{{1.2, 1.62, testSpeech}, {1.7, 2.3, testSpeech}},
			edge:  1.6, limit: 1.7, d: forward, expected: 1.62,
		},
		{
			name: "the closure of a stop inside the removed word does not end it",
			spans: [][3]float64{
				{1.2, 1.65, testSpeech}, {1.7, 1.75, testSpeech}, {1.9, 2.3, testSpeech},
			},
			edge: 1.6, limit: 1.95, d: forward, expected: 1.75,
		},
		{
			name:  "the sound unbroken: the quietest window",
			spans: [][3]float64{{0.2, 1.02, testSpeech}, {1.02, 1.03, dip}, {1.03, 1.6, testSpeech}},
			edge:  1.15, limit: 1.0, d: backward, expected: 1.025,
		},
		{
			name:  "the kept word overlaps the removed one: Whisper's edge",
			spans: [][3]float64{{0.2, 1.6, testSpeech}},
			edge:  1.15, limit: 1.2, d: backward, expected: 1.15,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			levels := newSpeechLevels(soundOf(4, tt.spans...))
			require.NotNil(t, levels)

			assert.InDelta(t, tt.expected, levels.removedBorder(tt.edge, tt.limit, tt.d), 1e-6)
		})
	}
}

// TestCutGridAroundRemovedSound (T3.30, live checks 4 and 5): the removed word starts 0.1 s before Whisper says (only
// 50 ms after the kept «раз») and sounds 20 ms after its end, close to the kept «два». The tail of «раз» takes none
// of its start (it was a cut-off piece of the next phrase at the end of a piece), the piece of «два» none of its end
// (20 ms of a loud sound before the word); both kept words keep their sound.
func TestCutGridAroundRemovedSound(t *testing.T) {
	t.Parallel()

	const (
		keptEnd      = 1.0
		removedFrom  = 1.05 // the sound of the removed word; Whisper: 1.15–1.6
		removedTo    = 1.62
		keptNextFrom = 1.7
	)

	levels := soundOf(3, [3]float64{0.2, keptEnd, testSpeech}, [3]float64{removedFrom, removedTo, testSpeech},
		[3]float64{keptNextFrom, 2.3, testSpeech})
	grid := &cutGrid{levels: newSpeechLevels(levels), fps: testFPS}
	require.NotNil(t, grid.levels)

	settings := testSettings()
	settings.MaxPause, settings.PadBefore, settings.PadAfter = 0.3, defaultPadBefore, defaultPadAfter

	end, next := grid.around(word("раз", 0.2, keptEnd), word("два", keptNextFrom, 2.3), 1.15, 1.6, settings)

	assert.GreaterOrEqual(t, end, keptEnd-1e-6, "«раз» is kept whole")
	assert.LessOrEqual(t, end, removedFrom+1e-6, "no start of the removed word in the tail")
	assert.GreaterOrEqual(t, next, removedTo-1e-6, "no end of the removed word at the start of the piece")
	assert.LessOrEqual(t, next, keptNextFrom-leadMin+1e-6, "«два» keeps its lead")

	src := words(word("раз", 0.2, keptEnd), word("лишнее", 1.15, 1.6), word("два", keptNextFrom, 2.3))

	plan, err := buildPlan(src, 3, settings, &Edit{Remove: []WordRange{{From: 1, To: 1}}}, grid)
	require.NoError(t, err)
	require.Len(t, plan.Keep, 2)
	assert.False(t, grid.levels.heardIn(plan.Keep[0].End, removedFrom), "nothing of the removed word in the reel")
	assert.LessOrEqual(t, plan.Keep[0].End, removedFrom+1e-3)
	assert.GreaterOrEqual(t, plan.Keep[1].Start, removedTo-1e-3)
}

// TestReelEdgesAroundRemovedSound (T3.30): removed words before the first kept piece and after the last one sound
// past Whisper's edges; the reel neither starts with the end of the first ones nor ends with the start of the last.
func TestReelEdgesAroundRemovedSound(t *testing.T) {
	t.Parallel()

	const (
		firstRemovedTo  = 0.63 // Whisper: 0.2–0.6
		lastRemovedFrom = 2.05 // Whisper: 2.15–2.6
	)

	levels := soundOf(3, [3]float64{0.2, firstRemovedTo, testSpeech}, [3]float64{0.7, 2.0, testSpeech},
		[3]float64{lastRemovedFrom, 2.6, testSpeech})
	grid := &cutGrid{levels: newSpeechLevels(levels), fps: testFPS}
	require.NotNil(t, grid.levels)

	settings := testSettings()
	settings.MaxPause, settings.PadBefore, settings.PadAfter = 0.3, defaultPadBefore, defaultPadAfter

	src := words(word("лишнее", 0.2, 0.6), word("раз", 0.7, 1.3), word("два", 1.35, 2.0), word("лишнее", 2.15, 2.6))

	plan, err := buildPlan(src, 3, settings, &Edit{Remove: []WordRange{{From: 0, To: 0}, {From: 3, To: 3}}}, grid)
	require.NoError(t, err)
	require.Len(t, plan.Keep, 1)
	assert.GreaterOrEqual(t, plan.Keep[0].Start, firstRemovedTo-1e-3, "no end of the removed word at the start")
	assert.LessOrEqual(t, plan.Keep[0].Start, 0.7-leadMin+1e-3, "«раз» keeps its lead")
	assert.LessOrEqual(t, plan.Keep[0].End, lastRemovedFrom+1e-3, "no start of the removed word at the end")
	assert.GreaterOrEqual(t, plan.Keep[0].End, 2.0-1e-3, "«два» is kept whole")
}
