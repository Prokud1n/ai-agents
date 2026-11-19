package transcription

import (
	"strings"

	"github.com/Prokud1n/ai-agents/internal/providers/whisper"
)

// Whisper hallucinates text on silence and background music ("Продолжение следует...", subtitles credits).
// The thresholds are Whisper's own defaults for treating a window as silence or a failed decode.
const (
	noSpeechThreshold         = 0.6
	logprobThreshold          = -1.0
	compressionRatioThreshold = 2.4
)

func isHallucination(s whisper.Segment) bool {
	if s.NoSpeechProb > noSpeechThreshold && s.AvgLogprob < logprobThreshold {
		return true
	}

	// A highly compressible segment is the same phrase repeated in a loop.
	return s.CompressionRatio > compressionRatioThreshold
}

// dropHallucinations removes hallucinated segments and the words inside them.
func dropHallucinations(t *whisper.Transcript) whisper.Transcript {
	kept := make([]whisper.Segment, 0, len(t.Segments))
	dropped := make([]whisper.Segment, 0)

	for _, s := range t.Segments {
		if isHallucination(s) {
			dropped = append(dropped, s)

			continue
		}

		kept = append(kept, s)
	}

	if len(dropped) == 0 {
		return *t
	}

	words := make([]whisper.Word, 0, len(t.Words))

	for _, w := range t.Words {
		if !insideAny(w, dropped) {
			words = append(words, w)
		}
	}

	// The text feeds the next chunk's prompt: a hallucination there would be repeated.
	texts := make([]string, 0, len(kept))
	for _, s := range kept {
		texts = append(texts, strings.TrimSpace(s.Text))
	}

	return whisper.Transcript{
		Language: t.Language,
		Duration: t.Duration,
		Text:     strings.Join(texts, " "),
		Words:    words,
		Segments: kept,
	}
}

func insideAny(w whisper.Word, segments []whisper.Segment) bool {
	for _, s := range segments {
		if w.Start >= s.Start && w.Start < s.End {
			return true
		}
	}

	return false
}
