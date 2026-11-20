package roughcut

import (
	"strings"
	"unicode"

	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// Synthetic speech for tests: every word lasts testWordLen, words follow each other after testWordGap,
// "|" is a pause of testPause (a phrase boundary), "||" a long pause of testLongPause.
const (
	testWordLen   = 0.4
	testWordGap   = 0.05
	testPause     = 0.6
	testLongPause = 2.0
	testStart     = 0.5
	testTail      = 1.0

	testLanguage = "ru"
	testClip     = "clip.mp4"
	testSource   = "a.mp4"
	testFiller   = "э"
)

func testSettings() Settings {
	return Settings{
		MaxPause:         0.4,
		PadBefore:        defaultPadBefore,
		PadAfter:         defaultPadAfter,
		RetakeSimilarity: 0.7,
		Fillers:          []string{testFiller, "эм", unitFiller, "хм"},
	}
}

// speech turns "давайте при… | давайте приступим." into a transcript with one segment per phrase.
// Like Whisper, words lose their punctuation and the segment text keeps it.
func speech(text string) *transcription.Result {
	result := &transcription.Result{Language: testLanguage}
	t := testStart
	phrase := []transcription.Word{}
	raw := []string{}

	flush := func() {
		if len(phrase) == 0 {
			return
		}

		result.Segments = append(result.Segments, transcription.Segment{
			Start: phrase[0].Start, End: phrase[len(phrase)-1].End, Text: " " + strings.Join(raw, " "),
		})
		phrase, raw = phrase[:0], raw[:0]
	}

	for _, w := range strings.Fields(text) {
		switch w {
		case "|":
			flush()

			t += testPause - testWordGap
		case "||":
			flush()

			t += testLongPause - testWordGap
		default:
			bare := strings.TrimFunc(w, unicode.IsPunct)
			word := transcription.Word{Word: bare, Start: roundTime(t), End: roundTime(t + testWordLen)}
			result.Words = append(result.Words, word)
			phrase = append(phrase, word)
			raw = append(raw, w)
			t += testWordLen + testWordGap
		}
	}

	flush()

	result.Duration = roundTime(t + testTail)
	result.Text = text

	return result
}

// keptText is the text of the words a plan keeps.
func keptText(src *transcription.Result, keep []Interval) string {
	remapped := RemapTranscript(src, keep)

	return joinWords(remapped.Words)
}
