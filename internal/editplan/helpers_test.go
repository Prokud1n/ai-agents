package editplan

import (
	"math"
	"strings"
	"unicode"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// Synthetic speech: every word lasts 0.4 s, words follow after 0.05 s, "|" is a 0.6 s pause, "||" a 2 s one; the
// first word starts at 0.5 s. Like Whisper, words lose their punctuation, the segment text keeps it.
const (
	testWordLen   = 0.4
	testWordGap   = 0.05
	testPause     = 0.6
	testLongPause = 2.0
	testStart     = 0.5
	testTail      = 1.0
	msPerSecond   = 1000
)

// testSpeech has a disputed repeat (words 0–2 said again as 3–5) and a filler (word 6):
//
//	0 держим 1 спину 2 ровно | 3 держим 4 спину 5 ровно || 6 эээ 7 делаем 8 по 9 десять 10 приседаний
const testSpeech = "держим спину ровно. | держим спину ровно. || эээ делаем по десять приседаний."

func testSettings() roughcut.Settings {
	return roughcut.Settings{
		MaxPause: 0.4, PadBefore: 0.08, PadAfter: 0.12, RetakeSimilarity: 0.7, Fillers: []string{"э", "эм"},
	}
}

func speech(text string) *transcription.Result {
	result := &transcription.Result{Language: "ru"}
	t := testStart

	var (
		phrase []transcription.Word
		raw    []string
	)

	flush := func() {
		if len(phrase) == 0 {
			return
		}

		result.Segments = append(result.Segments, transcription.Segment{
			Start: phrase[0].Start, End: phrase[len(phrase)-1].End, Text: " " + strings.Join(raw, " "),
		})
		phrase, raw = nil, nil
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
			word := transcription.Word{
				Word: strings.TrimFunc(w, unicode.IsPunct), Start: round(t), End: round(t + testWordLen),
			}
			result.Words = append(result.Words, word)
			phrase = append(phrase, word)
			raw = append(raw, w)
			t += testWordLen + testWordGap
		}
	}

	flush()

	result.Duration = round(t + testTail)

	return result
}

func round(seconds float64) float64 {
	return math.Round(seconds*msPerSecond) / msPerSecond
}

// testRequest is the first version of testSpeech with the automatic plan; edit is the previous versions' edit of a
// revision (nil: the first version).
func testRequest(version int, edit *roughcut.Edit) *Request {
	src := speech(testSpeech)
	settings := testSettings()

	plan, err := roughcut.BuildPlanEdited(src, src.Duration, settings, orEmpty(edit))
	if err != nil {
		panic(err)
	}

	plan.Edit = edit

	return &Request{TaskID: 7, Version: version, Transcript: src, Plan: &plan}
}

func orEmpty(edit *roughcut.Edit) *roughcut.Edit {
	if edit == nil {
		return &roughcut.Edit{}
	}

	return edit
}
