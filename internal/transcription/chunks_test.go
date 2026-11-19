package transcription

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/providers/whisper"
)

const (
	wordHello    = "Привет"
	textHello    = "Привет!"
	segmentHello = " Привет!"
	wordSquat    = "присед"
	wordKnees    = "колени"
	wordOne      = "раз"
)

func TestChunkDurationLimit(t *testing.T) {
	t.Parallel()

	const mb = 1 << 20

	tests := []struct {
		name     string
		duration float64
		size     int64
		expected float64
	}{
		{name: "short audio is capped by max chunk duration", duration: 300, size: 1 * mb, expected: 600},
		{name: "size limit is stricter", duration: 3600, size: 200 * mb, expected: 405}, // 3600 * 25/200 * 0.9
		{name: "never shorter than the minimum", duration: 3600, size: 10000 * mb, expected: 60},
		{name: "unknown size", duration: 3600, size: 0, expected: 600},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.InDelta(t, tt.expected, chunkDurationLimit(tt.duration, tt.size, 25*mb), 1e-9)
		})
	}
}

func TestPlanChunks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		duration float64
		maxChunk float64
		silences []ffmpeg.Silence
		expected []chunk
	}{
		{
			name:     "fits into one chunk",
			duration: 95.5, maxChunk: 600,
			expected: []chunk{{Start: 0, End: 95.5}},
		},
		{
			name:     "cuts in the middle of the last pause in the second half",
			duration: 1500, maxChunk: 600,
			silences: []ffmpeg.Silence{{Start: 100, End: 101}, {Start: 400, End: 401}, {Start: 550, End: 552}, {Start: 1100, End: 1101}},
			expected: []chunk{{Start: 0, End: 551}, {Start: 551, End: 1100.5}, {Start: 1100.5, End: 1500}},
		},
		{
			name:     "pauses in the first half and past the limit are ignored",
			duration: 700, maxChunk: 600,
			silences: []ffmpeg.Silence{{Start: 100, End: 102}, {Start: 640, End: 650}},
			expected: []chunk{{Start: 0, End: 600}, {Start: 598, End: 700}},
		},
		{
			name:     "no pauses: fixed cuts with overlap",
			duration: 1300, maxChunk: 600,
			expected: []chunk{{Start: 0, End: 600}, {Start: 598, End: 1198}, {Start: 1196, End: 1300}},
		},
		{
			name:     "short tail is glued to the only chunk",
			duration: 620, maxChunk: 600,
			expected: []chunk{{Start: 0, End: 620}},
		},
		{
			name:     "short tail is glued to the last chunk",
			duration: 1210, maxChunk: 600,
			expected: []chunk{{Start: 0, End: 600}, {Start: 598, End: 1210}},
		},
		{
			name:     "too small limit is raised to the minimum",
			duration: 100, maxChunk: 1,
			expected: []chunk{{Start: 0, End: 60}, {Start: 58, End: 100}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, planChunks(tt.duration, tt.maxChunk, tt.silences))
		})
	}
}

func TestMergeChunks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []chunkTranscript
		expected Result
	}{
		{
			name: "single chunk: text from segments, spaces trimmed",
			input: []chunkTranscript{{
				Chunk: chunk{Start: 0, End: 4},
				Transcript: whisper.Transcript{
					Text: " Привет! Сегодня присед. ",
					Words: []whisper.Word{
						{Word: " " + wordHello, Start: 0, End: 0.8}, {Word: "Сегодня", Start: 1.5, End: 2}, {Word: wordSquat, Start: 2, End: 2.6},
					},
					Segments: []whisper.Segment{{Start: 0, End: 1, Text: segmentHello}, {Start: 1.5, End: 2.6, Text: " Сегодня присед."}},
				},
			}},
			expected: Result{
				Text:     "Привет! Сегодня присед.",
				Words:    []Word{{Word: wordHello, Start: 0, End: 0.8}, {Word: "Сегодня", Start: 1.5, End: 2}, {Word: wordSquat, Start: 2, End: 2.6}},
				Segments: []Segment{{Start: 0, End: 1, Text: textHello}, {Start: 1.5, End: 2.6, Text: "Сегодня присед."}},
			},
		},
		{
			name: "chunks cut in a pause are shifted by their start",
			input: []chunkTranscript{
				{
					Chunk: chunk{Start: 0, End: 551},
					Transcript: whisper.Transcript{
						Words:    []whisper.Word{{Word: "Раз", Start: 549.1, End: 549.6}},
						Segments: []whisper.Segment{{Start: 549, End: 549.7, Text: " Раз."}},
					},
				},
				{
					Chunk: chunk{Start: 551, End: 900},
					Transcript: whisper.Transcript{
						Words:    []whisper.Word{{Word: "Два", Start: 1.3, End: 1.7}},
						Segments: []whisper.Segment{{Start: 1.2, End: 1.8, Text: " Два."}},
					},
				},
			},
			expected: Result{
				Text:     "Раз. Два.",
				Words:    []Word{{Word: "Раз", Start: 549.1, End: 549.6}, {Word: "Два", Start: 552.3, End: 552.7}},
				Segments: []Segment{{Start: 549, End: 549.7, Text: "Раз."}, {Start: 552.2, End: 552.8, Text: "Два."}},
			},
		},
		{
			// Fixed cut at 600 with the next chunk from 598: both chunks hear [598, 600], the boundary is 599.
			name: "segments overlapping the junction are clipped, the overlap is not repeated",
			input: []chunkTranscript{
				{
					Chunk: chunk{Start: 0, End: 600},
					Transcript: whisper.Transcript{
						Words: []whisper.Word{
							{Word: "Опускаемся", Start: 595.2, End: 596},
							{Word: "медленно", Start: 596.1, End: 597.4},
							{Word: "вниз,", Start: 597.5, End: 598.2},
							{Word: "держим", Start: 598.9, End: 599.5},
							{Word: "спи", Start: 599.6, End: 600},
						},
						Segments: []whisper.Segment{{Start: 595, End: 600, Text: " Опускаемся медленно вниз, держим спи"}},
					},
				},
				{
					Chunk: chunk{Start: 598, End: 900},
					Transcript: whisper.Transcript{
						Words: []whisper.Word{
							{Word: "низ,", Start: 0, End: 0.2},
							{Word: "держим", Start: 0.95, End: 1.5},
							{Word: "спину", Start: 1.6, End: 2.1},
							{Word: "ровно.", Start: 2.2, End: 2.7},
						},
						Segments: []whisper.Segment{
							{Start: 0, End: 2.8, Text: " низ, держим спину ровно."},
							{Start: 3.5, End: 4.5, Text: " Выдох."},
						},
					},
				},
			},
			expected: Result{
				Text: "Опускаемся медленно вниз, держим спину ровно. Выдох.",
				Words: []Word{
					{Word: "Опускаемся", Start: 595.2, End: 596},
					{Word: "медленно", Start: 596.1, End: 597.4},
					{Word: "вниз,", Start: 597.5, End: 598.2},
					{Word: "держим", Start: 598.9, End: 599.5},
					{Word: "спину", Start: 599.6, End: 600.1},
					{Word: "ровно.", Start: 600.2, End: 600.7},
				},
				Segments: []Segment{
					{Start: 595, End: 599, Text: "Опускаемся медленно вниз, держим"},
					{Start: 599, End: 600.8, Text: "спину ровно."},
					{Start: 601.5, End: 602.5, Text: "Выдох."},
				},
			},
		},
		{
			name: "word heard by both chunks at the junction is kept once",
			input: []chunkTranscript{
				{
					Chunk:      chunk{Start: 0, End: 600},
					Transcript: whisper.Transcript{Words: []whisper.Word{{Word: wordKnees, Start: 598.8, End: 599.4}}},
				},
				{
					Chunk: chunk{Start: 598, End: 700},
					Transcript: whisper.Transcript{
						Words: []whisper.Word{{Word: "Колени.", Start: 1.05, End: 1.5}, {Word: wordKnees, Start: 3, End: 3.5}},
					},
				},
			},
			expected: Result{
				Words:    []Word{{Word: wordKnees, Start: 598.8, End: 599.4}, {Word: wordKnees, Start: 601, End: 601.5}},
				Segments: []Segment{},
			},
		},
		{
			name: "real repetition across the junction without a pause is kept",
			input: []chunkTranscript{
				{
					Chunk: chunk{Start: 0, End: 600},
					Transcript: whisper.Transcript{Words: []whisper.Word{
						{Word: wordOne, Start: 598.6, End: 598.95}, {Word: wordOne, Start: 599, End: 599.4},
					}},
				},
				{
					Chunk: chunk{Start: 598, End: 700},
					Transcript: whisper.Transcript{Words: []whisper.Word{
						{Word: wordOne, Start: 0.6, End: 0.95}, {Word: wordOne, Start: 1, End: 1.4},
					}},
				},
			},
			expected: Result{
				Words:    []Word{{Word: wordOne, Start: 598.6, End: 598.95}, {Word: wordOne, Start: 599, End: 599.4}},
				Segments: []Segment{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, mergeChunks(tt.input))
		})
	}
}

func TestPromptTail(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("слово ", promptWords+5)

	tests := []struct {
		name          string
		input         string
		expectedWords int
	}{
		{name: "empty", input: "", expectedWords: 0},
		{name: "short text is kept", input: "Держим спину ровно.", expectedWords: 3},
		{name: "long text is cut to the tail", input: long + "конец", expectedWords: promptWords},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := promptTail(tt.input)

			assert.Len(t, strings.Fields(got), tt.expectedWords)
		})
	}
}
