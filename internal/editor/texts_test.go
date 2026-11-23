package editor

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Prokud1n/ai-agents/internal/editjob"
)

func TestDescribe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		result   editjob.Result
		caption  string
		expected string
	}{
		{
			name:    "cut with the trainer's caption",
			result:  editjob.Result{SourceDuration: 182.4, Duration: 141.2},
			caption: " про планку ",
			expected: "Черновик рилс готов: было 3:02, стало 2:21 — вырезал 0:41 (паузы, оговорки и неудачные дубли).\n" +
				"Ваша подпись к исходнику: «про планку»",
		},
		{
			name:   "disputed repeats are left in",
			result: editjob.Result{SourceDuration: 60, Duration: 45, Candidates: 2},
			expected: "Черновик рилс готов: было 1:00, стало 0:45 — вырезал 0:15 (паузы, оговорки и неудачные дубли).\n" +
				"Спорные повторы оставил в ролике: 2. Их разберём на следующем шаге.",
		},
		{
			name:     "nothing to cut",
			result:   editjob.Result{SourceDuration: 30.2, Duration: 30},
			expected: "Черновик рилс готов: 0:30, вырезать было почти нечего.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, Describe(&tt.result, tt.caption))
		})
	}
}

func TestDescribeTruncatesLongCaption(t *testing.T) {
	t.Parallel()

	got := Describe(&editjob.Result{SourceDuration: 60, Duration: 50}, strings.Repeat("а", maxCaptionRunes*2))

	assert.Less(t, len([]rune(got)), 1024, "fits a Telegram video caption")
	assert.True(t, strings.HasSuffix(got, "…»"))
}

func TestFailureText(t *testing.T) {
	t.Parallel()

	for _, code := range []editjob.FailureCode{
		editjob.NotConfiguredCode, editjob.TranscriptionRejectedCode, editjob.FontsCode, editjob.SourceMissingCode,
		editjob.SourceTooLargeCode, editjob.SourceTooLongCode, editjob.BadSourceCode, editjob.NoAudioCode,
		editjob.NoSpeechCode, editjob.TimeoutCode, editjob.TemporaryCode,
	} {
		text := failureText(&editjob.Result{Code: code, SourceDuration: 700, MaxDuration: 600})

		assert.NotEmpty(t, text, code)
		assert.NotContains(t, text, string(code), "no codes for the trainer")
	}

	assert.Equal(t, "Исходник длится 11:40, а монтирую я видео до 10 мин. Разрежьте его на части и пришлите по одной.",
		failureText(&editjob.Result{Code: editjob.SourceTooLongCode, SourceDuration: 700, MaxDuration: 600}))
	assert.Equal(t, "Монтаж не уложился в отведённые 45 мин. Пришлите исходник ещё раз или разрежьте его на части.",
		timeoutText(testTimeout))
}
