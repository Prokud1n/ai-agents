package editplan

import (
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
	// stemRunes of a word are compared, so the forms of a word match («ровно», «ровная»).
	stemRunes = 5
	// maxRunWords of a group of words said again in a row inside a phrase («ну привет ну привет»).
	maxRunWords = 3
	// maxRepeatHints and maxShootingHints listed in the prompt.
	maxRepeatHints   = 12
	maxShootingHints = 10
)

// shootingWords are the words about the shooting itself; shootingStems start such words in any form.
var (
	shootingWords = []string{"начинаем", "начали", "поехали", "стоп", "заново", "мотор", "снято"}
	shootingStems = []string{"дубл", "камер", "снима", "снимем", "перезап", "записыва"}
	// countdown is the count before a take: «раз, два, три».
	countdown = []string{"раз", "два", "три"}
)

// normPhrase is a phrase of the prompt kept in the reel with its words normalized: all of them, and the stems of the
// content words.
type normPhrase struct {
	from, to int
	all      []string
	stems    []string
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
			word := normalizeWord(w.Word)
			if word == "" {
				continue
			}

			p.all = append(p.all, word)

			if runes := []rune(word); len(runes) >= minContentRunes {
				p.stems = append(p.stems, string(runes[:min(len(runes), stemRunes)]))
			}
		}

		out = append(out, p)
	}

	return out
}

// normalizeWord: lower case, «ё» as «е», without the punctuation and symbols around it.
func normalizeWord(word string) string {
	trimmed := strings.TrimFunc(word, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })

	return strings.ReplaceAll(strings.ToLower(trimmed), "ё", "е")
}

// similarPairs pairs every phrase with the most similar one said after it within maxRepeatGap; maxRepeatHints pairs at
// most.
func similarPairs(words []transcription.Word, list []normPhrase) [][2]normPhrase {
	var out [][2]normPhrase

	for i := range list {
		if len(out) >= maxRepeatHints {
			break
		}

		best, bestShare := -1, 0.0

		for j := i + 1; j < len(list); j++ {
			if words[list[j].from].Start-words[list[i].from].Start > maxRepeatGap {
				break
			}

			if share := similarity(&list[i], &list[j]); share > bestShare {
				best, bestShare = j, share
			}
		}

		if best >= 0 {
			out = append(out, [2]normPhrase{list[i], list[best]})
		}
	}

	return out
}

// similarity of two phrases: 1 for the same words, the share of the shorter phrase's content stems found in the other
// one when it is at least minRepeatShare, 0 otherwise.
func similarity(a, b *normPhrase) float64 {
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

func isContent(word string) bool {
	return len([]rune(word)) >= minContentRunes
}

// aboutShooting: the phrase has a word about the shooting or the countdown «раз, два, три».
func aboutShooting(all []string) bool {
	for i, w := range all {
		if slices.Contains(shootingWords, w) || slices.ContainsFunc(shootingStems, func(stem string) bool {
			return strings.HasPrefix(w, stem)
		}) {
			return true
		}

		if i+len(countdown) <= len(all) && slices.Equal(all[i:i+len(countdown)], countdown) {
			return true
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
