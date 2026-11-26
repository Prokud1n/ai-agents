package subtitles

import (
	"strings"
	"unicode"
)

// censorMark replaces one letter of an obscene word on the screen: «х*йня», «б*ять», «п*здец». The sound is not touched.
const censorMark = '*'

// obsceneRoot is a root of a Russian obscene word: it is masked where a word (its letters, ё read as е) has it at the
// start, after one of its prefixes (prefixed), or anywhere inside the word (anywhere); with forms, only as one of
// these words.
type obsceneRoot struct {
	root     string
	anywhere bool
	prefixed bool
	forms    []string
}

// obscenePrefixes may stand before a root that is masked only at the start of a word: «нахуй», «заебал», «охуел».
// «хлеба», «лихую», «плохую», «ребята», «себе» keep their letters: «хл», «ли», «пло», «р», «с» are not prefixes here
// («съебал» is: the hard sign).
var obscenePrefixes = []string{
	"", "а", "о", "у", "вы", "на", "по", "ни", "за", "до", "от", "об", "из", "раз", "рас", "под", "при", "про",
	"пере", "недо", "долбо", "отъ", "съ", "разъ", "объ", "подъ", "изъ",
}

// obsceneRoots are the roots masked on the screen. A root inside common words («употреблять», «оскорблять», «хлеба»,
// «тихую», «сукно», «мудрый») is not masked: such roots count only at the start of a word or after a prefix, and
// «сука» only as a word of its own.
var obsceneRoots = []obsceneRoot{
	{root: "пизд", anywhere: true},
	{root: "залуп", anywhere: true},
	{root: "хуй", prefixed: true}, {root: "хуе", prefixed: true}, {root: "хуя", prefixed: true},
	{root: "хую", prefixed: true}, {root: "хуи", prefixed: true}, {root: "хуев", prefixed: true},
	{root: "бляд"}, {root: "блят"}, {root: "бля", forms: []string{"бля"}},
	{root: "еба", prefixed: true}, {root: "ебу", prefixed: true}, {root: "ебл", prefixed: true},
	{root: "ебн", prefixed: true}, {root: "еби", prefixed: true}, {root: "ебе", prefixed: true},
	{root: "ебо", prefixed: true}, {root: "долбоеб", anywhere: true},
	{root: "мудак"}, {root: "мудач"}, {root: "мудил"}, {root: "мудозвон"},
	{root: "пидор"}, {root: "пидар"}, {root: "пидр"}, {root: "гандон"},
	{root: "сук", forms: []string{
		"сука", "суки", "суке", "суку", "сукой", "сукам", "суками", "сукин", "сукина", "сукины", "сучка", "сучки",
		"сучке", "сучку", "сучара",
	}},
}

// censor masks the obscene words of a text shown on the screen: in every word (a run of letters) with an obscene root
// the second letter of the root becomes «*» («бля» → «б*я», «пизд» → «п*зд»). Everything else stays as it is.
func censor(text string) string {
	runes := []rune(text)
	changed := false

	for from := 0; from < len(runes); {
		if !unicode.IsLetter(runes[from]) {
			from++

			continue
		}

		to := from
		for to < len(runes) && unicode.IsLetter(runes[to]) {
			to++
		}

		if at, ok := obsceneAt(runes[from:to]); ok {
			runes[from+at] = censorMark
			changed = true
		}

		from = to
	}

	if !changed {
		return text
	}

	return string(runes)
}

// obsceneAt is the index of the letter to mask in the word; false for a word without an obscene root.
func obsceneAt(word []rune) (int, bool) {
	norm := make([]rune, len(word))
	for i, r := range word {
		norm[i] = unicode.ToLower(r)
		if norm[i] == 'ё' {
			norm[i] = 'е'
		}
	}

	lower := string(norm)

	for _, r := range obsceneRoots {
		if at, ok := r.find(lower); ok {
			return at + 1, true
		}
	}

	return 0, false
}

// find is the index (in runes) of the root in the word, as the root allows it.
func (r *obsceneRoot) find(word string) (int, bool) {
	switch {
	case len(r.forms) > 0:
		for _, f := range r.forms {
			if word == f {
				return 0, true
			}
		}

		return 0, false
	case r.anywhere:
		if i := strings.Index(word, r.root); i >= 0 {
			return len([]rune(word[:i])), true
		}

		return 0, false
	}

	prefixes := obscenePrefixes[:1]
	if r.prefixed {
		prefixes = obscenePrefixes
	}

	for _, p := range prefixes {
		if strings.HasPrefix(word, p+r.root) {
			return len([]rune(p)), true
		}
	}

	return 0, false
}
