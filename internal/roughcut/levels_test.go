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
		{name: "the ending heard after Whisper's end, over the closure of the stop", got: levels.speechEnd(1.2, 1.2, 1.5), expected: 1.35},
		{name: "not past the limit", got: levels.speechEnd(1.2, 1.2, 1.3), expected: 1.3},
		{name: "a late start found earlier", got: levels.speechStart(2.5, 2.5, 2.2), expected: 2.4},
		{name: "silence: Whisper's edge stays", got: levels.speechStart(3.5, 3.5, 3.2), expected: 3.5},
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
			// The pause heard is 1.0–2.4: the tail of the first phrase ends at the latest of the equally quiet frames
			// 0.06–0.10 s after its sound, the next starts so that a breath of 0.15 s (± half a frame) is left.
			name:  "a long pause becomes a breath",
			input: soundOf(4, [3]float64{0.2, 1.0, testSpeech}, [3]float64{2.4, 3, testSpeech}),
			cut:   true, end: 33.0 / testFPS, next: 70.0 / testFPS,
		},
		{
			// A breath before the next phrase is not speech: it is cut with the pause.
			name: "the breath in the pause is cut",
			input: soundOf(4, [3]float64{0.2, 1.0, testSpeech}, [3]float64{2.0, 2.3, testBreath},
				[3]float64{2.4, 3, testSpeech}),
			cut: true, end: 33.0 / testFPS, next: 70.0 / testFPS,
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

			end, next, ok := grid.pause(word("раз", 0.2, prevEnd), word("два", nextStart, 3), 0.3)
			require.Equal(t, tt.cut, ok)

			if !tt.cut {
				return
			}

			assert.InDelta(t, tt.end, end, 1e-6)
			assert.InDelta(t, tt.next, next, 1e-6)
			assert.InDelta(t, breathPause, (end-1.0)+(2.4-next), 0.5/testFPS+1e-6, "a breath of silence is left")
			assert.GreaterOrEqual(t, end-1.0, tailMin-1e-6, "the tail of the phrase")
			assert.GreaterOrEqual(t, 2.4-next, leadMin-1e-6, "the lead of the next one")
		})
	}
}

// TestCutGridPauseStretchedWords: Whisper's edges lie in the silence (a late end, an early start, both edges meeting
// in the middle of a real pause): the pause is measured from the sound inside the words and still becomes a breath.
func TestCutGridPauseStretchedWords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      *ffmpeg.Levels
		prev, next transcription.Word
		heardEnd   float64
		heardStart float64
	}{
		{
			name:  "the end late by 0.35 s",
			input: soundOf(4, [3]float64{0.2, 1.0, testSpeech}, [3]float64{2.4, 3, testSpeech}),
			prev:  word("раз", 0.2, 1.35), next: word("два", 2.4, 3), heardEnd: 1.0, heardStart: 2.4,
		},
		{
			name:  "the start early by 0.3 s",
			input: soundOf(4, [3]float64{0.2, 1.0, testSpeech}, [3]float64{2.4, 3, testSpeech}),
			prev:  word("раз", 0.2, 1.0), next: word("два", 2.1, 3), heardEnd: 1.0, heardStart: 2.4,
		},
		{
			// A short word (its sound 0.3 s) stretched by 0.35 s: its middle is in the silence.
			name:  "a short word late by 0.35 s",
			input: soundOf(4, [3]float64{0.8, 1.1, testSpeech}, [3]float64{2.4, 3, testSpeech}),
			prev:  word("раз", 0.8, 1.45), next: word("два", 2.4, 3), heardEnd: 1.1, heardStart: 2.4,
		},
		{
			// The sound 0.8–1.1 and from 1.9, Whisper says 0.8–1.5 | 1.5–2.2: the pause is not seen by the margins at all.
			name:  "the edges meet in the middle of a pause of 0.8 s",
			input: soundOf(4, [3]float64{0.8, 1.1, testSpeech}, [3]float64{1.9, 3, testSpeech}),
			prev:  word("раз", 0.8, 1.5), next: word("два", 1.5, 2.2), heardEnd: 1.1, heardStart: 1.9,
		},
		{
			// A short word (0.3 s of sound) late by 0.35 s, the next one early by 0.4 s: they almost meet.
			name:  "a short word stretched, the edges meet inside a pause of 0.8 s",
			input: soundOf(4, [3]float64{1.0, 1.3, testSpeech}, [3]float64{2.1, 3, testSpeech}),
			prev:  word("раз", 1.0, 1.65), next: word("два", 1.7, 2.6), heardEnd: 1.3, heardStart: 2.1,
		},
		{
			// Whisper puts no pause between the words at all, the sound has 0.8 s of silence.
			name:  "the edges meet inside a pause of 0.8 s",
			input: soundOf(4, [3]float64{0.2, 1.0, testSpeech}, [3]float64{1.8, 3, testSpeech}),
			prev:  word("раз", 0.2, 1.4), next: word("два", 1.4, 3), heardEnd: 1.0, heardStart: 1.8,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			grid := &cutGrid{levels: newSpeechLevels(tt.input), fps: testFPS}
			require.NotNil(t, grid.levels)

			end, next, ok := grid.pause(tt.prev, tt.next, 0.3)
			require.True(t, ok, "the pause is cut")

			assert.InDelta(t, breathPause, (end-tt.heardEnd)+(tt.heardStart-next), 0.5/testFPS+1e-6,
				"a breath of silence is left")
			assert.GreaterOrEqual(t, end-tt.heardEnd, tailMin-1e-6, "the tail of the phrase")
			assert.GreaterOrEqual(t, tt.heardStart-next, leadMin-1e-6, "the lead of the next one")
			assert.Greater(t, end, (tt.prev.Start+tt.prev.End)/2, "the word keeps its middle")
			assert.Less(t, next, (tt.next.Start+tt.next.End)/2, "the next word keeps its middle")
		})
	}
}

// TestQuietEndingKept: a fricative ending (-48 dBFS: under the threshold of speech, -44, well over the floor, -60)
// that Whisper ends with is not silence: the edge is not moved inside the word, the ending keeps its tail.
func TestQuietEndingKept(t *testing.T) {
	t.Parallel()

	const fricative = -48.0

	levels := soundOf(4, [3]float64{0.2, 0.9, testSpeech}, [3]float64{0.9, 1.0, fricative},
		[3]float64{2.4, 3, testSpeech})
	grid := &cutGrid{levels: newSpeechLevels(levels), fps: testFPS}
	require.NotNil(t, grid.levels)
	require.Less(t, fricative, grid.levels.threshold, "the ending is under the threshold of speech")

	assert.InDelta(t, 1.0, grid.levels.speechEnd(1.0, innerEnd(word("раз", 0.2, 1.0)), 1.3), 1e-9,
		"the end stays at the end of the ending")

	end, _, ok := grid.pause(word("раз", 0.2, 1.0), word("два", 2.4, 3), 0.3)
	require.True(t, ok)
	assert.GreaterOrEqual(t, end, 1.0+tailMin-1e-6, "the ending and its tail are kept")
}

// TestBuildPlanStretchedPause: the plan cuts a pause Whisper hid inside the words, and the subtitles keep both words.
func TestBuildPlanStretchedPause(t *testing.T) {
	t.Parallel()

	src := words(word("раз", 0.2, 1.4), word("два", 1.4, 3))
	levels := soundOf(4, [3]float64{0.2, 1.0, testSpeech}, [3]float64{1.8, 3, testSpeech})

	settings := testSettings()
	settings.MaxPause, settings.PadBefore, settings.PadAfter = 0.3, defaultPadBefore, defaultPadAfter

	grid := &cutGrid{levels: newSpeechLevels(levels), fps: testFPS}
	require.NotNil(t, grid.levels)

	plan, err := buildPlan(src, 4, settings, &Edit{}, grid)
	require.NoError(t, err)
	require.Len(t, plan.Keep, 2)

	silence := (plan.Keep[0].End - 1.0) + (1.8 - plan.Keep[1].Start)
	assert.InDelta(t, breathPause, silence, 0.5/testFPS+1e-3, "the pause of 0.8 s is a breath")
	assert.Len(t, RemapTranscript(src, plan.Keep).Words, 2, "both words stay in the subtitles")
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
	assert.InDelta(t, breathPause, silence, 0.5/testFPS+1e-3, "the pause of 1.2 s is a breath")
	assert.GreaterOrEqual(t, plan.Keep[1].End-3.2, tailMin-1e-3, "the tail of «три»")

	// Around the filler: at least a breath of silence is left, «два» to its cut and from the cut to «три».
	around := (plan.Keep[0].End - 2.05) + (2.6 - plan.Keep[1].Start)
	assert.GreaterOrEqual(t, around, breathPause-1e-3, "a breath around the removed filler")
	assert.Less(t, plan.Keep[2].Start, 4.42, "the early start of «четыре» is kept")
	assert.InDelta(t, 5.15, plan.Keep[2].End, 1/testFPS+1e-3, "edgeTail after the last sound")
}

// TestCutGridAroundBreath: the quietest frame before the next word is right at it (a breath before it is louder), so
// the silence left around the removed word would be short: the next word starts earlier, not before the removed one.
func TestCutGridAroundBreath(t *testing.T) {
	t.Parallel()

	levels := soundOf(4, [3]float64{0.2, 1.0, testSpeech}, [3]float64{1.12, 1.38, testSpeech},
		[3]float64{1.38, 1.455, testBreath}, [3]float64{1.51, 2, testSpeech})
	grid := &cutGrid{levels: newSpeechLevels(levels), fps: testFPS}
	require.NotNil(t, grid.levels)
	require.InDelta(t, 44.0/testFPS, grid.quietest(1.4, 1.47, false), 1e-6, "the quietest frame, 0.043 s before the word")

	settings := testSettings()
	settings.PadBefore, settings.PadAfter = defaultPadBefore, defaultPadAfter

	end, next := grid.around(word("раз", 0.2, 1.0), word("два", 1.51, 2), 1.12, 1.38, settings)

	assert.InDelta(t, 33.0/testFPS, end, 1e-6, "the tail: the latest quiet frame 0.06–0.10 s after the sound")
	assert.InDelta(t, 43.0/testFPS, next, 1e-6, "one frame earlier than the quietest, into the breath")
	assert.GreaterOrEqual(t, next, 1.38, "never into the removed word")
	assert.GreaterOrEqual(t, (end-1.0)+(1.51-next), breathPause-1e-6, "at least a breath of silence")
}

// TestCutGridAroundStretchedWords (T3.22): before and after a removed piece Whisper stretched the kept words far into
// its silence («раз» sounds 0.2–1.0, Whisper says to 2.0; «два» sounds from 4.4, Whisper starts it at 3.8): both
// middles are in the silence. The joint still leaves a breath, not the 1 s of silence their middles would keep.
func TestCutGridAroundStretchedWords(t *testing.T) {
	t.Parallel()

	const (
		soundEnd   = 1.0
		soundStart = 4.4
	)

	levels := soundOf(6, [3]float64{0.2, soundEnd, testSpeech}, [3]float64{2.6, 3.0, testSpeech},
		[3]float64{soundStart, 5.2, testSpeech})
	grid := &cutGrid{levels: newSpeechLevels(levels), fps: testFPS}
	require.NotNil(t, grid.levels)

	settings := testSettings()
	settings.MaxPause, settings.PadBefore, settings.PadAfter = 0.3, defaultPadBefore, defaultPadAfter

	end, next := grid.around(word("раз", 0.2, 2.0), word("два", 3.8, 4.6), 2.6, 3.0, settings)

	silence := (end - soundEnd) + (soundStart - next)
	assert.LessOrEqual(t, silence, settings.MaxPause+1e-6, "no long pause at the joint")
	assert.GreaterOrEqual(t, silence, breathPause-0.5/testFPS-1e-6, "a breath is left")
	assert.GreaterOrEqual(t, end-soundEnd, tailMin-1e-6, "the tail of «раз»")
	assert.GreaterOrEqual(t, soundStart-next, leadMin-1e-6, "the lead of «два»")
	assert.GreaterOrEqual(t, next, 3.0, "never into the removed word")

	// The plan keeps both words in the subtitles: their silent stretched edges are trimmed to the kept pieces.
	src := words(word("раз", 0.2, 2.0), word("лишнее", 2.6, 3.0), word("два", 3.8, 4.6))

	plan, err := buildPlan(src, 6, settings, &Edit{Remove: []WordRange{{From: 1, To: 1}}}, grid)
	require.NoError(t, err)
	require.Len(t, plan.Keep, 2)
	assert.InDelta(t, end, plan.Keep[0].End, 1e-3)
	assert.InDelta(t, next, plan.Keep[1].Start, 1e-3)

	assert.Empty(t, RemapTranscript(src, plan.Keep).Words, "both middles are cut")

	reel := RemapTranscript(trimSilentEdges(src, plan.Keep, grid.levels), plan.Keep).Words
	require.Len(t, reel, 2)
	assert.Equal(t, "раз", reel[0].Word)
	assert.Equal(t, "два", reel[1].Word)
}

// TestCutGridAroundRoomInStretchedWords (T3.26): Whisper stretched the kept words of a joint over the room noise or a
// breath (over the silence level, under the threshold of speech), not over silence: the word before the cut, the first
// word of the next piece, or both. The joint is cut by the sound still — no more than MaxPause of quiet between the
// speech on both sides — and a sound of speech inside a stretched word is never cut off.
func TestCutGridAroundRoomInStretchedWords(t *testing.T) {
	t.Parallel()

	const (
		room      = -52.0 // over the silence level (-55), under the threshold of speech (-44)
		speechEnd = 1.0
		firstFrom = 4.4
		inWordEnd = 1.6 // the end of a sound of speech inside the stretched word
	)

	tests := []struct {
		name       string
		spans      [][3]float64
		heardEnd   float64
		heardStart float64
	}{
		{
			name:     "the word before the cut stretched over the room",
			spans:    [][3]float64{{speechEnd, 2.6, room}},
			heardEnd: speechEnd, heardStart: firstFrom,
		},
		{
			name:     "the first word of the piece stretched over the room",
			spans:    [][3]float64{{3.0, firstFrom, room}},
			heardEnd: speechEnd, heardStart: firstFrom,
		},
		{
			name:     "both stretched over the room",
			spans:    [][3]float64{{speechEnd, 2.6, room}, {3.0, firstFrom, room}},
			heardEnd: speechEnd, heardStart: firstFrom,
		},
		{
			name: "a sound of speech inside the stretched word is kept",
			spans: [][3]float64{
				{speechEnd, 1.5, room}, {1.5, inWordEnd, testSpeech}, {inWordEnd, 2.6, room}, {3.0, firstFrom, room},
			},
			heardEnd: inWordEnd, heardStart: firstFrom,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			spans := append([][3]float64{{0.2, speechEnd, testSpeech}, {2.6, 3.0, testSpeech}, {firstFrom, 5.2, testSpeech}},
				tt.spans...)
			grid := &cutGrid{levels: newSpeechLevels(soundOf(6, spans...)), fps: testFPS}
			require.NotNil(t, grid.levels)
			require.Greater(t, room, grid.levels.silence, "the room is not silence")
			require.Less(t, room, grid.levels.threshold, "the room is not speech")

			settings := testSettings()
			settings.MaxPause, settings.PadBefore, settings.PadAfter = 0.3, defaultPadBefore, defaultPadAfter

			end, next := grid.around(word("раз", 0.2, 2.0), word("два", 3.8, 4.6), 2.6, 3.0, settings)

			silence := (end - tt.heardEnd) + (tt.heardStart - next)
			assert.LessOrEqual(t, silence, settings.MaxPause+1e-6, "no long pause at the joint")
			assert.GreaterOrEqual(t, end-tt.heardEnd, tailMin-1e-6, "the speech of «раз» and its tail are kept")
			assert.GreaterOrEqual(t, tt.heardStart-next, leadMin-1e-6, "the speech of «два» and its lead are kept")
			assert.GreaterOrEqual(t, next, 3.0, "never into the removed word")

			src := words(word("раз", 0.2, 2.0), word("лишнее", 2.6, 3.0), word("два", 3.8, 4.6))

			plan, err := buildPlan(src, 6, settings, &Edit{Remove: []WordRange{{From: 1, To: 1}}}, grid)
			require.NoError(t, err)
			require.Len(t, plan.Keep, 2)

			reel := RemapTranscript(trimSilentEdges(src, plan.Keep, grid.levels), plan.Keep).Words
			require.Len(t, reel, 2, "both words stay in the subtitles")
		})
	}
}

// TestSilentEdgeQuietEnding (T3.26): a quiet ending up to maxQuietEnding after the last speech of a word is kept; a
// longer quiet stretch Whisper put into the word is the room, and the edge goes back to the speech.
func TestSilentEdgeQuietEnding(t *testing.T) {
	t.Parallel()

	const room = -52.0

	tests := []struct {
		name     string
		quietTo  float64
		expected float64
	}{
		{name: "a short quiet ending stays", quietTo: 1.1, expected: 1.1},
		{name: "a long quiet stretch is the room", quietTo: 1.8, expected: 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			levels := newSpeechLevels(soundOf(4, [3]float64{0.2, 1.0, testSpeech}, [3]float64{1.0, tt.quietTo, room},
				[3]float64{2.4, 3, testSpeech}))
			require.NotNil(t, levels)

			assert.InDelta(t, tt.expected, levels.speechEnd(1.8, 0.28, 2.0), 1e-9)
		})
	}
}

// TestTrimSilentEdges: only a silent edge cut off a kept word is trimmed; a word cut through its sound (a trim by
// time), a word with its middle kept and a word with less than minWordSound kept stay as they are.
func TestTrimSilentEdges(t *testing.T) {
	t.Parallel()

	levels := newSpeechLevels(soundOf(6, [3]float64{0.2, 1.0, testSpeech}, [3]float64{2.0, 3.0, testSpeech}))
	require.NotNil(t, levels)

	keep := []Interval{{Start: 0.1, End: 1.1}, {Start: 2.5, End: 2.55}}

	tests := []struct {
		name     string
		input    transcription.Word
		expected transcription.Word
	}{
		{name: "the middle kept", input: word("раз", 0.2, 1.0), expected: word("раз", 0.2, 1.0)},
		{name: "a silent end trimmed", input: word("раз", 0.2, 2.0), expected: word("раз", 0.2, 1.1)},
		{name: "cut through its sound", input: word("два", 0.9, 2.5), expected: word("два", 0.9, 2.5)},
		{name: "too little kept", input: word("три", 2.52, 2.9), expected: word("три", 2.52, 2.9)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := trimSilentEdges(words(tt.input), keep, levels)
			assert.Equal(t, tt.expected, got.Words[0])
		})
	}

	src := words(word("раз", 0.2, 2.0))
	assert.Same(t, src, trimSilentEdges(src, keep, nil), "nothing without levels")
}

// TestTailKeepsTheDecay: a phrase that dies away into digital silence (a quiet room, a gate on the phone) keeps its
// decay: the threshold is measured from the silence, so the fading sound is still speech, and the tail is cut after it.
func TestTailKeepsTheDecay(t *testing.T) {
	t.Parallel()

	const (
		decayFrom = 1.0
		decayTo   = 1.2
		digital   = -120.0
	)

	levels := soundOf(4, [3]float64{0.2, decayFrom, testSpeech}, [3]float64{2.4, 3, testSpeech})
	for i := range levels.DB {
		mid := (float64(i) + 0.5) * levels.Window

		switch {
		case mid >= decayFrom && mid < decayTo:
			levels.DB[i] = testSpeech + (digital-testSpeech)*(mid-decayFrom)/(decayTo-decayFrom)
		case levels.DB[i] == testFloor:
			levels.DB[i] = digital
		}
	}

	grid := &cutGrid{levels: newSpeechLevels(levels), fps: testFPS}
	require.NotNil(t, grid.levels)

	// Whisper ends the word before its sound does.
	end, _, ok := grid.pause(word("раз", 0.2, 0.95), word("два", 2.5, 3), 0.3)
	require.True(t, ok)

	audible := decayFrom + (decayTo-decayFrom)*(grid.levels.threshold-testSpeech)/(digital-testSpeech)
	assert.GreaterOrEqual(t, end, audible+tailMin-1e-6, "the decay down to the threshold (%.1f dB) is kept, with a tail",
		grid.levels.threshold)
}

// TestRoomNoiseOverDigitalSilence: a quarter of the sound is digital silence (a gate), the pauses between the
// phrases have the noise of the room (-65 dBFS): the floor is the room noise, not the silence, so the pause is cut.
func TestRoomNoiseOverDigitalSilence(t *testing.T) {
	t.Parallel()

	const (
		digital = -120.0
		room    = -65.0
	)

	levels := soundOf(10, [3]float64{0, 1, digital}, [3]float64{1, 4, testSpeech}, [3]float64{4, 5, room},
		[3]float64{5, 8, testSpeech}, [3]float64{8, 8.5, room}, [3]float64{8.5, 10, digital})

	floor, _, ok := levelStats(levels)
	require.True(t, ok)
	assert.InDelta(t, room, floor, 1e-9, "the floor is the room noise")

	grid := &cutGrid{levels: newSpeechLevels(levels), fps: testFPS}
	require.NotNil(t, grid.levels)
	assert.InDelta(t, -47.0, grid.levels.threshold, 1e-9, "40 percent of the way from the room noise to the speech")

	end, next, ok := grid.pause(word("раз", 1, 4), word("два", 5, 8), 0.3)
	require.True(t, ok, "the pause with the room noise is cut")
	assert.InDelta(t, breathPause, (end-4)+(5-next), 0.5/testFPS+1e-6, "a breath is left")
}

func TestLevelStatsDigitalSilence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    *ffmpeg.Levels
		expected float64
	}{
		{
			name:     "no digital silence: the 10th percentile",
			input:    soundOf(10, [3]float64{1, 4, testSpeech}, [3]float64{5, 8, testSpeech}),
			expected: testFloor,
		},
		{
			// The audible windows are the speech only: their floor is not the noise, the floor is held at -90.
			name:     "all the pauses are digital silence",
			input:    soundOf(10, [3]float64{0, 10, -120}, [3]float64{1, 4, testSpeech}, [3]float64{5, 8, -30}),
			expected: digitalFloor,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			floor, _, ok := levelStats(tt.input)
			require.True(t, ok)
			assert.InDelta(t, tt.expected, floor, 1e-9)
		})
	}
}

func TestSoundLevelsOf(t *testing.T) {
	t.Parallel()

	got := soundLevelsOf(soundOf(10, [3]float64{1, 4, testSpeech}, [3]float64{5, 8, testSpeech}))
	assert.Equal(t, &SoundLevels{NoiseFloor: testFloor, Speech: testSpeech, Threshold: -44, Used: true}, got)

	flat := soundLevelsOf(soundOf(5, [3]float64{0, 5, testSpeech}))
	require.NotNil(t, flat)
	assert.False(t, flat.Used)
	assert.Nil(t, soundLevelsOf(nil))
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
