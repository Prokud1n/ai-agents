package editor

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/editplan"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

func TestDescribe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		result   editjob.Result
		version  int
		caption  string
		summary  *editjob.Summary
		expected string
	}{
		{
			name:    "cut with the trainer's caption",
			result:  editjob.Result{SourceDuration: 182.4, Duration: 141.2},
			version: 1,
			caption: " про планку ",
			expected: "Черновик рилс готов: было 3:02, стало 2:21 — вырезал 0:41 (паузы, оговорки и неудачные дубли).\n" +
				"Ваша подпись к исходнику: «про планку»",
		},
		{
			name:    "repeats left in without a decision",
			result:  editjob.Result{SourceDuration: 60, Duration: 45, Candidates: 2},
			version: 1,
			expected: "Черновик рилс готов: было 1:00, стало 0:45 — вырезал 0:15 (паузы, оговорки и неудачные дубли).\n" +
				"Спорные повторы оставил в ролике: 2.",
		},
		{
			name:    "the caption applied and the repeats decided",
			result:  editjob.Result{SourceDuration: 60, Duration: 40, Candidates: 1},
			version: 1,
			caption: "без первой фразы",
			summary: &editjob.Summary{Changes: []string{"Вырезал фрагмент «всем привет».", "Спорные повторы: вырезал 1, оставил 1."}},
			expected: "Черновик рилс готов: было 1:00, стало 0:40 — вырезал 0:20 (паузы, оговорки и неудачные дубли).\n" +
				"Вырезал фрагмент «всем привет».\nСпорные повторы: вырезал 1, оставил 1.\n" +
				"Ваша подпись к исходнику: «без первой фразы»",
		},
		{
			name:    "the caption is not applied",
			result:  editjob.Result{SourceDuration: 60, Duration: 45},
			version: 1,
			caption: "добавь музыку",
			summary: &editjob.Summary{Note: captionCannotNote},
			expected: "Черновик рилс готов: было 1:00, стало 0:45 — вырезал 0:15 (паузы, оговорки и неудачные дубли).\n" +
				captionCannotNote + "\nВаша подпись к исходнику: «добавь музыку»",
		},
		{
			name:    "a workout kept whole, with its titles",
			result:  editjob.Result{SourceDuration: 120, Duration: 120},
			version: 1,
			caption: "Выпады",
			summary: &editjob.Summary{Mode: roughcut.WorkoutMode, Changes: []string{titlesHeader, testTitleLine}},
			expected: "Черновик тренировки готов: 2:00 — ролик целиком, паузы и молчаливые отрезки не вырезал.\n" +
				"Подписи упражнений:\n0:03 — «Выпады»\nВаша подпись к исходнику: «Выпады»",
		},
		{
			name:    "a workout with the start cut, without the model",
			result:  editjob.Result{SourceDuration: 130, Duration: 120},
			version: 1,
			summary: &editjob.Summary{Mode: roughcut.WorkoutMode, Note: workoutFallbackNote},
			expected: "Черновик тренировки готов: было 2:10, стало 2:00 — паузы и молчаливые отрезки оставил, убрал " +
				"только указанное (0:10).\n" + workoutFallbackNote,
		},
		{
			name:    "a talking head shortened for a reel",
			result:  editjob.Result{SourceDuration: 182, Duration: 48},
			version: 1,
			summary: &editjob.Summary{Selected: true, Changes: []string{
				"Оставил главное: начало «планка укрепляет спину…», финал «…сохраняйте и пробуйте завтра».",
				"Не вошло: «привет сегодня расскажу про планку».",
			}},
			expected: "Черновик рилс готов: было 3:02, стало 0:48 — сократил под рилс.\n" +
				"Оставил главное: начало «планка укрепляет спину…», финал «…сохраняйте и пробуйте завтра».\n" +
				"Не вошло: «привет сегодня расскажу про планку».",
		},
		{
			name:    "a long talking head only cleaned",
			result:  editjob.Result{SourceDuration: 182, Duration: 141},
			version: 1,
			summary: &editjob.Summary{Note: longFailed60},
			expected: "Черновик рилс готов: было 3:02, стало 2:21 — вырезал 0:41 (паузы, оговорки и неудачные дубли).\n" +
				longFailed60,
		},
		{
			name:     "nothing to cut",
			result:   editjob.Result{SourceDuration: 30.2, Duration: 30},
			version:  1,
			expected: "Черновик рилс готов: 0:30, вырезать было почти нечего.",
		},
		{
			name:     "a revision says what changed",
			result:   editjob.Result{SourceDuration: 60, Duration: 42},
			version:  2,
			caption:  "про планку",
			summary:  &editjob.Summary{Changes: []string{testChange}, PreviousDuration: 45},
			expected: "Новая версия рилс: было 0:45, стало 0:42.\n" + testChange,
		},
		{
			name:     "a revision without its summary",
			result:   editjob.Result{SourceDuration: 60, Duration: 42},
			version:  3,
			expected: "Новая версия рилс: 0:42.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, Describe(&tt.result, tt.version, tt.caption, tt.summary))
		})
	}
}

func TestDescribeTruncatesLongCaption(t *testing.T) {
	t.Parallel()

	got := Describe(&editjob.Result{SourceDuration: 60, Duration: 50}, 1, strings.Repeat("а", maxCaptionRunes*2), nil)

	assert.Less(t, len([]rune(got)), 1024, "fits a Telegram video caption")
	assert.True(t, strings.HasSuffix(got, "…»"))
}

func TestFirstSummary(t *testing.T) {
	t.Parallel()

	twoCandidates := &roughcut.Plan{Candidates: []roughcut.Candidate{{ID: 0}, {ID: 1}}}

	longTalk := &roughcut.Plan{Duration: 141, Candidates: []roughcut.Candidate{{ID: 0}}}

	tests := []struct {
		name     string
		caption  string
		plan     *roughcut.Plan
		target   float64
		outcome  editplan.Outcome
		expected editjob.Summary
	}{
		{
			name: "a long talking head shortened", plan: longTalk, target: 60,
			outcome: editplan.Outcome{Kind: editplan.EditOutcome, Selected: true, Changes: []string{droppedHello}},
			expected: editjob.Summary{Selected: true, Changes: []string{
				droppedHello, cutNoneOfOne,
			}},
		},
		{
			name: "a long talking head the model failed on", plan: longTalk, target: 60,
			outcome:  editplan.Outcome{Kind: editplan.FailedOutcome},
			expected: editjob.Summary{Changes: []string{leftOneRepeat}, Note: longFailed60},
		},
		{
			name: "a long talking head with a caption not understood", plan: longTalk, target: 60, caption: "красиво",
			outcome:  editplan.Outcome{Kind: editplan.InvalidOutcome},
			expected: editjob.Summary{Changes: []string{leftOneRepeat}, Note: captionUnclearNote + " " + longUnchosen60},
		},
		{
			name: "within the target: no note", plan: longTalk, target: 180,
			outcome:  editplan.Outcome{Kind: editplan.NoAnswerOutcome},
			expected: editjob.Summary{Changes: []string{leftOneRepeat}},
		},
		{
			name: "repeats decided", plan: twoCandidates,
			outcome:  editplan.Outcome{Kind: editplan.EditOutcome, Cut: 1, Kept: 1},
			expected: editjob.Summary{Changes: []string{"Спорные повторы: вырезал 1, оставил 1."}},
		},
		{
			name: "the caption applied, no repeats", caption: "без начала", plan: &roughcut.Plan{},
			outcome:  editplan.Outcome{Kind: editplan.EditOutcome, Changes: []string{"Обрезал начало: убрал первые 2 с."}},
			expected: editjob.Summary{Changes: []string{"Обрезал начало: убрал первые 2 с."}},
		},
		{
			name: "the caption cannot be applied", caption: "добавь музыку", plan: twoCandidates,
			outcome: editplan.Outcome{Kind: editplan.CannotOutcome},
			expected: editjob.Summary{
				Changes: []string{"Спорные повторы оставил в ролике: 2."}, Note: captionCannotNote,
			},
		},
		{
			name: "the caption not understood", caption: "сделай красиво", plan: &roughcut.Plan{},
			outcome:  editplan.Outcome{Kind: editplan.UnclearOutcome},
			expected: editjob.Summary{Note: captionUnclearNote},
		},
		{
			name: "a workout the model did not understand: the whole video and a hint", caption: "сделай красиво",
			plan:    &roughcut.Plan{Edit: &roughcut.Edit{Mode: roughcut.WorkoutMode}},
			outcome: editplan.Outcome{Kind: editplan.UnclearOutcome},
			expected: editjob.Summary{
				Mode: roughcut.WorkoutMode, Changes: []string{titlesHint}, Note: captionUnclearNote,
			},
		},
		{
			name: "a workout chosen by the model, an exercise without a time", plan: &roughcut.Plan{},
			outcome: editplan.Outcome{
				Kind: editplan.EditOutcome, Mode: roughcut.WorkoutMode, Untimed: []string{"Планка"},
				Titles: []string{testTitleLine},
			},
			expected: editjob.Summary{Mode: roughcut.WorkoutMode, Changes: []string{
				titlesHeader, testTitleLine, "Не поставил подписи «Планка»: не понял, в какой момент их показать. " +
					"Добавьте их правкой с временем, например: „подпиши выпады с 0:15 до 0:45“.",
			}},
		},
		{
			name: "the model switched a workout back to talk", plan: &roughcut.Plan{Edit: &roughcut.Edit{Mode: roughcut.WorkoutMode}},
			outcome:  editplan.Outcome{Kind: editplan.EditOutcome, Mode: roughcut.TalkMode},
			expected: editjob.Summary{},
		},
		{
			name: "the model did not decide the repeats", plan: twoCandidates,
			outcome:  editplan.Outcome{Kind: editplan.NoAnswerOutcome},
			expected: editjob.Summary{Changes: []string{"Спорные повторы оставил в ролике: 2."}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := &editplan.Request{Version: 1, Caption: tt.caption, Plan: tt.plan, TargetSeconds: tt.target}

			assert.Equal(t, &tt.expected, firstSummary(req, &tt.outcome))
		})
	}
}

const (
	droppedHello = "Не вошло: «привет»."
	cutNoneOfOne = "Спорные повторы: вырезал 0, оставил 1."
)

// The notes of a long talking head only cleaned, for the recommended 30–60 s.
var (
	longFailed60   = fmt.Sprintf(longFailedNote, "30–60 с", "45 секунд")
	longUnchosen60 = fmt.Sprintf(longUnchosenNote, "30–60 с", "45 секунд")
)

// TestLongNotes: the notes of a long talking head as the trainer reads them.
func TestLongNotes(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Ролик длиннее рекомендованных для рилс 30–60 с: сократить его не получилось из-за технической "+
		"ошибки, поэтому вырезал только паузы, оговорки и неудачные дубли. Если нужно короче, ответьте на черновик, "+
		"например: „сделай 45 секунд“.", longFailed60)
	assert.Equal(t, "Ролик длиннее рекомендованных для рилс 30–60 с: не смог выбрать, что оставить, поэтому вырезал "+
		"только паузы, оговорки и неудачные дубли. Если нужно короче, ответьте на черновик, например: „сделай 45 "+
		"секунд“.", longUnchosen60)
}

func TestSecondsText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    float64
		expected string
	}{
		{input: 45, expected: "45 секунд"},
		{input: 21, expected: "21 секунду"},
		{input: 22.5, expected: "23 секунды"},
		{input: 12, expected: "12 секунд"},
		{input: 111, expected: "111 секунд"},
		{input: 30, expected: "30 секунд"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, secondsText(tt.input))
		})
	}
}

func TestRevisionTexts(t *testing.T) {
	t.Parallel()

	for _, text := range []string{
		cannotRevisionText, unclearRevisionText, modelFailedText, artifactsMissingText, captionCannotNote,
		captionUnclearNote, captionFailedNote, modelFailedRevisionText, editFailedRevisionText, technicalRevisionText,
		workoutFallbackNote, titlesHint, longReelNote(180), untimedNote([]string{"Мост"}), longFailedNote,
		longUnchosenNote,
	} {
		assert.NotContains(t, text, "S3")
		assert.NotContains(t, text, "Claude")
		assert.NotContains(t, text, "модел")
		assert.NotContains(t, text, "Whisper")
	}

	assert.Equal(t, "Такое я пока не умею: могу обрезать, сократить до нужной длины, убрать или вернуть фрагмент, "+
		"выбрать дубль, поправить слово в субтитрах, подписать упражнения, поменять или убрать заголовок и убрать звук.",
		cannotRevisionText)
	assert.Equal(t, "Не понял, что именно поменять. Пример правки: „убери первые 3 секунды“ или "+
		"„оставь второй дубль“.", unclearRevisionText)

	for _, text := range []string{modelFailedRevisionText, editFailedRevisionText, technicalRevisionText} {
		assert.NotContains(t, text, "исходник", "a revision does not ask for the source again")
		assert.NotContains(t, text, "правк", "the note around it already speaks of the revision")
	}
}

func TestFailureText(t *testing.T) {
	t.Parallel()

	e := newTestExecutor(&fakeBoard{})

	for _, code := range []editjob.FailureCode{
		editjob.SourceMissingCode, editjob.SourceTooLargeCode, editjob.SourceTooLongCode, editjob.BadSourceCode,
		editjob.NoAudioCode, editjob.NoSpeechCode, editjob.TimeoutCode, editjob.QueueTimeoutCode,
	} {
		text := e.failureText(&editjob.Result{Code: code, SourceDuration: 700, MaxDuration: 600})

		assert.NotEmpty(t, text, code)
		assert.NotContains(t, text, string(code), "no codes for the trainer")
		assert.NotContains(t, text, "OPENAI", "no setup details for the trainer")
	}

	assert.Equal(t, "Исходник длится 11:40, а монтирую я видео до 10 мин. Разрежьте его на части и пришлите по одной.",
		e.failureText(&editjob.Result{Code: editjob.SourceTooLongCode, SourceDuration: 700, MaxDuration: 600}))
	assert.Equal(t, "Монтаж этого видео занял больше 45 мин и был остановлен — скорее всего, оно слишком длинное. "+
		"Разрежьте его на части и пришлите по одной.", runningTimeoutText(testTimeout))
	assert.Equal(t, "Очередь на монтаж была слишком длинной: за 3 ч до этого видео дело не дошло. "+
		"Пришлите исходник ещё раз чуть позже.", queueTimeoutText(testQueue))
	assert.NotContains(t, technicalText, "OPENAI")
}

// TestDescribeWorkoutFitsCaption: the description of a workout stays within the Telegram caption of a video (1024
// UTF-16 units, with room for the link to the source) in the worst case — six long changes, the disputed repeats, 20
// titles, 20 exercises without a time, a 300-character caption, a long reel, the fallback without the model: the
// notes stand right after the header, the lists are cut from the tail with a note.
func TestDescribeWorkoutFitsCaption(t *testing.T) {
	t.Parallel()

	const (
		captionLimit = 1024
		linkRoom     = 100 // «Исходник: https://t.me/c/…» after a blank line
		titlesCount  = 20
		changesCount = 6
	)

	name := strings.Repeat("я", 40)
	caption := strings.Repeat("в", 300)
	titles, untimed := make([]string, 0, titlesCount), make([]string, 0, titlesCount)
	changes := make([]string, 0, changesCount)

	for range titlesCount {
		titles = append(titles, "0:15 — «"+name+"»")
		untimed = append(untimed, name)
	}

	for range changesCount {
		changes = append(changes, "Вырезал фрагмент «"+strings.Repeat("ф", 60)+"».")
	}

	workout := &roughcut.Plan{
		Edit: &roughcut.Edit{Mode: roughcut.WorkoutMode}, Candidates: []roughcut.Candidate{{ID: 0}, {ID: 1}},
	}
	result := &editjob.Result{SourceDuration: 400, Duration: 380}
	long := longReelNote(testReelsMax.Seconds())

	tests := []struct {
		name      string
		outcome   editplan.Outcome
		note      string
		shortened bool
	}{
		{
			name: "every change, title and exercise",
			outcome: editplan.Outcome{
				Kind: editplan.EditOutcome, Mode: roughcut.WorkoutMode, Changes: changes, Cut: 1, Titles: titles,
				Untimed: untimed,
			},
			shortened: true,
		},
		{
			name:    "without the model",
			outcome: editplan.Outcome{Kind: editplan.FailedOutcome},
			note:    workoutFallbackNote,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			summary := firstSummary(&editplan.Request{Version: 1, Caption: caption, Plan: workout}, &tt.outcome)
			text := Describe(result, 1, caption, summary, long)

			assert.LessOrEqual(t, len(utf16.Encode([]rune(text))), captionLimit-linkRoom, text)

			lines := strings.Split(text, "\n")
			require.GreaterOrEqual(t, len(lines), 3)
			assert.Equal(t, long, lines[1], "the length note right after the header")

			if tt.note != "" {
				assert.Equal(t, tt.note, lines[2])
			}

			assert.Equal(t, tt.shortened, strings.HasSuffix(text, shortenedNote))
			assert.NotContains(t, text, strings.Repeat("в", maxWorkoutCaptionRunes+1), "the caption is cut")
		})
	}
}

func TestFitDescription(t *testing.T) {
	t.Parallel()

	line := strings.Repeat("д", 300)

	tests := []struct {
		name     string
		tail     []string
		expected string
	}{
		{name: "everything fits", tail: []string{"а", "б"}, expected: "заголовок\nа\nб"},
		{
			name:     "the tail is cut with a note",
			tail:     []string{line, line, line, line},
			expected: "заголовок\n" + line + "\n" + line + "\n" + shortenedNote,
		},
		{name: "a last line that does not fit", tail: []string{line, line, strings.Repeat("д", 400)},
			expected: "заголовок\n" + line + "\n" + line + "\n" + shortenedNote},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := fitDescription([]string{"заголовок"}, tt.tail)
			assert.Equal(t, tt.expected, got)
			assert.LessOrEqual(t, utf16Len(got), maxDescriptionUnits)
		})
	}
}
