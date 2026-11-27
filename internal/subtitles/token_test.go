package subtitles

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Prokud1n/ai-agents/internal/transcription"
)

func TestTokensFromTranscript(t *testing.T) {
	t.Parallel()

	type w = transcription.Word

	type seg = transcription.Segment

	tests := []struct {
		name     string
		input    *transcription.Result
		expected []token
	}{
		{
			name:     "empty transcript",
			input:    &transcription.Result{},
			expected: []token{},
		},
		{
			name: "punctuation comes from segments, segment end is a strong break",
			input: &transcription.Result{
				Segments: []seg{{Start: 0, End: 2, Text: "Привет, друзья! Присед"}, {Start: 3, End: 4, Text: wordKnees}},
				Words:    []w{{Word: " Привет", Start: 0, End: 0.5}, {Word: "друзья", Start: 0.6, End: 1}, {Word: "Присед", Start: 1.2, End: 2}, {Word: wordKnees, Start: 3, End: 4}},
			},
			expected: []token{
				withBreak(tok("Привет,", 0, 0.5), softBreak),
				withBreak(tok("друзья!", 0.6, 1), strongBreak),
				openEnd(withBreak(tok("Присед", 1.2, 2), strongBreak)),
				withBreak(tok(wordKnees, 3, 4), strongBreak),
			},
		},
		{
			name: "a dash written apart goes with the previous word",
			input: &transcription.Result{
				Segments: []seg{{Start: 0, End: 3, Text: "Тяжело — снизьте вес."}},
				Words:    []w{{Word: "Тяжело", Start: 0, End: 1}, {Word: "снизьте", Start: 1, End: 2}, {Word: wVes, Start: 2, End: 3}},
			},
			expected: []token{
				withBreak(tok("Тяжело —", 0, 1), softBreak),
				tok("снизьте", 1, 2),
				withBreak(tok("вес.", 2, 3), strongBreak),
			},
		},
		{
			name: "a word missing in the segment keeps its text",
			input: &transcription.Result{
				Segments: []seg{{Start: 0, End: 4, Text: "раз два, три."}},
				Words:    []w{{Word: wordOne, Start: 0, End: 1}, {Word: "эээ", Start: 1, End: 2}, {Word: wordTwo, Start: 2, End: 3}, {Word: wTri, Start: 3, End: 4}},
			},
			expected: []token{
				tok(wordOne, 0, 1), tok("эээ", 1, 2), withBreak(tok(wDvaComma, 2, 3), softBreak), withBreak(tok("три.", 3, 4), strongBreak),
			},
		},
		{
			name: "segment without punctuation breaks only at its end",
			input: &transcription.Result{
				Segments: []seg{{Start: 0, End: 2, Text: "держим спину"}},
				Words:    []w{{Word: wordHold, Start: 0, End: 1}, {Word: wordBack, Start: 1, End: 2}},
			},
			expected: []token{tok(wordHold, 0, 1), withBreak(tok(wordBack, 1, 2), strongBreak)},
		},
		{
			name: "a segment whose words are not in words does not shift the next one",
			input: &transcription.Result{
				Segments: []seg{
					{Start: 0, End: 2, Text: "Совсем другие слова здесь, правда."},
					{Start: 2, End: 4, Text: "Держим, спину!"},
				},
				Words: []w{{Word: "Чужое", Start: 0, End: 1}, {Word: wordHold, Start: 2, End: 3}, {Word: wordBack, Start: 3, End: 4}},
			},
			expected: []token{
				withBreak(tok("Чужое", 0, 1), strongBreak),
				withBreak(tok("Держим,", 2, 3), softBreak),
				withBreak(tok("спину!", 3, 4), strongBreak),
			},
		},
		{
			name: "repeated words are aligned in order",
			input: &transcription.Result{
				Segments: []seg{{Start: 0, End: 3, Text: "Раз, раз, раз!"}},
				Words:    []w{{Word: wordOne, Start: 0, End: 1}, {Word: wordOne, Start: 1, End: 2}, {Word: wordOne, Start: 2, End: 3}},
			},
			expected: []token{
				withBreak(tok(wRazCapComma, 0, 1), softBreak), withBreak(tok("раз,", 1, 2), softBreak), withBreak(tok("раз!", 2, 3), strongBreak),
			},
		},
		{
			name: "word past its segment by Whisper's drift goes to the nearest segment",
			input: &transcription.Result{
				Segments: []seg{{Start: 0, End: 1, Text: "Держим спину ровно."}, {Start: 1.6, End: 2.8, Text: "Тянем носок, выдох."}},
				Words: []w{
					{Word: wordHold, Start: 0, End: 0.4}, {Word: wordBack, Start: 0.4, End: 0.8}, {Word: "ровно", Start: 0.8, End: 1.3},
					{Word: "Тянем", Start: 1.4, End: 1.7}, {Word: "носок", Start: 1.7, End: 2.1}, {Word: "выдох", Start: 2.2, End: 2.8},
				},
			},
			expected: []token{
				tok("Держим", 0, 0.4), tok(wordBack, 0.4, 0.8), withBreak(tok("ровно.", 0.8, 1.3), strongBreak),
				tok("Тянем", 1.4, 1.7), withBreak(tok("носок,", 1.7, 2.1), softBreak), withBreak(tok("выдох.", 2.2, 2.8), strongBreak),
			},
		},
		{
			name: "Whisper's order is kept, times out of order are evened out, backslashes dropped",
			input: &transcription.Result{
				Words: []w{
					{Word: wordHold, Start: 2, End: 2.5}, {Word: "a\\Nb", Start: 1, End: 0.5}, {Word: " ", Start: 3, End: 4},
					{Word: wordBack, Start: 2.6, End: 3},
				},
			},
			expected: []token{tok(wordHold, 2, 2.5), tok("aNb", 2, 2), tok(wordBack, 2.6, 3)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, tokensFromTranscript(tt.input))
		})
	}
}

func TestBreakAfter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected breakKind
	}{
		{name: "plain word", input: wordBack, expected: noBreak},
		{name: "comma", input: "спину,", expected: softBreak},
		{name: "colon", input: "итак:", expected: softBreak},
		{name: "question in quotes", input: "«да?»", expected: strongBreak},
		{name: "ellipsis", input: "так...", expected: strongBreak},
		{name: "unicode ellipsis", input: "так…", expected: strongBreak},
		{name: "dash", input: "тяжело —", expected: softBreak},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, breakAfter(tt.input))
		})
	}
}

func TestNormalize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "case and punctuation", input: "«Привет!»", expected: "привет"},
		{name: "yo is e", input: "Ещё", expected: "еще"},
		{name: "digits", input: "10%", expected: "10"},
		{name: "hyphen is dropped", input: "давай-ка", expected: "давайка"},
		{name: "only punctuation", input: "—", expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, normalize(tt.input))
		})
	}
}
