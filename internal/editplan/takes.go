package editplan

import (
	"slices"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// maxTakeGap between the starts of a failed take and the take said again that the cleaning floor and the hint about
// the end of a sentence count as one take (takeWords), seconds: a take is restarted right away; the hints of the prompt
// look further (maxRepeatGap) only for the model to check.
const maxTakeGap = 20.0

// takeWords marks the words the cleaning may cut as failed takes (T3.26), by the hints of the cleaning and the reel
// after it (kept):
//   - of a similar pair (similarPairs) within maxTakeGap, the earlier phrase, when the later one stays (half of its
//     words or all its content words): cutting both takes is not cleaning a take;
//   - of a phrase said again as the next phrase with the same words, the first one, when the next one stays;
//   - of a group of words said twice in a row inside a phrase («ну привет ну привет»), one copy — the one cut while
//     the other stays.
func takeWords(req *Request, kept []bool) []bool {
	words := req.Transcript.Words
	list := normalizedPhrases(req.Transcript, req.Plan)
	marked := make([]bool, len(words))

	pairs := similarPairs(words, list)
	for k := range pairs {
		earlier, later := &pairs[k][0], &pairs[k][1]
		if words[later.from].Start-words[earlier.from].Start <= maxTakeGap && phraseStays(words, kept, later) {
			mark(marked, phraseRange(earlier), true)
		}
	}

	for i := range list {
		if i+1 < len(list) && len(list[i].stems) > 0 && slices.Equal(list[i].all, list[i+1].all) &&
			phraseStays(words, kept, &list[i+1]) {
			mark(marked, phraseRange(&list[i]), true)
		}

		if first, second, ok := repeatedGroup(words, &list[i]); ok {
			switch {
			case anyKept(kept, second):
				mark(marked, first, true)
			case anyKept(kept, first):
				mark(marked, second, true)
			}
		}
	}

	return marked
}

// phraseRange is the words of the phrase.
func phraseRange(p *normPhrase) roughcut.WordRange {
	return roughcut.WordRange{From: p.from, To: p.to}
}

// phraseStays: at least half of the words of the phrase, or all its content words, stay in the reel.
func phraseStays(words []transcription.Word, kept []bool, p *normPhrase) bool {
	all, stays, content, contentStays := 0, 0, 0, 0

	for i := p.from; i <= p.to; i++ {
		all++

		if kept[i] {
			stays++
		}

		if isContent(normalizeWord(words[i].Word)) {
			content++

			if kept[i] {
				contentStays++
			}
		}
	}

	return 2*stays >= all || (content > 0 && contentStays == content)
}

// repeatedGroup finds the first group of up to maxRunWords words with a content word said twice in a row inside the
// phrase (repeatsInside) and returns its two copies; false without one.
func repeatedGroup(words []transcription.Word, p *normPhrase) (first, second roughcut.WordRange, ok bool) {
	var (
		index []int
		norm  []string
	)

	for i := p.from; i <= p.to; i++ {
		if w := normalizeWord(words[i].Word); w != "" {
			index, norm = append(index, i), append(norm, w)
		}
	}

	for size := 1; size <= maxRunWords; size++ {
		for i := 0; i+2*size <= len(norm); i++ {
			group := norm[i : i+size]
			if slices.Equal(group, norm[i+size:i+2*size]) && slices.ContainsFunc(group, isContent) {
				return roughcut.WordRange{From: index[i], To: index[i+size-1]},
					roughcut.WordRange{From: index[i+size], To: index[i+2*size-1]}, true
			}
		}
	}

	return roughcut.WordRange{}, roughcut.WordRange{}, false
}
