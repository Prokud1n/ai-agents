package subtitles

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Whisper writes the speech of the trainer without the hyphens of «кто-то» and «онлайн-тренировки» and without «ё»,
// and capitalizes the start of a sentence it did not end with a period («что-то скажет Да не скажет»). fixOrthography
// (layout.orthography) fixes the frequent cases by a dictionary, in the subtitles only: the hook and the headings are
// written by the editor's model.

// hyphenSuffixes are the particles written with a hyphen after a pronoun or an adverb: «кто-то», «как-нибудь».
var hyphenSuffixes = wordSet("то", "нибудь", "либо")

// hyphenHosts take a particle of hyphenSuffixes.
var hyphenHosts = wordSet(
	"кто", "что", "как", "где", "куда", "откуда", "когда", "зачем", "почему", "сколько", "кого", "кому", "кем",
	"чего", "чему", "чем", "ком", "чей", "чья", "чье", "чьи", "какой", "какая", "какое", "какие", "какого",
	"какую", "каким", "каких", "какими", "каком",
)

// hyphenPairs are two words always written with a hyphen.
var hyphenPairs = map[string]bool{
	"изза": true, "изпод": true, "вопервых": true, "вовторых": true, "втретьих": true, "вчетвертых": true,
	"впятых": true, "подругому": true, "поразному": true, "поновому": true, "попрежнему": true,
	"понастоящему": true, "всетаки": true,
}

// hyphenBeforeBreak are the pairs written with a hyphen only as an aside, before a comma or the end of a sentence:
// «по-моему,» but «по моему опыту».
var hyphenBeforeBreak = wordSet("помоему", "потвоему", "посвоему", "понашему", "повашему")

// compoundPrefixes are the first parts of compound nouns («онлайн-тренировка», «фитнес-клуб»): written with a hyphen
// before a word that starts with one of their stems.
var compoundPrefixes = map[string][]string{
	"онлайн": {"тренир", "тренер", "заняти", "курс", "формат", "марафон", "консульт", "программ", "урок", "школ",
		"сопровожд", "ведени", "встреч", "клиент"},
	"офлайн":  {"тренир", "тренер", "заняти", "формат", "встреч"},
	"оффлайн": {"тренир", "тренер", "заняти", "формат", "встреч"},
	"фитнес":  {"тренер", "клуб", "зал", "браслет", "трекер", "модел", "блог", "индустри", "тренир"},
}

// yoWords are words always written with «ё» in the subtitles.
var yoWords = map[string]string{
	"еще": "ещё", "идет": "идёт", "придет": "придёт", "пойдет": "пойдёт", "подойдет": "подойдёт",
	"найдет": "найдёт", "дает": "даёт", "живет": "живёт", "мое": "моё", "твое": "твоё", "свое": "своё",
}

// yoBefore: «все» is «всё» before these words («всё равно», «всё получится»); yoAfter: after these words at the end of
// a phrase («это всё», «вот и всё»).
var (
	yoBefore = wordSet("равно", "таки", "время", "еще", "хорошо", "получится", "понятно", "просто", "будет",
		"нормально", "отлично", "сделаю", "сделаем", "зависит")
	yoAfter = wordSet("это", "и", "вот")
)

// sentenceStarters are words that start a sentence when Whisper writes them with a capital in the middle of one: not
// names, so their capital means a sentence it did not end.
var sentenceStarters = wordSet("ну", "ведь", "кстати", "итак", "первое", "второе", "третье", "смотри", "слушай",
	"окей", "хорошо", "ладно", "нет")

// fixOrthography returns the tokens with the dictionary's hyphens and «ё» (two words joined into one keep the time of
// both and the punctuation of the second) and a sentence break before a function word Whisper capitalized in the middle
// of a sentence. tokens is not changed.
func fixOrthography(tokens []token) []token {
	out := make([]token, 0, len(tokens))

	for i := 0; i < len(tokens); i++ {
		t := tokens[i]

		if i+1 < len(tokens) {
			if joined, ok := joinHyphen(t, tokens[i+1]); ok {
				out = append(out, joined)
				i++

				continue
			}
		}

		t.Text = withYo(tokens, i)

		if len(out) > 0 && out[len(out)-1].Break != strongBreak && startsSentence(t) {
			out[len(out)-1].Break = strongBreak
		}

		out = append(out, t)
	}

	return out
}

// joinHyphen joins two words written with a hyphen into one token; false when they are written apart.
func joinHyphen(a, b token) (token, bool) {
	if a.Break != noBreak || gapBreaks(a, b) || !hyphenated(a.Norm, b) {
		return token{}, false
	}

	first := a.Text
	if a.Norm == "все" {
		first = replaceWord(first, "всё") // «всё-таки»
	}

	text := first + "-" + b.Text

	return token{
		Text: text, Norm: normalize(text), Start: a.Start, End: b.End, Break: b.Break, Accent: max(a.Accent, b.Accent),
	}, true
}

// hyphenated: the word a and the token b are written with a hyphen by the dictionary.
func hyphenated(a string, b token) bool {
	switch {
	case hyphenHosts[a] && hyphenSuffixes[b.Norm]:
		// «то, что» is a pronoun of its own: «лучше, чем то, что было».
		return b.Norm != "то" || b.Break != softBreak
	case hyphenPairs[a+b.Norm]:
		return true
	case hyphenBeforeBreak[a+b.Norm]:
		return b.Break != noBreak
	case a == "кое":
		return b.Norm != ""
	}

	for _, stem := range compoundPrefixes[a] {
		if strings.HasPrefix(b.Norm, stem) {
			return true
		}
	}

	return false
}

// withYo is the text of the token i with «ё» where the dictionary has it; the case and the punctuation are kept.
func withYo(tokens []token, i int) string {
	t := tokens[i]

	if yo, ok := yoWords[t.Norm]; ok {
		return replaceWord(t.Text, yo)
	}

	if t.Norm != "все" {
		return t.Text
	}

	next := i+1 < len(tokens) && t.Break == noBreak && yoBefore[tokens[i+1].Norm]
	prev := i > 0 && tokens[i-1].Break == noBreak && yoAfter[tokens[i-1].Norm] && t.Break != noBreak

	if next || prev {
		return replaceWord(t.Text, "всё")
	}

	return t.Text
}

// replaceWord puts the word (lower case) in place of the letters of text, keeping the punctuation around them and a
// capital first letter.
func replaceWord(text, word string) string {
	start := strings.IndexFunc(text, unicode.IsLetter)
	if start < 0 {
		return text
	}

	end := start
	for end < len(text) {
		r, size := utf8.DecodeRuneInString(text[end:])
		if !unicode.IsLetter(r) {
			break
		}

		end += size
	}

	if r, _ := utf8.DecodeRuneInString(text[start:]); unicode.IsUpper(r) {
		first, size := utf8.DecodeRuneInString(word)
		word = string(unicode.ToUpper(first)) + word[size:]
	}

	return text[:start] + word + text[end:]
}

// startsSentence: a function word written with one capital (not a name: «Да», «А», «Ну»), so a sentence starts at it.
func startsSentence(t token) bool {
	first, _ := utf8.DecodeRuneInString(strings.TrimLeftFunc(t.Text, func(r rune) bool { return !unicode.IsLetter(r) }))
	if !unicode.IsUpper(first) || capitals(t.Text) != 1 {
		return false
	}

	n := t.Norm

	return proclitics[n] || enclitics[n] || stopWords[n] || sentenceStarters[n]
}
