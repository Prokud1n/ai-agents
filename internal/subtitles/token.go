package subtitles

import (
	"math"
	"strings"
	"unicode"

	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// breakKind is how strongly the text breaks after a word.
type breakKind int

const (
	noBreak breakKind = iota
	// softBreak: a comma, colon, semicolon or dash — a good place to start the next subtitle.
	softBreak
	// strongBreak: the end of a sentence or of a Whisper segment — the next subtitle always starts here.
	strongBreak
)

// accentKind is why a word is accented; the order is the priority of automatic accents.
type accentKind int

const (
	noAccent accentKind = iota
	explicitAccent
	numberAccent
	dictionaryAccent
	longestAccent
)

// token is a spoken word with the punctuation recovered from the segments.
type token struct {
	Text   string // as shown: the word with its punctuation
	Norm   string // lower case letters and digits, ё → е: for matching
	Start  float64
	End    float64
	Break  breakKind
	Accent accentKind
	// open: the strong break is only the end of a Whisper segment, without a sentence mark: the sentence may go on.
	open bool
	// leans: the word holds on to the next one as a preposition does: a subject pronoun after a conjunction («и я |
	// буду»; markLeaningSubjects, the trainer's phrases only).
	leans bool
}

// segmentWord is a word of a segment text with its punctuation; a dash or ellipsis written apart is appended
// to the word before it.
type segmentWord struct {
	text string
	norm string
}

// tokensFromTranscript turns transcript words into tokens. Whisper words carry no punctuation, so it is taken from
// the segment texts. Words are first assigned to segments by time (the middle of the word, see assignSegments),
// then aligned with the segment words by their normalized forms (longest common subsequence), so a mismatch
// in one segment never shifts the others. An unmatched word keeps its bare text; the last word of a segment
// always ends with a strong break. Words keep Whisper's order (it is the order of the text); a start earlier
// than the previous word's is moved up to it, and an end is never before the start.
func tokensFromTranscript(result *transcription.Result) []token {
	tokens := make([]token, 0, len(result.Words))

	for _, w := range result.Words {
		text := cleanWord(w.Word)
		if text == "" {
			continue
		}

		start := w.Start
		if len(tokens) > 0 {
			start = max(start, tokens[len(tokens)-1].Start)
		}

		tokens = append(tokens, token{Text: text, Norm: normalize(text), Start: start, End: max(w.End, start)})
	}

	bySegment := assignSegments(tokens, result.Segments)

	for seg, idx := range bySegment {
		if len(idx) == 0 {
			continue
		}

		punctuate(tokens, idx, splitSegment(result.Segments[seg].Text))
		last := idx[len(idx)-1]
		tokens[last].Break = strongBreak
	}

	// Obscene words are masked on the screen («х*йня»); Norm keeps the word for matching.
	for i := range tokens {
		tokens[i].Text = censor(tokens[i].Text)
	}

	markOpenEnds(tokens)

	return tokens
}

// markOpenEnds marks the ends of segments that do not end a sentence (token.open): without a sentence mark, the next
// segment going on with a small letter («онлайн» | «тренировка хуже»). A next segment with a capital starts a sentence
// («спросите» | «Отвечу…»), so the end stays a sentence end. tokens is owned by the caller.
func markOpenEnds(tokens []token) {
	for i := range tokens[:max(len(tokens)-1, 0)] {
		t := &tokens[i]
		t.open = t.Break == strongBreak && breakAfter(t.Text) != strongBreak && capitals(tokens[i+1].Text) == 0
	}
}

// cleanWord collapses whitespace and drops backslashes: spoken text has none, and in ASS they start escapes.
func cleanWord(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, `\`, "")), " ")
}

// assignSegments returns, per segment, the indexes of the tokens whose middle falls into it. Word and segment
// times of Whisper differ by up to half a second, so a segment owns the time from the middle of the pause before
// it to the middle of the pause after it: every word gets the nearest segment.
func assignSegments(tokens []token, segments []transcription.Segment) [][]int {
	bySegment := make([][]int, len(segments))
	if len(segments) == 0 {
		return bySegment
	}

	// upper[i] is where segment i ends and segment i+1 begins; the last one owns the rest.
	upper := make([]float64, len(segments))
	for i := range segments {
		upper[i] = math.Inf(1)
		if i+1 < len(segments) {
			upper[i] = (segments[i].End + segments[i+1].Start) / 2
		}
	}

	for i, t := range tokens {
		mid := (t.Start + t.End) / 2
		seg := 0

		for seg < len(segments)-1 && mid >= upper[seg] {
			seg++
		}

		bySegment[seg] = append(bySegment[seg], i)
	}

	return bySegment
}

func splitSegment(text string) []segmentWord {
	var words []segmentWord

	for _, f := range strings.Fields(cleanWord(text)) {
		n := normalize(f)
		if n == "" && len(words) > 0 {
			words[len(words)-1].text += " " + f

			continue
		}

		if n != "" {
			words = append(words, segmentWord{text: f, norm: n})
		}
	}

	return words
}

// punctuate gives the tokens idx (one segment) the text of the segment words they align with.
// tokens is owned by the caller (tokensFromTranscript builds it).
func punctuate(tokens []token, idx []int, segWords []segmentWord) {
	norms := make([]string, len(idx))
	for i, ti := range idx {
		norms[i] = tokens[ti].Norm
	}

	segNorms := make([]string, len(segWords))
	for j, w := range segWords {
		segNorms[j] = w.norm
	}

	for _, pair := range alignLCS(norms, segNorms) {
		t := &tokens[idx[pair[0]]]
		t.Text = segWords[pair[1]].text
		t.Break = breakAfter(t.Text)
	}
}

// alignLCS returns index pairs (i in a, j in b) of a longest common subsequence of equal strings.
func alignLCS(a, b []string) [][2]int {
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}

	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	var pairs [][2]int

	for i, j := 0, 0; i < len(a) && j < len(b); {
		switch {
		case a[i] == b[j]:
			pairs = append(pairs, [2]int{i, j})
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			i++
		default:
			j++
		}
	}

	return pairs
}

func breakAfter(text string) breakKind {
	trimmed := strings.TrimRightFunc(text, func(r rune) bool { return strings.ContainsRune(`"'»”)`, r) })

	switch {
	case strings.HasSuffix(trimmed, "...") || strings.ContainsAny(lastRune(trimmed), ".!?…"):
		return strongBreak
	case strings.ContainsAny(lastRune(trimmed), ",;:—–-"):
		return softBreak
	default:
		return noBreak
	}
}

func lastRune(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return ""
	}

	return string(r[len(r)-1])
}

// normalize keeps lower case letters and digits, with ё as е.
func normalize(s string) string {
	var b strings.Builder

	for _, r := range strings.ToLower(s) {
		switch {
		case r == 'ё':
			b.WriteRune('е')
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}

	return b.String()
}

func isNumber(norm string) bool {
	if norm == "" {
		return false
	}

	if numberWords[norm] {
		return true
	}

	for _, r := range norm {
		if !unicode.IsDigit(r) {
			return false
		}
	}

	return true
}

// isCompoundNumber: two number words that are one number, tens or hundreds and then a smaller number word
// ("двадцать пять", "сто двадцать"). Digits are never compound: "1 2" is counting.
func isCompoundNumber(prev, next string) bool {
	return tensAndHundreds[prev] && numberWords[next]
}

// isCommand is an imperative a trainer repeats all the time: never accented as the longest word.
func isCommand(norm string) bool { return commandVerbs[norm] }

func isProclitic(norm string) bool { return proclitics[norm] }

// holdsNext: no line or subtitle ends after the word: a preposition, a conjunction, «не» or a leaning pronoun.
func holdsNext(t token) bool { return isProclitic(t.Norm) || t.leans }

func isEnclitic(norm string) bool { return enclitics[norm] }

// isSignificant is a word that may be accented as the longest one: not a function word.
func isSignificant(norm string) bool {
	return norm != "" && !proclitics[norm] && !enclitics[norm] && !stopWords[norm] && !isNumber(norm)
}

func wordSet(words ...string) map[string]bool {
	set := make(map[string]bool, len(words))
	for _, w := range words {
		set[w] = true
	}

	return set
}

// Russian function words. Proclitics (prepositions, conjunctions, negation) are never left at the end of a line:
// they go with the next word. Enclitic particles stay with the previous word.
var (
	proclitics = wordSet(
		"в", "во", "на", "к", "ко", "с", "со", "у", "о", "об", "обо", "от", "ото", "по", "за", "из", "изо", "до",
		"без", "для", "при", "про", "под", "подо", "над", "надо", "перед", "через", "между", "из-за", "изза",
		"и", "а", "но", "да", "или", "либо", "не", "ни", "что", "чтобы", "как", "если", "когда", "чем",
	)
	enclitics = wordSet("же", "ж", "ли", "ль", "бы", "б")

	numberWords = wordSet(
		"ноль", "один", "одна", "одно", "два", "две", "три", "четыре", "пять", "шесть", "семь", "восемь", "девять",
		"десять", "одиннадцать", "двенадцать", "пятнадцать", "двадцать", "тридцать", "сорок", "пятьдесят",
		"шестьдесят", "семьдесят", "восемьдесят", "девяносто", "сто", "двести", "триста", "тысяча", "полтора",
		"полторы", "тринадцать", "четырнадцать", "шестнадцать", "семнадцать", "восемнадцать", "девятнадцать",
	)
	tensAndHundreds = wordSet(
		"двадцать", "тридцать", "сорок", "пятьдесят", "шестьдесят", "семьдесят", "восемьдесят", "девяносто",
		"сто", "двести", "триста",
	)

	// commandVerbs are the trainer's routine commands: "держим", "тянем" are said in every video.
	commandVerbs = wordSet(
		"держим", "держите", "тянем", "тяните", "делаем", "делайте", "опускаем", "опускаемся", "поднимаем",
		"поднимаемся", "вдыхаем", "выдыхаем", "дышим", "сгибаем", "разгибаем", "напрягаем", "расслабляем",
		"следим", "смотрим", "повторяем", "задерживаем", "фиксируем", "работаем", "начинаем", "заканчиваем",
		"отдыхаем", "толкаем", "давим", "садимся", "встаем", "возвращаемся", "продолжаем", "переходим",
	)

	// stopWords are frequent words that carry no meaning of their own: never accented as the longest word.
	stopWords = wordSet(
		"это", "этот", "эта", "эти", "этого", "этой", "этому", "этом", "этих", "этим", "тот", "та", "те", "того",
		"который", "которая", "которое", "которые", "которого", "которой", "которых", "которым", "которую",
		"потому", "поэтому", "почему", "сейчас", "сегодня", "теперь", "потом", "всегда", "никогда", "иногда",
		"просто", "только", "тоже", "также", "очень", "вообще", "конечно", "например", "значит", "кстати",
		"немного", "примерно", "особенно", "действительно", "наверное", "вот", "вам", "вас", "ваш", "ваша", "ваши",
		"вашей", "вашего", "нам", "нас", "наш", "наша", "наши", "нашей", "нашего", "себя", "себе", "свой", "своя",
		"свои", "своей", "своих", "своего", "мне", "меня", "тебя", "тебе", "его", "ее", "ему", "ней", "них",
		"они", "она", "оно", "мы", "вы", "ты", "он", "я", "есть", "быть", "был", "была", "было", "были", "будет",
		"будем", "будут", "буду", "можно", "нужно", "надо", "должны", "должен", "давайте", "давай", "хочу",
		"хотите", "какой", "какая", "какое", "какие", "такой", "такая", "такое", "такие", "более", "менее",
		"самый", "самая", "самое", "самые", "здесь", "там", "тут", "где", "куда", "когда", "уже", "еще",
		"после", "около", "всего", "всех", "всем", "все", "весь", "вся", "делаем", "делать", "делаете",
		"смотрите", "смотри", "ребята", "друзья", "привет", "спасибо", "итак",
		"the", "and", "you", "your", "this", "that", "with", "for", "are", "was", "will", "just",
	)
)
