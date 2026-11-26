package roughcut

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const (
	testFloor  = -60.0
	testSpeech = -20.0
	testBreath = -50.0 // a breath: above the floor, under the threshold of speech (-44)
	testFPS    = 30.0
)

// soundOf is the level of a sound of the given length (10 ms windows): the floor with the spans at their levels.
func soundOf(duration float64, spans ...[3]float64) *ffmpeg.Levels {
	n := int(math.Round(duration / ffmpeg.LevelWindow))
	db := make([]float64, n)

	for i := range db {
		db[i] = testFloor
		t := (float64(i) + 0.5) * ffmpeg.LevelWindow

		for _, s := range spans {
			if t >= s[0] && t < s[1] {
				db[i] = s[2]
			}
		}
	}

	return &ffmpeg.Levels{Window: ffmpeg.LevelWindow, DB: db}
}

func TestNewSpeechLevels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     *ffmpeg.Levels
		threshold float64
		none      bool
	}{
		{name: "no levels", input: nil, none: true},
		{name: "a constant tone tells nothing", input: soundOf(5, [3]float64{0, 5, testSpeech}), none: true},
		{
			name:  "speech over the floor: 40 % of the way up",
			input: soundOf(10, [3]float64{1, 4, testSpeech}, [3]float64{5, 8, testSpeech}), threshold: -44,
		},
		{
			name:  "a low contrast: at least 10 dB above the floor",
			input: soundOf(10, [3]float64{1, 4, -42}, [3]float64{5, 8, -42}), threshold: -50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := newSpeechLevels(tt.input)
			if tt.none {
				assert.Nil(t, got)

				return
			}

			require.NotNil(t, got)
			assert.InDelta(t, tt.threshold, got.threshold, 1e-9)
		})
	}
}

func TestSpeechEdges(t *testing.T) {
	t.Parallel()

	// A word whose sound runs 0.15 s past Whisper's end (an eaten ending) with the closure of a stop in it, and the next
	// one whose sound starts 0.1 s before Whisper's start.
	levels := newSpeechLevels(soundOf(4,
		[3]float64{0.5, 1.2, testSpeech}, [3]float64{1.28, 1.35, testSpeech}, [3]float64{2.4, 3, testSpeech}))
	require.NotNil(t, levels)

	tests := []struct {
		name     string
		got      float64
		expected float64
	}{
		{name: "the ending heard after Whisper's end, over the closure of the stop", got: levels.speechEnd(1.2, 1.5), expected: 1.35},
		{name: "not past the limit", got: levels.speechEnd(1.2, 1.3), expected: 1.3},
		{name: "a late start found earlier", got: levels.speechStart(2.5, 2.2), expected: 2.4},
		{name: "silence: Whisper's edge stays", got: levels.speechStart(3.5, 3.2), expected: 3.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.InDelta(t, tt.expected, tt.got, 1e-9)
		})
	}
}

func TestCutGridPause(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input *ffmpeg.Levels
		cut   bool
		end   float64
		next  float64
	}{
		{
			// The pause heard is 1.0–2.4: the first phrase keeps its tail to the quietest frame of the first breath, the
			// next starts so that a breath of silence is left.
			name:  "a long pause becomes a breath",
			input: soundOf(4, [3]float64{0.2, 1.0, testSpeech}, [3]float64{2.4, 3, testSpeech}),
			cut:   true, end: 1.0 + 1.0/testFPS, next: 69.0 / testFPS,
		},
		{
			// A breath before the next phrase is not speech: it is cut with the pause.
			name: "the breath in the pause is cut",
			input: soundOf(4, [3]float64{0.2, 1.0, testSpeech}, [3]float64{2.0, 2.3, testBreath},
				[3]float64{2.4, 3, testSpeech}),
			cut: true, end: 1.0 + 1.0/testFPS, next: 69.0 / testFPS,
		},
		{
			// Whisper says 1.0–1.5, but the word goes on to 1.3: the silence heard (0.2 s) is short, nothing is cut.
			name:  "a pause shorter than heard stays",
			input: soundOf(4, [3]float64{0.2, 1.3, testSpeech}, [3]float64{1.5, 3, testSpeech}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			grid := &cutGrid{levels: newSpeechLevels(tt.input), fps: testFPS}
			require.NotNil(t, grid.levels)

			prevEnd, nextStart := 1.0, 2.5
			if !tt.cut {
				nextStart = 1.5
			}

			end, next, ok := grid.pause(prevEnd, nextStart, 0.3)
			require.Equal(t, tt.cut, ok)

			if !tt.cut {
				return
			}

			assert.InDelta(t, tt.end, end, 1e-6)
			assert.InDelta(t, tt.next, next, 1e-6)
			left := (end - 1.0) + (2.4 - next)
			assert.GreaterOrEqual(t, left, breathPause-1e-6, "a breath of silence is left")
			assert.LessOrEqual(t, left, breathPause+2/testFPS+1e-6, "the grid adds at most two frames")
		})
	}
}

// TestBuildPlanBySound: with the sound level the phrases keep the sound Whisper's times miss, the pauses become a
// breath, a filler is cut at the quietest points around it, and every cut point is on the frame grid.
func TestBuildPlanBySound(t *testing.T) {
	t.Parallel()

	src := words(word("раз", 1, 1.4), word("два", 1.5, 2.0), word("эээ", 2.1, 2.4), word("три", 2.6, 3.0),
		word("четыре", 4.5, 5.0))
	// "два" sounds to 2.05, "три" to 3.2 (an eaten ending), "четыре" from 4.42; the filler is at 2.12–2.38.
	levels := soundOf(7, [3]float64{1.0, 1.4, testSpeech}, [3]float64{1.5, 2.05, testSpeech},
		[3]float64{2.12, 2.38, testSpeech}, [3]float64{2.6, 3.2, testSpeech}, [3]float64{4.42, 5.0, testSpeech})

	settings := testSettings()
	settings.MaxPause, settings.PadBefore, settings.PadAfter = 0.3, defaultPadBefore, defaultPadAfter

	grid := &cutGrid{levels: newSpeechLevels(levels), fps: testFPS}
	require.NotNil(t, grid.levels)

	plan, err := buildPlan(src, 7, settings, &Edit{}, grid)
	require.NoError(t, err)
	assert.True(t, plan.SoundCuts)
	require.Len(t, plan.Keep, 3)

	for _, iv := range plan.Keep {
		for _, edge := range []float64{iv.Start, iv.End} {
			assert.InDelta(t, math.Round(edge*testFPS), edge*testFPS, 0.03, "%.3f on the frame grid", edge)
		}
	}

	assert.InDelta(t, 0.9, plan.Keep[0].Start, 1/testFPS+1e-3, "edgeLead before the first sound")
	assert.Greater(t, plan.Keep[0].End, 2.05, "the tail of «два» is kept")
	assert.LessOrEqual(t, plan.Keep[0].End, 2.12, "the filler is not")
	assert.GreaterOrEqual(t, plan.Keep[1].Start, 2.38, "after the filler")
	assert.Less(t, plan.Keep[1].Start, 2.6)
	assert.Greater(t, plan.Keep[1].End, 3.2, "the eaten ending of «три» is kept")

	silence := (plan.Keep[1].End - 3.2) + (4.42 - plan.Keep[2].Start)
	assert.InDelta(t, breathPause, silence, 2/testFPS+1e-3, "the pause of 1.2 s is a breath")
	assert.Less(t, plan.Keep[2].Start, 4.42, "the early start of «четыре» is kept")
	assert.InDelta(t, 5.15, plan.Keep[2].End, 1/testFPS+1e-3, "edgeTail after the last sound")
}

func TestLongPauses(t *testing.T) {
	t.Parallel()

	levels := newSpeechLevels(soundOf(10, [3]float64{0.5, 1, testSpeech}, [3]float64{1.2, 2, testSpeech},
		[3]float64{2.5, 3, testSpeech}, [3]float64{6, 7, testSpeech}))
	require.NotNil(t, levels)

	tests := []struct {
		name     string
		input    []Interval
		expected int
	}{
		{name: "0.2 s stays short, 0.5 s is long", input: []Interval{{Start: 0.4, End: 3.2}}, expected: 1},
		{name: "the silence at the edges does not count", input: []Interval{{Start: 0, End: 1.1}}, expected: 0},
		{
			name:     "across a joint: 0.2 + 0.25 s of silence on both sides",
			input:    []Interval{{Start: 0.4, End: 3.2}, {Start: 5.75, End: 7}},
			expected: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, levels.longPauses(tt.input))
		})
	}
}

func TestCountLongPausesByWords(t *testing.T) {
	t.Parallel()

	reel := []transcription.Word{word("раз", 0, 0.4), word("два", 0.6, 1), word("три", 1.5, 2)}

	assert.Equal(t, 1, countLongPauses(&Plan{}, nil, reel))
}

func TestDefaultMargins(t *testing.T) {
	t.Parallel()

	settings := newTestService(t, &fakeMedia{}).DefaultSettings()

	assert.InDelta(t, 0.12, settings.PadBefore, 1e-9)
	assert.InDelta(t, 0.18, settings.PadAfter, 1e-9)
	assert.InDelta(t, settings.PadBefore+settings.PadAfter, effectiveSettings(Settings{
		MaxPause: 0.3, PadBefore: settings.PadBefore, PadAfter: settings.PadAfter,
	}).PadBefore+effectiveSettings(Settings{
		MaxPause: 0.3, PadBefore: settings.PadBefore, PadAfter: settings.PadAfter,
	}).PadAfter, 1e-9, "they fit the default threshold of 0.3 s and are not shrunk")
}
