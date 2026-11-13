package transcription

import (
	"math"
	"strings"
	"unicode"

	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/providers/whisper"
)

// Long audio is split into chunks that fit the Whisper upload limit. Durations are in seconds.
const (
	// Shorter chunks than the size limit allows: a failed request is cheaper to retry and stays well
	// within the request timeout. Typical trainer videos (a few minutes) are one chunk.
	maxChunkDuration = 600.0
	minChunkDuration = 60.0
	sizeSafetyFactor = 0.9 // keeps a chunk below the limit despite VBR and container overhead
	// A chunk is cut in the middle of the last pause in its second half; without a pause it is cut
	// at the limit and the next chunk starts chunkOverlap earlier, so no word is lost at the cut.
	minCutFraction = 0.5
	chunkOverlap   = 2.0
	// A word repeated by both chunks at a junction has nearly the same start time in each of them.
	junctionTolerance = 0.5

	msPerSecond = 1000
	promptWords = 40 // tail of the previous chunk passed to Whisper as context
)

type chunk struct {
	Start float64
	End   float64
}

type chunkTranscript struct {
	Chunk      chunk
	Transcript whisper.Transcript
}

// chunkDurationLimit returns the longest chunk that fits maxSize, given that size bytes hold duration seconds.
func chunkDurationLimit(duration float64, size, maxSize int64) float64 {
	if duration <= 0 || size <= 0 {
		return maxChunkDuration
	}

	bySize := duration * float64(maxSize) / float64(size) * sizeSafetyFactor

	return max(min(bySize, maxChunkDuration), minChunkDuration)
}

// planChunks splits [0, duration] into chunks of at most maxChunk, preferring cuts in pauses.
func planChunks(duration, maxChunk float64, silences []ffmpeg.Silence) []chunk {
	maxChunk = max(maxChunk, minChunkDuration) // keeps chunks longer than the overlap, so the loop advances

	if duration <= maxChunk {
		return []chunk{{Start: 0, End: duration}}
	}

	var chunks []chunk

	for start := 0.0; ; {
		limit := start + maxChunk
		if limit >= duration {
			return append(chunks, chunk{Start: start, End: duration})
		}

		if cut, ok := findPauseCut(silences, start+maxChunk*minCutFraction, limit); ok {
			chunks = append(chunks, chunk{Start: start, End: cut})
			start = cut

			continue
		}

		chunks = append(chunks, chunk{Start: start, End: limit})
		start = limit - chunkOverlap
	}
}

// findPauseCut returns the middle of the last pause whose middle lies in [from, to].
func findPauseCut(silences []ffmpeg.Silence, from, to float64) (float64, bool) {
	cut, found := 0.0, false

	for _, s := range silences {
		mid := (s.Start + s.End) / 2
		if mid >= from && mid <= to && mid > cut {
			cut, found = mid, true
		}
	}

	return cut, found
}

// mergeChunks joins chunk transcripts into one, shifting times by the chunk start.
// Where chunks overlap, each one owns the half of the overlap next to it: a word is kept by the chunk
// whose range contains its start, a segment by the one that contains its middle. A word still repeated
// by both chunks at the junction is dropped once.
func mergeChunks(parts []chunkTranscript) Result {
	// Empty slices, not nil: the JSON has [] for a video without speech.
	result := Result{Segments: []Segment{}, Words: []Word{}}

	segmentTexts := make([]string, 0, len(parts))
	lastWordChunk := -1

	for i := range parts {
		lower, upper := chunkBounds(parts, i)
		offset := parts[i].Chunk.Start

		for _, w := range parts[i].Transcript.Words {
			word := Word{Word: strings.TrimSpace(w.Word), Start: roundTime(offset + w.Start), End: roundTime(offset + w.End)}
			if word.Start < lower || word.Start >= upper {
				continue
			}

			if lastWordChunk != i && len(result.Words) > 0 && isSameWord(result.Words[len(result.Words)-1], word) {
				continue
			}

			result.Words = append(result.Words, word)
			lastWordChunk = i
		}

		for _, s := range parts[i].Transcript.Segments {
			segment := Segment{Start: roundTime(offset + s.Start), End: roundTime(offset + s.End), Text: strings.TrimSpace(s.Text)}
			if mid := (segment.Start + segment.End) / 2; mid < lower || mid >= upper {
				continue
			}

			result.Segments = append(result.Segments, segment)
			segmentTexts = append(segmentTexts, segment.Text)
		}
	}

	if len(parts) == 1 {
		result.Text = strings.TrimSpace(parts[0].Transcript.Text)
	} else {
		result.Text = strings.Join(segmentTexts, " ")
	}

	return result
}

// chunkBounds returns the absolute time range chunk i owns: overlaps with neighbors are split in half.
func chunkBounds(parts []chunkTranscript, i int) (float64, float64) {
	lower, upper := math.Inf(-1), math.Inf(1)

	if i > 0 {
		lower = (parts[i-1].Chunk.End + parts[i].Chunk.Start) / 2
	}

	if i < len(parts)-1 {
		upper = (parts[i].Chunk.End + parts[i+1].Chunk.Start) / 2
	}

	return lower, upper
}

func isSameWord(prev, next Word) bool {
	return normalizeWord(prev.Word) == normalizeWord(next.Word) && math.Abs(next.Start-prev.Start) <= junctionTolerance
}

func normalizeWord(w string) string {
	return strings.ToLower(strings.TrimFunc(w, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}))
}

// roundTime drops float noise from adding offsets (12.300000000001 -> 12.3).
func roundTime(seconds float64) float64 {
	return math.Round(seconds*msPerSecond) / msPerSecond
}

// promptTail returns the last words of a chunk transcript to give Whisper context for the next chunk.
func promptTail(text string) string {
	words := strings.Fields(text)
	if len(words) > promptWords {
		words = words[len(words)-promptWords:]
	}

	return strings.Join(words, " ")
}
