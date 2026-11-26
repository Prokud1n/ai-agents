package editplan

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// A rough search of the phrases said again, for the model's cleaning of a first version: the model decides by the
// meaning, the list only keeps it from missing a take.
const (
	// maxRepeatGap between the starts of two takes of one thought, seconds.
	maxRepeatGap = 90.0
	// minRepeatShare of the content words of the shorter phrase found in the other one.
	minRepeatShare = 0.6
	// minRepeatWords: the phrases share at least this many content words (or are the same words, minSameWords of them).
	minRepeatWords = 2
	minSameWords   = 2
	// minContentRunes: shorter words («и», «в», «на») are not compared.
	minContentRunes = 3
	// A word is compared without its ending: a word of shortStemRunes letters without the last one, a longer one
	// without the last two, so the forms of a word match («спину» and «спина», «ноги» and «ногу»).
	shortStemRunes = 4
	// minSeries similar phrases in a row are an enumeration or a refrain, not a failed take.
	minSeries = 3
	// maxRunWords of a group of words said again in a row inside a phrase («ну привет ну привет»).
	maxRunWords = 3
	// maxRepeatHints and maxShootingHints listed in the prompt.
	maxRepeatHints   = 12
	maxShootingHints = 10
)

// shootingWords are the words about the shooting itself (normalized: «ё» is «е»).
var shootingWords = []string{
	"начинаем", "начали", "поехали", "стоп", "заново", "мотор", "снято", "дубль", "дубля", "дубли", "дублей", "камера",
	"камеру", "записываю", "записываем", "записываешь", "снимаешь", "снимаем", "снимаю", "снимай", "снимешь",
	"перезапишу", "перезапишем",
}

// countdowns are the counts before a take: «раз, два, три» or «1, 2, 3».
var countdowns = [][]string{{"раз", "два", "три"}, {"1", "2", "3"}}

// stopWords are the frequent words that say nothing of the thought: two phrases sharing them are not a repeat.
var stopWords = []string{
	"это", "этот", "эта", "эти", "что", "как", "для", "вас", "вам", "нас", "нам", "так", "там", "тут", "все", "всем",
	"еще", "уже", "очень", "меня", "мне", "тебя", "тебе", "себя", "если", "когда", "чтобы", "или", "она", "они", "оно",
	"его", "ее", "мой", "моя", "наш", "ваш", "вот", "просто", "даже", "есть", "был", "была", "было", "будет", "быть",
	"тоже", "только", "можно", "нужно", "надо", "сейчас", "потому", "который", "которая", "которые", "где", "чем",
	"при", "про", "без", "под", "над", "раз", "всего", "всегда", "давайте", "ну", "вообще",
}

// numberWords are the numerals and ordinal words: phrases that differ by them are the points of a list («страх номер
// один», «страх номер два»), not a repeat.
var numberWords = []string{
	"один", "одна", "одно", "два", "две", "три", "четыре", "пять", "шесть", "семь", "восемь", "девять", "десять",
	"первый", "первая", "первое", "первую", "второй", "вторая", "второе", "вторую", "третий", "третья", "третье",
	"третью", "четвертый", "четвертая", "четвертое", "пятый", "пятая", "пятое", "во-первых", "во-вторых", "в-третьих",
}

// normPhrase is a phrase of the prompt kept in the reel with its words normalized: all of them, the stems of the
// content words, and the numbers.
type normPhrase struct {
	from, to int
	all      []string
	stems    []string
	numbers  []string
}

// writeRepeats lists the hints for the cleaning of a first talking head: the phrases that look said again, the words
// said again in a row inside a phrase, and the phrases about the shooting.
func writeRepeats(b *strings.Builder, req *Request) {
	if req.IsRevision() || previousEdit(req.Plan).Mode == roughcut.WorkoutMode || len(req.Transcript.Words) == 0 {
		return
	}

	words := req.Transcript.Words
	list := normalizedPhrases(req.Transcript, req.Plan)

	if pairs := similarPairs(words, list); len(pairs) > 0 {
		b.WriteString("\nПохожие фразы — подсказка для чистки (раздел «Чистка дублей и подготовки»): та же мысль, " +
			"сказанная ещё раз, — возможно, неудачный дубль. Проверь по смыслу и оставь последний полный и удачный " +
			"вариант; осознанный повтор не трогай:\n")

		for i := range pairs {
			fmt.Fprintf(b, "- %s ↔ %s\n", phraseRef(words, &pairs[i][0]), phraseRef(words, &pairs[i][1]))
		}
	}

	writeHintList(b, "\nПовтор подряд внутри фразы (оставь одно повторение, если это не счёт и не акцент):\n", words,
		filterPhrases(list, func(p *normPhrase) bool { return repeatsInside(p.all) }, maxRepeatHints))
	writeHintList(b, "\nПохоже на слова про съёмку — отсчёт, «начинаем», «дубль», «заново» (проверь: это не всегда "+
		"подготовка):\n", words, filterPhrases(list, func(p *normPhrase) bool { return aboutShooting(p.all) },
		maxShootingHints))
}

// writeHintList writes the header and the phrases; nothing without phrases.
func writeHintList(b *strings.Builder, header string, words []transcription.Word, list []normPhrase) {
	if len(list) == 0 {
		return
	}

	b.WriteString(header)

	for i := range list {
		b.WriteString("- " + phraseRef(words, &list[i]) + "\n")
	}
}

// phraseRef: «слова 12–19 (0:14) «…»».
func phraseRef(words []transcription.Word, p *normPhrase) string {
	return fmt.Sprintf("слова %s (%s) «%s»", indexRange(p.from, p.to), roughcut.FormatTimecode(words[p.from].Start),
		joinWords(words[p.from:p.to+1]))
}

// normalizedPhrases are the phrases of the prompt kept in the reel, normalized.
func normalizedPhrases(transcript *transcription.Result, plan *roughcut.Plan) []normPhrase {
	all := phrases(transcript, plan)
	out := make([]normPhrase, 0, len(all))

	for _, ph := range all {
		if ph.key.interval < 0 {
			continue
		}

		p := normPhrase{from: ph.from, to: ph.to}

		for _, w := range transcript.Words[ph.from : ph.to+1] {
			p.add(normalizeWord(w.Word))
		}

		out = append(out, p)
	}

	return out
}

// add puts a normalized word into the phrase: a number, a content word by its stem, or only into all.
func (p *normPhrase) add(word string) {
	if word == "" {
		return
	}

	p.all = append(p.all, word)

	switch {
	case isNumber(word):
		p.numbers = append(p.numbers, word)
	case isContent(word):
		p.stems = append(p.stems, stem(word))
	}
}

// stem is the word without its ending: shortStemRunes letters lose the last one, longer words the last two.
func stem(word string) string {
	runes := []rune(word)

	switch {
	case len(runes) > shortStemRunes:
		return string(runes[:len(runes)-2])
	case len(runes) == shortStemRunes:
		return string(runes[:len(runes)-1])
	default:
		return word
	}
}

// isNumber: digits, a numeral or an ordinal word.
func isNumber(word string) bool {
	return slices.Contains(numberWords, word) || !strings.ContainsFunc(word, func(r rune) bool { return !unicode.IsDigit(r) })
}

// normalizeWord: lower case, «ё» as «е», without the punctuation and symbols around it.
func normalizeWord(word string) string {
	trimmed := strings.TrimFunc(word, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })

	return strings.ReplaceAll(strings.ToLower(trimmed), "ё", "е")
}

// similarPairs pairs every phrase with the most similar one said after it within maxRepeatGap; the maxRepeatHints most
// similar pairs, in the order of the speech. A series of minSeries or more similar phrases in a row is left out.
func similarPairs(words []transcription.Word, list []normPhrase) [][2]normPhrase {
	series := seriesOf(list)

	type scored struct {
		first, second int
		share         float64
	}

	var found []scored

	for i := range list {
		if series[i] {
			continue
		}

		best, bestShare := -1, 0.0

		for j := i + 1; j < len(list); j++ {
			if words[list[j].from].Start-words[list[i].from].Start > maxRepeatGap {
				break
			}

			if share := similarity(&list[i], &list[j]); !series[j] && share > bestShare {
				best, bestShare = j, share
			}
		}

		if best >= 0 {
			found = append(found, scored{first: i, second: best, share: bestShare})
		}
	}

	slices.SortStableFunc(found, func(a, b scored) int { return cmp.Compare(b.share, a.share) })
	found = found[:min(len(found), maxRepeatHints)]
	slices.SortFunc(found, func(a, b scored) int { return cmp.Compare(a.first, b.first) })

	out := make([][2]normPhrase, 0, len(found))
	for _, f := range found {
		out = append(out, [2]normPhrase{list[f.first], list[f.second]})
	}

	return out
}

// seriesOf marks the phrases of every run of minSeries or more neighbours, each similar to the next one.
func seriesOf(list []normPhrase) []bool {
	marked := make([]bool, len(list))

	for from := 0; from < len(list); {
		to := from
		for to+1 < len(list) && similarity(&list[to], &list[to+1]) > 0 {
			to++
		}

		if to-from+1 >= minSeries {
			for k := from; k <= to; k++ {
				marked[k] = true
			}
		}

		from = to + 1
	}

	return marked
}

// similarity of two phrases: 0 for phrases with different numbers (the points of a list); 1 for the same words; the
// share of the shorter phrase's content stems found in the other one when it is at least minRepeatShare; 0 otherwise.
func similarity(a, b *normPhrase) float64 {
	if len(a.numbers)+len(b.numbers) > 0 && commonCount(a.numbers, b.numbers) != max(len(a.numbers), len(b.numbers)) {
		return 0
	}

	if len(a.all) >= minSameWords && slices.Equal(a.all, b.all) {
		return 1
	}

	shorter := min(len(a.stems), len(b.stems))
	if shorter < minRepeatWords {
		return 0
	}

	common := commonCount(a.stems, b.stems)
	share := float64(common) / float64(shorter)

	if common < minRepeatWords || share < minRepeatShare {
		return 0
	}

	return share
}

// commonCount is the number of words of a found in b, each word of b matched once.
func commonCount(a, b []string) int {
	counts := make(map[string]int, len(b))
	for _, w := range b {
		counts[w]++
	}

	n := 0

	for _, w := range a {
		if counts[w] > 0 {
			counts[w]--
			n++
		}
	}

	return n
}

// repeatsInside: a group of up to maxRunWords words with a content word is said twice in a row («ну привет ну привет»).
func repeatsInside(all []string) bool {
	for size := 1; size <= maxRunWords; size++ {
		for i := 0; i+2*size <= len(all); i++ {
			group := all[i : i+size]
			if slices.Equal(group, all[i+size:i+2*size]) && slices.ContainsFunc(group, isContent) {
				return true
			}
		}
	}

	return false
}

// isContent: a word of minContentRunes letters or more that is not a stop word.
func isContent(word string) bool {
	return len([]rune(word)) >= minContentRunes && !slices.Contains(stopWords, word)
}

// aboutShooting: the phrase has a word about the shooting or a countdown («раз, два, три», «1, 2, 3»).
func aboutShooting(all []string) bool {
	for i, w := range all {
		if slices.Contains(shootingWords, w) {
			return true
		}

		for _, count := range countdowns {
			if i+len(count) <= len(all) && slices.Equal(all[i:i+len(count)], count) {
				return true
			}
		}
	}

	return false
}

// filterPhrases are up to limit phrases that match.
func filterPhrases(list []normPhrase, match func(*normPhrase) bool, limit int) []normPhrase {
	var out []normPhrase

	for i := range list {
		if len(out) < limit && match(&list[i]) {
			out = append(out, list[i])
		}
	}

	return out
}
