package subtitles

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Whisper writes the speech of the trainer without the hyphens of «кто-то» and «онлайн-тренировки» and without «ё»,
// and capitalizes the start of a sentence it did not end with a period («что-то спросит Ну тогда поедем»). fixOrthography
// (layout.orthography) fixes the frequent cases by a dictionary; the hook, the notes and the headings are written by
// the editor's model and get only the words of the dictionary (textOrthography), not the sentences.

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
	"онлайн": slices.Concat(offlineStems, []string{"тренинг", "курс", "марафон", "консульт", "программ", "урок", "школ",
		"сопровожд", "ведени", "клиент"}),
	"офлайн":  offlineStems,
	"оффлайн": offlineStems,
	"фитнес":  {"тренер", "клуб", "зал", "браслет", "трекер", "модел", "блог", "индустри", "тренировк"},
}

// offlineStems are the stems after «офлайн» (and «онлайн»): nouns only — «тренировк», not the verb «тренироваться».
var offlineStems = []string{"тренировк", "тренер", "заняти", "формат", "встреч"}

// yoWords are words always written with «ё» in the subtitles; yoStems are stems written with «ё» in all their forms
// («тренажёр», «тренажёрами», «тренажёрный»).
var (
	yoWords = map[string]string{
		"еще": "ещё", "идет": "идёт", "придет": "придёт", "пойдет": "пойдёт", "подойдет": "подойдёт",
		"найдет": "найдёт", "дает": "даёт", "живет": "живёт", "мое": "моё", "твое": "твоё", "свое": "своё",
		"четвертое": "четвёртое", "четвертый": "четвёртый",
	}
	yoStems = map[string]string{"тренажер": "тренажёр"}
)

// respellings are words Whisper writes in a spelling of before: «оффлайн» is «офлайн».
var respellings = map[string]string{"оффлайн": "офлайн"}

// compoundMaxGap: the two parts of a compound noun («онлайн | тренировка») are joined with a hyphen across a pause up
// to this long and across the end of a Whisper segment without a sentence mark (a slow question under a heading).
const compoundMaxGap = 1.0

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
//
// The speech of the subtitles is the only text from Whisper; the hook, the notes and the headings are written by the
// editor's model and get the dictionary part only (textOrthography). A sentence that is clearly a question gets its
// «?» (questionMarks).
func fixOrthography(tokens []token) []token {
	return questionMarks(orthographyWords(tokens))
}

// textOrthography is the dictionary part of fixOrthography for a text written on the screen (the hook, a note, a
// heading): the hyphens, «ё» and the spellings of its words; the words are not changed.
func textOrthography(words []string) []string {
	tokens := make([]token, 0, len(words))
	for _, w := range words {
		tokens = append(tokens, token{Text: w, Norm: normalize(w), Break: breakAfter(w)})
	}

	out := make([]string, 0, len(words))
	for _, t := range orthographyWords(tokens) {
		out = append(out, t.Text)
	}

	return out
}

// orthographyWords is fixOrthography without the question marks.
func orthographyWords(source []token) []token {
	tokens := respell(source)
	out := make([]token, 0, len(tokens))

	for i := 0; i < len(tokens); i++ {
		t := tokens[i]

		var before token // the word before t; none at the start
		if i > 0 {
			before = tokens[i-1]
		}

		if i+1 < len(tokens) && !conjunctionThen(before, t, tokens[i+1]) {
			if joined, ok := joinHyphen(t, tokens[i+1]); ok {
				out = append(out, joined)
				i++

				continue
			}
		}

		t.Text = withYo(tokens, i)

		if len(out) > 0 && (out[len(out)-1].Break != strongBreak || out[len(out)-1].open) && startsSentence(t) {
			out[len(out)-1].Break, out[len(out)-1].open = strongBreak, false
		}

		out = append(out, t)
	}

	return out
}

// thenAfterWhat: «что то» after these is a conjunction and a pronoun («потому что то упражнение»).
var thenAfterWhat = wordSet("потому", "так", "оттого")

// conjunctionThen: «что», «чем» or «как» before «то» is a conjunction, not «что-то»: after «потому»/«так»/«оттого»
// («потому что то упражнение») or, for «чем» and «как», after a comma («сложнее, чем то упражнение»).
func conjunctionThen(before, a, b token) bool {
	if b.Norm != "то" {
		return false
	}

	switch a.Norm {
	case wordWhat:
		return thenAfterWhat[before.Norm] && before.Break == noBreak
	case "чем", "как":
		return before.Break == softBreak
	default:
		return false
	}
}

// respell is the tokens with the spellings of respellings; tokens is not changed.
func respell(tokens []token) []token {
	out := slices.Clone(tokens)

	for i, t := range out {
		if word, ok := respellings[t.Norm]; ok {
			out[i].Text, out[i].Norm = replaceWord(t.Text, word), word
		}
	}

	return out
}

// joinHyphen joins two words written with a hyphen into one token; false when they are written apart. The parts of
// a compound noun are joined across a pause up to compoundMaxGap and across the end of a segment without a sentence
// mark (Whisper splits a slow phrase there: «онлайн | тренировка»), other words only when said together.
func joinHyphen(a, b token) (token, bool) {
	together := a.Break == noBreak && !gapBreaks(a, b)
	if compound(a.Norm, b) {
		together = (a.Break == noBreak || a.open) && b.Start-a.End <= compoundMaxGap
	}

	if !together || !hyphenated(a.Norm, b) {
		return token{}, false
	}

	first := a.Text
	if a.Norm == "все" {
		first = replaceWord(first, "всё") // «всё-таки»
	}

	text := first + "-" + b.Text

	return token{
		Text: text, Norm: normalize(text), Start: a.Start, End: b.End, Break: b.Break, Accent: max(a.Accent, b.Accent),
		open: b.open,
	}, true
}

// compound: the word a and the token b are the parts of a compound noun of compoundPrefixes.
func compound(a string, b token) bool {
	return slices.ContainsFunc(compoundPrefixes[a], func(stem string) bool { return strings.HasPrefix(b.Norm, stem) })
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

	return compound(a, b)
}

// withYo is the text of the token i with «ё» where the dictionary has it; the case and the punctuation are kept.
func withYo(tokens []token, i int) string {
	t := tokens[i]

	if yo, ok := yoWords[t.Norm]; ok {
		return replaceWord(t.Text, yo)
	}

	for stem, yo := range yoStems {
		if strings.HasPrefix(t.Norm, stem) && normalize(firstWord(t.Text)) == t.Norm {
			return replaceWord(t.Text, yo+strings.TrimPrefix(t.Norm, stem))
		}
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

// firstWord is the first run of letters of the text («тренажерами,» → «тренажерами»).
func firstWord(text string) string {
	start := strings.IndexFunc(text, unicode.IsLetter)
	if start < 0 {
		return ""
	}

	end := strings.IndexFunc(text[start:], func(r rune) bool { return !unicode.IsLetter(r) })
	if end < 0 {
		return text[start:]
	}

	return text[start : start+end]
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

// Whisper often ends a question with a period. questionMarks puts «?» at the end of a sentence that is clearly a
// question by its first words: «а что если…», «что если…», «а вдруг…», «почему…», «зачем…», «сколько…», «откуда…»,
// «разве…», «неужели…», «можно ли…»; «а если…» only without a comma (not «а если честно, …», not «а если болит, делайте
// мягче»), «почему…» not with its answer in it («почему так, потому что…») and not «сколько бы ни…». A sentence ending
// with «!», «?» or «…» stays as it is.
// The words of the question openers: «а что если», «а если».
const (
	wordAnd  = "а"
	wordWhat = "что"
	wordIf   = "если"
)

var (
	questionOpeners  = [][]string{{wordAnd, wordWhat, wordIf}, {wordWhat, wordIf}, {wordAnd, "вдруг"}}
	questionWords    = wordSet("почему", "зачем", "сколько", "откуда", "разве", "неужели")
	questionAnswers  = wordSet("потому", "ни")
	conditionalAside = wordSet("честно", "серьезно", "коротко", "кратко", "проще", "откровенно")
)

// questionMarks returns the tokens with «?» at the end of the sentences that are questions (asksQuestion); tokens is
// not changed.
func questionMarks(tokens []token) []token {
	out := slices.Clone(tokens)
	start := 0

	for i, t := range out {
		if i+1 < len(out) && (t.Break != strongBreak || t.open) {
			continue
		}

		if asksQuestion(out[start : i+1]) {
			out[i].Text = withQuestionMark(t.Text)
			out[i].Break, out[i].open = strongBreak, false
		}

		start = i + 1
	}

	return out
}

// asksQuestion: the words of a sentence (without a mark of its own other than a period) are a question.
func asksQuestion(sentence []token) bool {
	if mark := sentenceMark(sentence[len(sentence)-1].Text); mark != "" && mark != "." {
		return false
	}

	norms := make([]string, 0, len(sentence))
	for _, t := range sentence {
		norms = append(norms, t.Norm)
	}

	if norms[0] == "ну" && len(norms) > 1 {
		norms = norms[1:]
	}

	for _, opener := range questionOpeners {
		if len(norms) > len(opener) && slices.Equal(norms[:len(opener)], opener) {
			return true
		}
	}

	switch {
	case len(norms) > 2 && norms[0] == wordAnd && norms[1] == wordIf:
		return !conditionalAside[norms[2]] && !slices.ContainsFunc(sentence[:len(sentence)-1], func(t token) bool {
			return t.Break == softBreak
		})
	case questionWords[norms[0]], len(norms) > 2 && norms[1] == "ли":
		return !slices.ContainsFunc(norms, func(n string) bool { return questionAnswers[n] })
	default:
		return false
	}
}

// sentenceMark is the mark that ends the text: «.», «!», «?», «…» (or «...»), «» for none.
func sentenceMark(text string) string {
	trimmed := strings.TrimRightFunc(text, func(r rune) bool { return strings.ContainsRune(`"'»”)`, r) })

	switch {
	case strings.HasSuffix(trimmed, "..."):
		return "…"
	case breakAfter(trimmed) == strongBreak:
		return lastRune(trimmed)
	default:
		return ""
	}
}

// withQuestionMark is the text with «?» in place of its period or after its last letter.
func withQuestionMark(text string) string {
	end := strings.LastIndexFunc(text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) })
	if end < 0 {
		return text
	}

	_, size := utf8.DecodeRuneInString(text[end:])
	rest := strings.ReplaceAll(text[end+size:], ".", "")

	return text[:end+size] + "?" + rest
}
