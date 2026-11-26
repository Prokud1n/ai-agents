package editplan

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

// repeatSpeech has a greeting said three times in a row and the intro said again after a pause:
//
//	0–5 ну привет ну привет ну привет || 6–10 меня зовут настя я тренер || 11–17 меня зовут настя я тренер по фитнесу |
//	18–22 сегодня про планку и спину
const repeatSpeech = "ну привет ну привет ну привет || меня зовут настя я тренер || " +
	"меня зовут настя я тренер по фитнесу. | сегодня про планку и спину."

// straightBack is a phrase compared with others.
const straightBack = "держите спину ровно"

// wholeRequest is the first version of the speech with a plan that keeps the whole source, so every phrase is in it.
func wholeRequest(text string) *Request {
	src := speech(text)
	plan := &roughcut.Plan{
		SourceDuration: src.Duration, Duration: src.Duration, Keep: []roughcut.Interval{{Start: 0, End: src.Duration}},
	}

	return &Request{TaskID: 7, Version: 1, Transcript: src, Plan: plan, Cleanup: true}
}

func TestWriteRepeats(t *testing.T) {
	t.Parallel()

	var b strings.Builder

	writeRepeats(&b, wholeRequest(repeatSpeech))
	hints := b.String()

	assert.Contains(t, hints, "Похожие фразы — подсказка для чистки")
	assert.Contains(t, hints, "«меня зовут настя я тренер» ↔ слова 11–17 (")
	assert.Contains(t, hints, "Повтор подряд внутри фразы")
	assert.Contains(t, hints, "- слова 0–5 (0:00.5) «ну привет ну привет ну привет»")
	assert.NotContains(t, hints, "Похоже на слова про съёмку", "nothing about the shooting")

	var revision strings.Builder

	req := wholeRequest(repeatSpeech)
	req.Version = 2
	writeRepeats(&revision, req)
	assert.Empty(t, revision.String(), "a revision gets no hints")
}

// testPhrase is the phrase of the words as normalizedPhrases makes it.
func testPhrase(text string) *normPhrase {
	p := &normPhrase{}
	for _, w := range strings.Fields(text) {
		p.add(normalizeWord(w))
	}

	return p
}

func TestSimilarity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		a, b     string
		expected float64
	}{
		{name: "the same words", a: "Ну, привет!", b: "ну привет", expected: 1},
		{name: "one word is not a repeat", a: "так", b: "так"},
		{name: "the forms of the words", a: "держите спину ровно и ноги", b: "держите спина ровно и ногу", expected: 1},
		{name: "a share of the shorter one", a: "сегодня расскажу про планку и пресс", b: "сегодня про планку", expected: 1},
		{name: "different thoughts", a: straightBack, b: "дышите спокойно и не спешите"},
		{name: "one content word in common", a: straightBack, b: "спину не сутульте никогда"},
		{name: "«ё» is «е»", a: "всё ещё болит", b: "все еще болит", expected: 1},
		{name: "the points of a list", a: "страх номер один", b: "страх номер два"},
		{name: "ordinal points", a: "первая ошибка в планке", b: "вторая ошибка в планке"},
		{name: "the same number said again", a: "страх номер один", b: "страх номер один это", expected: 1},
		{name: "stop words only in common", a: "что это значит для вас", b: "как это работает для вас"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.InDelta(t, tt.expected, similarity(testPhrase(tt.a), testPhrase(tt.b)), 1e-9)
		})
	}
}

func TestStem(t *testing.T) {
	t.Parallel()

	tests := []struct {
		a, b string
	}{
		{a: "спину", b: "спина"},
		{a: "ноги", b: "ногу"},
		{a: "планку", b: "планка"},
		{a: "колени", b: "колено"},
	}

	for _, tt := range tests {
		t.Run(tt.a+"/"+tt.b, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, stem(tt.a), stem(tt.b))
		})
	}
}

// TestSimilarPairsSkipsSeries: three similar phrases in a row are an enumeration or a refrain, not a failed take.
func TestSimilarPairsSkipsSeries(t *testing.T) {
	t.Parallel()

	req := wholeRequest("держим спину ровно. | держим спину ровно. | держим спину ровно. || дышите спокойно.")
	pairs := similarPairs(req.Transcript.Words, normalizedPhrases(req.Transcript, req.Plan))
	assert.Empty(t, pairs)

	req = wholeRequest("держим спину ровно. | дышите спокойно. | держим спину ровно.")
	pairs = similarPairs(req.Transcript.Words, normalizedPhrases(req.Transcript, req.Plan))
	require.Len(t, pairs, 1, "two of them apart are a repeat")
	assert.Equal(t, 0, pairs[0][0].from)
}

func TestRepeatsInside(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "a greeting three times", input: "ну привет ну привет ну привет", expected: true},
		{name: "a word twice", input: "держим держим спину", expected: true},
		{name: "short words only", input: "да да конечно"},
		{name: "no repeat", input: straightBack},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, repeatsInside(strings.Fields(tt.input)))
		})
	}
}

func TestAboutShooting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "a countdown", input: "раз два три", expected: true},
		{name: "a slate", input: "это последний дубль", expected: true},
		{name: "a restart", input: "стоп давай заново", expected: true},
		{name: "the camera", input: "камера пишет", expected: true},
		{name: "recording", input: "так я записываю", expected: true},
		{name: "a question to the operator", input: "ты снимаешь", expected: true},
		{name: "a countdown in digits", input: "1 2 3", expected: true},
		{name: "a word that only starts so", input: "снимает нагрузку", expected: false},
		{name: "the content", input: straightBack, expected: false},
		{name: "one in a count is not a countdown", input: "раз и два", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, aboutShooting(strings.Fields(tt.input)))
		})
	}
}
