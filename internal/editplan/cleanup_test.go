package editplan

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

// cleanupSpeech is a talking head with the preparation for the shooting before the content:
//
//	0 раз 1 два 2 три 3 начинаем || 4 это 5 последний 6 дубль || 7 привет 8 сегодня 9 про 10 планку |
//	11 держите 12 спину 13 ровно | 14 дышите 15 спокойно 16 и 17 не 18 спешите
const cleanupSpeech = "раз два три начинаем || это последний дубль || привет сегодня про планку. | " +
	"держите спину ровно. | дышите спокойно и не спешите."

// oneFragment starts the cleaning line of one fragment.
const oneFragment = cleanupPrepPrefix + ": 1 фрагмент ("

var (
	countdownWords = roughcut.WordRange{From: 0, To: 3}
	lastTakeWords  = roughcut.WordRange{From: 4, To: 6}
	helloWord      = roughcut.WordRange{From: 7, To: 7}
)

// cleanupSpeechRequest is the first version of cleanupSpeech without a caption, only cleaned by the editor.
func cleanupSpeechRequest(t *testing.T) *Request {
	t.Helper()

	src := speech(cleanupSpeech)

	plan, err := roughcut.BuildPlanEdited(src, src.Duration, testSettings(), &roughcut.Edit{})
	require.NoError(t, err)

	return &Request{TaskID: 7, Version: 1, Transcript: src, Plan: &plan, Cleanup: true}
}

func TestRequestCleaning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		change   func(req *Request)
		cleaning bool
		only     bool
	}{
		{name: "a first version without a caption", change: func(*Request) {}, cleaning: true, only: true},
		{name: "the cleanup off", change: func(req *Request) { req.Cleanup = false }},
		{name: "a caption: cleaned too", change: func(req *Request) { req.Caption = "без начала" }, cleaning: true},
		{name: "a revision is not cleaned", change: func(req *Request) { req.Version = 2 }},
		{name: "a long talk is selected", change: func(req *Request) { req.TargetSeconds = 2 }},
		{
			name:   "a workout is not cleaned",
			change: func(req *Request) { req.Plan.Edit = &roughcut.Edit{Mode: roughcut.WorkoutMode} },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := cleanupSpeechRequest(t)
			tt.change(req)

			assert.Equal(t, tt.cleaning, req.Cleaning())
			assert.Equal(t, tt.only, req.CleanupOnly())
			assert.False(t, req.HookOnly(), "the hook-only first version is for the cleanup off")
		})
	}
}

// TestApplyCleanupOnly: a first version without a caption is cleaned — words cut, the start trimmed, the disputed
// repeats decided, the hook set — but not shortened to a length, cut by time, switched to a workout, titled or muted.
func TestApplyCleanupOnly(t *testing.T) {
	t.Parallel()

	const refused = "только чистка"

	tests := []struct {
		name  string
		req   func() *Request
		input Input
		err   string
	}{
		{name: "the hook", req: cleanupRequest, input: Input{HookTitle: new(testHook)}},
		{name: "a correction", req: cleanupRequest, input: Input{FixWords: []FixInput{{Index: 7, Text: "делайте"}}}},
		{name: "words cut", req: cleanupRequest, input: Input{Remove: []roughcut.WordRange{{From: 7, To: 7}}}},
		{name: "the start trimmed", req: cleanupRequest, input: Input{TrimStart: 1}},
		{name: "seconds cut", req: cleanupRequest, input: Input{CutSeconds: []SecondsRange{{From: 1, To: 2}}}, err: refused},
		{name: "a length", req: cleanupRequest, input: Input{TargetSeconds: 5}, err: refused},
		{name: "cleanup words", req: cleanupRequest, input: Input{CleanupWords: []roughcut.WordRange{{From: 7, To: 7}}}},
		{name: "the workout mode without cuts", req: cleanupRequest, input: Input{Mode: roughcut.WorkoutMode}},
		{
			name: "the workout mode with a cut", req: cleanupRequest, err: "в тренировке без подписи ничего не вырезай",
			input: Input{Mode: roughcut.WorkoutMode, Remove: []roughcut.WordRange{{From: 7, To: 7}}},
		},
		{
			name: "the workout mode with cleanup words", req: cleanupRequest, err: "cleanup_words — только",
			input: Input{Mode: roughcut.WorkoutMode, CleanupWords: []roughcut.WordRange{{From: 7, To: 7}}},
		},
		{
			name: "an exercise title", req: cleanupRequest, err: refused,
			input: Input{AddTitles: []TitleInput{{Text: "Мостик", Word: new(7)}}},
		},
		{name: "muted", req: cleanupRequest, input: Input{Mute: new(true)}, err: refused},
		{
			name: "with a caption the reel is cut by time", input: Input{CutSeconds: []SecondsRange{{From: 1, To: 2}}},
			req: func() *Request {
				req := cleanupRequest()
				req.Caption = "вырежи с 1 по 2 секунду"

				return req
			},
		},
		{
			name: "cleanup words with the cleanup off", input: Input{CleanupWords: []roughcut.WordRange{{From: 7, To: 7}}},
			err: "cleanup_words — только",
			req: func() *Request {
				req := cleanupRequest()
				req.Cleanup = false

				return req
			},
		},
		{
			name: "with the cleanup and the hook off the sound can go", input: Input{Mute: new(true)},
			req: func() *Request {
				req := cleanupRequest()
				req.Cleanup, req.Hook = false, false

				return req
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			in := tt.input
			in.Decision, in.Reason = EditDecision, testReason

			outcome, err := Apply(tt.req(), &in)
			if tt.err != "" {
				var check *CheckError
				require.ErrorAs(t, err, &check)
				assert.Contains(t, check.Message, tt.err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, EditOutcome, outcome.Kind)
		})
	}
}

// TestApplyCleanupLine: the cleaning is one line about the failed takes with the fragments and the seconds removed,
// not a line for every fragment or the trim; the decisions log names every range cut.
func TestApplyCleanupLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    Input
		expected string
	}{
		{
			name:     "the countdown and the slate are one fragment",
			input:    Input{Remove: []roughcut.WordRange{countdownWords, lastTakeWords}},
			expected: oneFragment,
		},
		{
			name:     "two fragments apart",
			input:    Input{Remove: []roughcut.WordRange{countdownWords, helloWord}},
			expected: cleanupPrepPrefix + ": 2 фрагмента (",
		},
		{
			name:     "cleanup words count the same",
			input:    Input{CleanupWords: []roughcut.WordRange{countdownWords, lastTakeWords}},
			expected: oneFragment,
		},
		{
			name:     "a trim counts",
			input:    Input{TrimStart: 2.2},
			expected: oneFragment,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := cleanupSpeechRequest(t)
			in := tt.input
			in.Decision, in.Reason = EditDecision, "отсчёт и слова про дубль"

			outcome, err := Apply(req, &in)
			require.NoError(t, err)
			require.Len(t, outcome.Changes, 1)
			assert.True(t, strings.HasPrefix(outcome.Changes[0], tt.expected), outcome.Changes[0])
			assert.True(t, strings.HasSuffix(outcome.Changes[0], " с)."), outcome.Changes[0])
			assert.Less(t, outcome.Duration, req.Plan.Duration)
			assert.Positive(t, outcome.Cleaned, "the fragments cleaned")
			assert.Equal(t, []string{"Чистка первой версии: отсчёт и слова про дубль"}, outcome.Edit.Notes)
			assert.Len(t, outcome.Edit.Details, len(in.Remove)+len(in.CleanupWords))
		})
	}

	nothing, err := Apply(cleanupSpeechRequest(t), &Input{Decision: EditDecision, Reason: "чисто"})
	require.NoError(t, err)
	assert.Empty(t, nothing.Changes, "nothing cleaned: no line")
}

// TestApplyCleanupKeepsTheSpeech: a cleaning that leaves less than cleanupFloor of the reel is refused.
func TestApplyCleanupKeepsTheSpeech(t *testing.T) {
	t.Parallel()

	_, err := Apply(cleanupSpeechRequest(t), &Input{
		Decision: EditDecision, Remove: []roughcut.WordRange{{From: 0, To: 15}},
	})

	var check *CheckError
	require.ErrorAs(t, err, &check)
	assert.Contains(t, check.Message, "а должно остаться хотя бы")
}

func TestCleanupFloor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    float64
		expected float64
	}{
		{name: "a short reel keeps half", input: 9.3, expected: 4.65},
		{name: "15 s at least", input: 40, expected: 15},
		{name: "a quarter of a long reel", input: 120, expected: 30},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.InDelta(t, tt.expected, cleanupFloor(tt.input), 1e-9)
		})
	}
}

// TestApplyCleaningWithCaption: with a caption the cleaning is cleanup_words — one line — and the caption's cuts and
// trim are described as usual; nothing limits the length.
func TestApplyCleaningWithCaption(t *testing.T) {
	t.Parallel()

	req := cleanupSpeechRequest(t)
	req.Caption = "убери «привет»"

	outcome, err := Apply(req, &Input{
		Decision: EditDecision, Reason: "подпись и отсчёт", Remove: []roughcut.WordRange{helloWord},
		CleanupWords: []roughcut.WordRange{countdownWords, lastTakeWords},
	})
	require.NoError(t, err)
	require.Len(t, outcome.Changes, 2)
	assert.True(t, strings.HasPrefix(outcome.Changes[0], oneFragment), outcome.Changes[0])
	assert.Equal(t, "Вырезал фрагмент «привет».", outcome.Changes[1])
	assert.Equal(t, []string{"Подпись тренера к исходнику: подпись и отсчёт"}, outcome.Edit.Notes)
}

// TestApplyCleanupWorkout: a workout with comments (the speech is about 40 % of the source) may be switched to the
// workout mode by the cleaning, without cuts and without the cleaning line.
func TestApplyCleanupWorkout(t *testing.T) {
	t.Parallel()

	req := cleanupRequest()
	req.Plan.SpeechShare = 0.4

	outcome, err := Apply(req, &Input{Decision: EditDecision, Reason: "показ упражнений", Mode: roughcut.WorkoutMode})
	require.NoError(t, err)
	assert.Equal(t, roughcut.WorkoutMode, outcome.Mode)
	assert.Empty(t, outcome.Changes, "no cleaning line in a workout")
	assert.Greater(t, outcome.Duration, req.Plan.Duration, "the pauses stay")
}

func TestCleanupLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    cleaning
		expected string
	}{
		{
			name:     "with a failed take",
			input:    cleaning{fragments: 3, seconds: 52.38, takes: true},
			expected: "Почистил ролик — неудачные дубли, подготовка к съёмке и лишние вставки: 3 фрагмента (52,4 с).",
		},
		{
			name:     "the preparation and the links only",
			input:    cleaning{fragments: 5, seconds: 1},
			expected: "Почистил ролик — подготовка к съёмке и лишние вставки: 5 фрагментов (1 с).",
		},
		{name: "nothing", input: cleaning{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, cleanupLine(tt.input))
		})
	}
}

func TestPromptCleanup(t *testing.T) {
	t.Parallel()

	prompt := Prompt(cleanupSpeechRequest(t))

	assert.Contains(t, prompt, "Подписи нет — почисти ролик (раздел «Чистка дублей и подготовки»)")
	assert.NotContains(t, prompt, "Предложи заголовок-хук.", "the hook is off")
	assert.Contains(t, prompt, "Похоже на слова про съёмку")
	assert.Contains(t, prompt, "- слова 0–3 (0:00.5) «раз два три начинаем»")
	assert.Contains(t, prompt, "«это последний дубль»")

	captioned := cleanupSpeechRequest(t)
	captioned.Caption = "про планку"
	assert.Contains(t, Prompt(captioned), "Ещё почисти ролик (раздел «Чистка дублей и подготовки»)")

	selected := selectionRequest(t, 1)
	selected.Cleanup = true
	assert.Contains(t, Prompt(selected), "Сначала выкинь неудачные дубли, повторы подряд и подготовку к съёмке через "+
		"cleanup_words")
	assert.NotContains(t, Prompt(selected), "Ещё почисти ролик", "the selection is not the cleaning mode")
}

// TestCleanedTakes (T3.22): the header of the description names the failed takes only when the cleaning cut a phrase
// the hints found said again; the preparation for the shooting alone is not a failed take.
func TestCleanedTakes(t *testing.T) {
	t.Parallel()

	src := speech("сегодня расскажу про планку. | итак сегодня про планку и спину. | держите спину ровно. | " +
		"дышите спокойно и не спешите.")

	plan, err := roughcut.BuildPlanEdited(src, src.Duration, testSettings(), &roughcut.Edit{})
	require.NoError(t, err)

	takes := &Request{TaskID: 7, Version: 1, Transcript: src, Plan: &plan, Cleanup: true}
	require.Contains(t, Prompt(takes), "Похожие фразы", "the intro said again is hinted")

	tests := []struct {
		name     string
		req      *Request
		cleanup  []roughcut.WordRange
		expected bool
	}{
		{name: "the first intro", req: takes, cleanup: []roughcut.WordRange{{From: 0, To: 3}}, expected: true},
		{name: "the countdown", req: cleanupSpeechRequest(t), cleanup: []roughcut.WordRange{countdownWords}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			outcome, err := Apply(tt.req, &Input{Decision: EditDecision, Reason: testReason, CleanupWords: tt.cleanup})
			require.NoError(t, err)
			assert.Equal(t, 1, outcome.Cleaned)
			assert.Equal(t, tt.expected, outcome.CleanedTakes)
		})
	}
}
