package subtitles

import "github.com/Prokud1n/ai-agents/internal/transcription"

const (
	wordKnees = "колени"
	wordHold  = "держим"
	wordBack  = "спину"
	wordOne   = "раз"
	wordTwo   = "два"
	wordLower = "Опускаемся"

	// repeated test words (goconst)
	wV             = "в"
	wI             = "и"
	wNe            = "не"
	wDelaem        = "делаем"
	wPovtoreniy    = "повторений"
	wTri           = "три"
	wChetyre       = "четыре"
	wPyat          = "пять"
	wSem           = "семь"
	wVosem         = "восемь"
	wPrivetCapExcl = "Привет!"
	wSegodnyaCap   = "Сегодня"
	wMedlennoComma = "медленно,"
	wPodnimaemsya  = "поднимаемся"
	wBystro        = "быстро"
	wPlavno        = "плавно"
	wRazCapComma   = "Раз,"
	wDvaComma      = "два,"
	wMedlenno      = "медленно"
	wRovno         = "ровно"
	wTyanem        = "тянем"
	wNosok         = "носок"
	wRovnoy        = "ровной"
	wZavalivaem    = "заваливаем"
	wPrised        = "присед"
	wSo            = "со"
	wShtangoy      = "штангой"
	wDelaemCap     = "Делаем"
	wPodhoda       = "подхода"
	wVes           = "вес"
	wYagoditsy     = "ягодицы"
)

// tok is a token with the norm computed from its text.
func tok(text string, start, end float64) token {
	return token{Text: text, Norm: normalize(text), Start: start, End: end}
}

func withBreak(t token, b breakKind) token {
	t.Break = b

	return t
}

// openEnd marks the strong break of the token as the end of a segment without a sentence mark.
func openEnd(t token) token {
	t.open = true

	return t
}

func withAccent(t token, a accentKind) token {
	t.Accent = a

	return t
}

// texts returns the shown text of tokens.
func texts(tokens []token) []string {
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, t.Text)
	}

	return out
}

// lineTexts returns every line of every cue as a string.
func lineTexts(cues []cue) [][]string {
	out := make([][]string, 0, len(cues))

	for _, c := range cues {
		lines := make([]string, 0, len(c.Lines))
		for _, line := range c.Lines {
			s := ""
			for i, t := range line {
				if i > 0 {
					s += " "
				}

				s += t.Text
			}

			lines = append(lines, s)
		}

		out = append(out, lines)
	}

	return out
}

// speech makes a transcript of evenly spoken words: each word takes 0.3 s with 0.05 s between words;
// a segment boundary adds a 0.5 s pause. Segments are given with their punctuation.
func speech(segments ...string) *transcription.Result {
	const (
		wordDuration = 0.3
		wordGap      = 0.05
		segmentPause = 0.5
	)

	result := &transcription.Result{Language: "ru", Segments: []transcription.Segment{}, Words: []transcription.Word{}}
	at := 0.0

	for _, text := range segments {
		start := at

		for _, w := range splitWords(text) {
			result.Words = append(result.Words, transcription.Word{Word: w, Start: at, End: at + wordDuration})
			at += wordDuration + wordGap
		}

		result.Segments = append(result.Segments, transcription.Segment{Start: start, End: at, Text: text})
		at += segmentPause
	}

	result.Duration = at

	return result
}

// splitWords strips punctuation like Whisper word timestamps do.
func splitWords(text string) []string {
	var words []string

	word := []rune{}
	flush := func() {
		if len(word) > 0 {
			words = append(words, string(word))
			word = word[:0]
		}
	}

	for _, r := range text {
		switch {
		case r == ' ':
			flush()
		case normalize(string(r)) != "" || r == '-':
			word = append(word, r)
		}
	}

	flush()

	return words
}
