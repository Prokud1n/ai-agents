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
	// A tail shorter than this is glued to the previous chunk: a few seconds without context are
	// recognized worse, and the chunk stays far below the size limit.
	minTailDuration = 30.0

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

// planChunks splits [0, duration] into chunks of at most maxChunk (plus a glued short tail),
// preferring cuts in pauses.
func planChunks(duration, maxChunk float64, silences []ffmpeg.Silence) []chunk {
	maxChunk = max(maxChunk, minChunkDuration) // keeps chunks longer than the overlap, so the loop advances

	var chunks []chunk

	for start := 0.0; ; {
		limit := start + maxChunk
		if duration-limit < minTailDuration {
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
// Where chunks overlap, each owns the half of the overlap next to it: words are kept by the chunk whose
// range contains their start; segments are clipped to that range, and a clipped segment gets its text
// from the words kept in it, so the overlap is never transcribed twice.
func mergeChunks(parts []chunkTranscript) Result {
	// Empty slices, not nil: the JSON has [] for a video without speech.
	result := Result{Segments: []Segment{}, Words: []Word{}}

	for i := range parts {
		lower, upper := chunkBounds(parts, i)

		words := chunkWords(&parts[i], lower, upper, lastWord(result.Words))
		result.Words = append(result.Words, words...)
		result.Segments = append(result.Segments, chunkSegments(&parts[i], lower, upper, words)...)
	}

	texts := make([]string, 0, len(result.Segments))
	for _, s := range result.Segments {
		texts = append(texts, s.Text)
	}

	result.Text = strings.Join(texts, " ")

	return result
}

// chunkWords returns the words of a chunk in absolute time that start within [lower, upper).
// The first of them is dropped if it repeats prev, the last word kept from the previous chunk: both
// chunks heard the same word at the junction when it starts before prev ends. A real repetition
// ("раз, раз") starts after the previous word ends and is kept.
func chunkWords(part *chunkTranscript, lower, upper float64, prev *Word) []Word {
	words := make([]Word, 0, len(part.Transcript.Words))

	for _, w := range part.Transcript.Words {
		word := Word{
			Word:  strings.TrimSpace(w.Word),
			Start: roundTime(part.Chunk.Start + w.Start),
			End:   roundTime(part.Chunk.Start + w.End),
		}

		if word.Start < lower || word.Start >= upper {
			continue
		}

		if len(words) == 0 && prev != nil && isJunctionDuplicate(*prev, word) {
			continue
		}

		words = append(words, word)
	}

	return words
}

// chunkSegments returns the segments of a chunk in absolute time, clipped to [lower, upper).
func chunkSegments(part *chunkTranscript, lower, upper float64, words []Word) []Segment {
	segments := make([]Segment, 0, len(part.Transcript.Segments))

	for _, s := range part.Transcript.Segments {
		segment := Segment{
			Start: roundTime(part.Chunk.Start + s.Start),
			End:   roundTime(part.Chunk.Start + s.End),
			Text:  strings.TrimSpace(s.Text),
		}

		if segment.End <= lower || segment.Start >= upper {
			continue
		}

		if segment.Start < lower || segment.End > upper {
			segment = clipSegment(segment, lower, upper, words)
			if segment.Text == "" {
				continue
			}
		}

		segments = append(segments, segment)
	}

	return segments
}

// clipSegment cuts a segment to [lower, upper) and rebuilds its text from the kept words inside it.
// Whisper words carry no punctuation, so it is lost only at a chunk junction.
func clipSegment(s Segment, lower, upper float64, words []Word) Segment {
	clipped := Segment{Start: max(s.Start, lower), End: min(s.End, upper)}

	texts := make([]string, 0, len(words))
	for _, w := range words {
		if w.Start >= clipped.Start && w.Start < clipped.End {
			texts = append(texts, w.Word)
		}
	}

	clipped.Text = strings.Join(texts, " ")

	return clipped
}

// chunkBounds returns the absolute time range chunk i owns: overlaps with neighbors are split in half.
func chunkBounds(parts []chunkTranscript, i int) (lower, upper float64) {
	lower, upper = math.Inf(-1), math.Inf(1)

	if i > 0 {
		lower = (parts[i-1].Chunk.End + parts[i].Chunk.Start) / 2
	}

	if i < len(parts)-1 {
		upper = (parts[i].Chunk.End + parts[i+1].Chunk.Start) / 2
	}

	return lower, upper
}

func lastWord(words []Word) *Word {
	if len(words) == 0 {
		return nil
	}

	w := words[len(words)-1]

	return &w
}

func isJunctionDuplicate(prev, next Word) bool {
	return normalizeWord(prev.Word) == normalizeWord(next.Word) && next.Start < prev.End
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
