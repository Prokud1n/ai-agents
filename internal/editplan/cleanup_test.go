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

func TestRequestCleanupOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		change   func(req *Request)
		expected bool
	}{
		{name: "a first version without a caption", change: func(*Request) {}, expected: true},
		{name: "the cleanup off", change: func(req *Request) { req.Cleanup = false }},
		{name: "a caption", change: func(req *Request) { req.Caption = "без начала" }},
		{name: "a revision", change: func(req *Request) { req.Version = 2 }},
		{name: "a long talk is selected", change: func(req *Request) { req.TargetSeconds = 2 }},
		{
			name:   "a workout",
			change: func(req *Request) { req.Plan.Edit = &roughcut.Edit{Mode: roughcut.WorkoutMode} },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := cleanupSpeechRequest(t)
			tt.change(req)

			assert.Equal(t, tt.expected, req.CleanupOnly())
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
		{name: "the workout mode", req: cleanupRequest, input: Input{Mode: roughcut.WorkoutMode}, err: refused},
		{
			name: "an exercise title", req: cleanupRequest, err: refused,
			input: Input{AddTitles: []TitleInput{{Text: "Планка", Word: new(7)}}},
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
			name: "with the cleanup off the sound can go", input: Input{Mute: new(true)},
			req: func() *Request {
				req := cleanupRequest()
				req.Cleanup = false

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
			expected: CleanupLinePrefix + ": 1 фрагмент (",
		},
		{
			name:     "two fragments apart",
			input:    Input{Remove: []roughcut.WordRange{countdownWords, helloWord}},
			expected: CleanupLinePrefix + ": 2 фрагмента (",
		},
		{
			name:     "a trim counts",
			input:    Input{TrimStart: 2.2},
			expected: CleanupLinePrefix + ": 1 фрагмент (",
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
			assert.Equal(t, []string{"Чистка первой версии: отсчёт и слова про дубль"}, outcome.Edit.Notes)
			assert.Len(t, outcome.Edit.Details, len(in.Remove))
		})
	}

	nothing, err := Apply(cleanupSpeechRequest(t), &Input{Decision: EditDecision, Reason: "чисто"})
	require.NoError(t, err)
	assert.Empty(t, nothing.Changes, "nothing cleaned: no line")
}

// TestApplyCleanupKeepsTheSpeech: a cleaning that leaves less than 40 % of the automatic cut is refused.
func TestApplyCleanupKeepsTheSpeech(t *testing.T) {
	t.Parallel()

	_, err := Apply(cleanupSpeechRequest(t), &Input{
		Decision: EditDecision, Remove: []roughcut.WordRange{{From: 0, To: 15}},
	})

	var check *CheckError
	require.ErrorAs(t, err, &check)
	assert.Contains(t, check.Message, "меньше 40%")
}

func TestCleanupLine(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Убрал неудачные дубли и подготовку к съёмке: 3 фрагмента (52,4 с).",
		cleanupLine(cleaning{fragments: 3, seconds: 52.38}))
	assert.Equal(t, "Убрал неудачные дубли и подготовку к съёмке: 5 фрагментов (1 с).",
		cleanupLine(cleaning{fragments: 5, seconds: 1}))
	assert.Empty(t, cleanupLine(cleaning{}))
}

func TestPromptCleanup(t *testing.T) {
	t.Parallel()

	prompt := Prompt(cleanupSpeechRequest(t))

	assert.Contains(t, prompt, "Подписи нет — почисти ролик (раздел «Чистка дублей и подготовки»)")
	assert.NotContains(t, prompt, "Предложи заголовок-хук.", "the hook is off")
	assert.Contains(t, prompt, "Похоже на слова про съёмку")
	assert.Contains(t, prompt, "- слова 0–3 (0:00.5) «раз два три начинаем»")
	assert.Contains(t, prompt, "«это последний дубль»")
}
