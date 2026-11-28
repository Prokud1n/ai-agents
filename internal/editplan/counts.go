package editplan

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// The announced number of points (T3.29). The trainer announces how many points follow («три правила», «пять
// ошибок») and numbers them at the start of a sentence («первое…», «во-вторых…», «и третье…»). A selection (or a
// revision for the length) that keeps the announcement but cuts some of the numbered points leaves the viewer counting
// three and seeing two. Such a decision gets a hint back once (like checkEnding: the same decision again is accepted)
// and a line in the details.
//
// The heuristic is conservative: the number is a word or digits from 2 to maxCount kept in the reel, followed by a
// word that is not a number or a measure of time, quantity or repetition (measureWords: «три раза в неделю», «два месяца»); the
// source has every ordinal from the first to that number at the start of a sentence within maxCountGap after it, and
// the reel keeps fewer of them. A number without its numbered points in the speech is never judged.
const (
	maxCount = 10
	// maxCountGap after the announcement the numbered points are looked for, seconds.
	maxCountGap = 120.0
	// announceWords quoted of the announcement: the number and the next two words («три важных правила»).
	announceWords = 3
	// minNounRunes: the word after the number is a word, not a letter or a particle.
	minNounRunes = 3
)

// countDetailFormat is the line of the decisions log.
const countDetailFormat = "Структура: оставлено объявление «%s» (слова %s), а пунктов с нумерацией — %d из %d"

// countNumbers are the numbers that announce points.
var countNumbers = map[string]int{
	twoWord: 2, "две": 2, threeWord: 3, "четыре": 4, "пять": 5, "шесть": 6, "семь": 7, "восемь": 8, "девять": 9,
	"десять": 10,
}

// ordinalWords number the points at the start of a sentence (normalized: «ё» is «е»).
var ordinalWords = map[string]int{
	"первое": 1, "первый": 1, "первая": 1, "во-первых": 1,
	"второе": 2, "второй": 2, "вторая": 2, "во-вторых": 2,
	"третье": 3, "третий": 3, "третья": 3, "в-третьих": 3,
	"четвертое": 4, "четвертый": 4, "четвертая": 4, "в-четвертых": 4,
	"пятое": 5, "пятый": 5, "пятая": 5, "в-пятых": 5,
	"шестое": 6, "в-шестых": 6, "седьмое": 7, "в-седьмых": 7, "восьмое": 8, "в-восьмых": 8, "девятое": 9,
	"в-девятых": 9, "десятое": 10, "в-десятых": 10,
}

// measureWords: a number followed by them counts time, quantity or repetitions, not points. Whole forms: a stem would
// also catch «разных», «частых», «годных».
var measureWords = []string{
	timesWord, "раза", "секунда", "секунды", "секунд", "секунду", "минута", "минуты", "минут", "минуту", "час", "часа",
	"часов", "день", "дня", "дней", "сутки", "суток", "неделя", "недели", "недель", "неделю", "месяц", "месяца",
	"месяцев", "год", "года", "лет", "кило", "килограмм", "килограмма", "килограммов", "грамм", "грамма", "граммов",
	"литр", "литра", "литров", "метр", "метра", "метров", "километр", "километра", "километров", "сантиметр",
	"сантиметра", "сантиметров", "шаг", "шага", "шагов", "подход", "подхода", "подходов", "повтор", "повтора",
	"повторов", "повторения", "повторений", "процент", "процента", "процентов", "рубль", "рубля", "рублей", "тысячи",
	"тысяч", "сотни", "сотен", "миллиона", "миллионов", "раунд", "раунда", "раундов", "круг", "круга", "кругов",
}

// leadWords may come before the ordinal at the start of a sentence («и третье»).
var leadWords = []string{"и", "а", "ну", "так", "итак"}

// announcement is a number of points kept in the reel: its word, the number, the numbered points of the source after
// it (by ordinal: the indices of its words) and the ordinals kept.
type announcement struct {
	word, count int
	points      map[int][]int
	kept        map[int]bool
}

// checkCounts adds the line about an announcement whose numbered points were cut to the details of the edit and returns
// the hint for the model; "" when there is none. Only a selection (or a length asked for) in a talking head is judged.
func checkCounts(req *Request, edit *roughcut.Edit, plan *roughcut.Plan, sel *selection) string {
	if !sel.on {
		return ""
	}

	words := req.Transcript.Words
	kept := keptSet(words, plan.Keep)

	a, ok := findBrokenCount(req.Transcript, kept)
	if !ok {
		return ""
	}

	head := roughcut.WordRange{From: a.word, To: min(a.word+announceWords-1, len(words)-1)}
	text := quote(words, head)
	edit.Details = append(edit.Details, fmt.Sprintf(countDetailFormat, text, indexRange(head.From, head.To),
		len(a.kept), a.count))

	cut := make([]string, 0, a.count)

	for n := 1; n <= a.count; n++ {
		if a.kept[n] {
			continue
		}

		indices := a.points[n]
		last := indices[len(indices)-1]
		cut = append(cut, fmt.Sprintf("слово %d «%s»", last, wordText(words, last)))
	}

	return fmt.Sprintf("структуру: в ролике осталось объявление «%s» (слова %s), а пунктов с нумерацией после него "+
		"оставлено %d из %d — вырезаны: %s. Зритель услышит одно число, а увидит другое: оставь все пункты с их "+
		"нумерацией (хотя бы по короткой фразе) — или вырежи и объявление числа, и лишние пункты", text,
		indexRange(head.From, head.To), len(a.kept), a.count, strings.Join(cut, ", "))
}

// findBrokenCount finds the first announcement kept in the reel whose numbered points are all in the source and not all
// in the reel.
func findBrokenCount(src *transcription.Result, kept []bool) (announcement, bool) {
	words := src.Words
	starts := sentenceStarts(src)

	for i := range words {
		count, ok := announced(words, i)
		if !ok || !kept[i] {
			continue
		}

		a := announcement{word: i, count: count, points: map[int][]int{}, kept: map[int]bool{}}

		for j := i + 1; j < len(words) && words[j].Start-words[i].Start <= maxCountGap; j++ {
			n, numbered := ordinalAt(words, starts, j)
			if !numbered || n > count {
				continue
			}

			a.points[n] = append(a.points[n], j)
			if kept[j] {
				a.kept[n] = true
			}
		}

		if len(a.points) == count && len(a.kept) < count {
			return a, true
		}
	}

	return announcement{}, false
}

// announced: the word i is a number of points — 2 to maxCount followed by a word that is not a number or a measure.
func announced(words []transcription.Word, i int) (int, bool) {
	word := normalizeWord(words[i].Word)

	count, ok := countNumbers[word]
	if !ok {
		if count, ok = digitCount(word); !ok {
			return 0, false
		}
	}

	if i+1 >= len(words) {
		return 0, false
	}

	next := normalizeWord(words[i+1].Word)
	if utf8.RuneCountInString(next) < minNounRunes || isNumber(next) || countNumbers[next] > 0 {
		return 0, false
	}

	if slices.Contains(measureWords, next) {
		return 0, false
	}

	return count, true
}

// digitCount: «3» is 3 when it is 2 to maxCount.
func digitCount(word string) (int, bool) {
	n, err := strconv.Atoi(word)
	if err != nil || strconv.Itoa(n) != word || n < 2 || n > maxCount {
		return 0, false
	}

	return n, true
}

// ordinalAt is the ordinal of the word j when it numbers a point: an ordinal word at the start of a sentence, after
// leadWords at most one.
func ordinalAt(words []transcription.Word, starts []bool, j int) (int, bool) {
	n, ok := ordinalWords[normalizeWord(words[j].Word)]
	if !ok {
		return 0, false
	}

	if starts[j] {
		return n, true
	}

	return n, j > 0 && starts[j-1] && slices.Contains(leadWords, normalizeWord(words[j-1].Word))
}

// sentenceStarts marks the words that start a sentence: the first word, a word after a sentence end
// (roughcut.SentenceEnds), after a pause longer than phraseGap or at the start of a segment.
func sentenceStarts(src *transcription.Result) []bool {
	words := src.Words
	ends := roughcut.SentenceEnds(src)
	starts := make([]bool, len(words))

	for i := range words {
		starts[i] = i == 0 || ends[i-1] || words[i].Start-words[i-1].End > phraseGap
	}

	for _, s := range src.Segments {
		for i, w := range words {
			if w.Start >= s.Start-segmentSlack {
				starts[i] = true

				break
			}
		}
	}

	return starts
}
