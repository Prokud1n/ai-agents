package editor

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Prokud1n/ai-agents/internal/editjob"
)

const (
	secondsPerMinute = 60
	// maxCaptionRunes of the trainer's caption shown in the description: the whole caption is kept in the task, the
	// description stays within the Telegram caption of a video (1024 characters).
	maxCaptionRunes = 300
	// noCutSeconds: less than this cut is "nothing to cut".
	noCutSeconds = 0.5
)

// Texts for the trainer. A failure is shown after «Не получилось выполнить задачу.» (tasks).
const (
	noSourceText   = "Не нашёл видео в задаче. Пришлите исходник в «Исходники» ещё раз."
	editFailedText = "Монтаж не удался из-за технической ошибки, я уже пробовал несколько раз. " +
		"Пришлите исходник ещё раз чуть позже."
	notConfiguredText = "Монтаж не настроен: не задан ключ распознавания речи (OPENAI_API_KEY). " +
		"Пришлите исходник ещё раз, когда ключ появится."
	transcriptionRejectedText = "Сервис распознавания речи не принял запрос: проверьте ключ и баланс OpenAI " +
		"(OPENAI_API_KEY)."
	fontsText          = "Монтаж не настроен: не нашлись фирменные шрифты субтитров."
	sourceMissingText  = "Не нашёл исходник в хранилище. Пришлите видео ещё раз."
	sourceTooLargeText = "Исходник слишком большой, такой я не скачиваю. Сожмите видео или разрежьте его на части."
	badSourceText      = "Не смог прочитать видео. Пришлите его ещё раз — лучше исходным файлом из галереи."
	noAudioText        = "В видео нет звука, а монтирую я по речи. Пришлите видео со звуком."
	timeoutReasonText  = "Монтаж не уложился в отведённое время. Пришлите исходник ещё раз или разрежьте его на части."
	noSpeechText       = "Не нашёл в видео речи, по которой можно резать. Пришлите видео, где вы говорите в кадре."
)

// failureText is the trainer's reason of a failed job, by its code.
func failureText(r *editjob.Result) string {
	switch r.Code {
	case editjob.NotConfiguredCode:
		return notConfiguredText
	case editjob.TranscriptionRejectedCode:
		return transcriptionRejectedText
	case editjob.FontsCode:
		return fontsText
	case editjob.SourceMissingCode:
		return sourceMissingText
	case editjob.SourceTooLargeCode:
		return sourceTooLargeText
	case editjob.SourceTooLongCode:
		return tooLongText(r.SourceDuration, r.MaxDuration)
	case editjob.BadSourceCode:
		return badSourceText
	case editjob.NoAudioCode:
		return noAudioText
	case editjob.NoSpeechCode:
		return noSpeechText
	case editjob.TimeoutCode:
		return timeoutReasonText
	default:
		return editFailedText
	}
}

// tooLongText: «Исходник длится 12:30, а монтирую я видео до 10 мин. Разрежьте его на части и пришлите по одной.»
func tooLongText(duration, limit float64) string {
	return fmt.Sprintf("Исходник длится %s, а монтирую я видео до %s. Разрежьте его на части и пришлите по одной.",
		clock(duration), minutesText(limit))
}

// timeoutText is the failure of a job with no result by its deadline.
func timeoutText(timeout time.Duration) string {
	return fmt.Sprintf("Монтаж не уложился в отведённые %s. Пришлите исходник ещё раз или разрежьте его на части.",
		minutesText(timeout.Seconds()))
}

// Describe is the description of the video draft: the durations before and after, what was cut, the disputed
// repeats left in the reel and the trainer's caption.
func Describe(r *editjob.Result, caption string) string {
	lines := make([]string, 0, 3)

	removed := r.SourceDuration - r.Duration
	if removed < noCutSeconds {
		lines = append(lines, fmt.Sprintf("Черновик рилс готов: %s, вырезать было почти нечего.", clock(r.Duration)))
	} else {
		lines = append(lines, fmt.Sprintf("Черновик рилс готов: было %s, стало %s — вырезал %s (паузы, оговорки "+
			"и неудачные дубли).", clock(r.SourceDuration), clock(r.Duration), clock(removed)))
	}

	if r.Candidates > 0 {
		lines = append(lines, fmt.Sprintf("Спорные повторы оставил в ролике: %d. Их разберём на следующем шаге.",
			r.Candidates))
	}

	if text := strings.TrimSpace(caption); text != "" {
		lines = append(lines, "Ваша подпись к исходнику: «"+truncate(text, maxCaptionRunes)+"»")
	}

	return strings.Join(lines, "\n")
}

// clock formats seconds as m:ss.
func clock(seconds float64) string {
	total := int(math.Round(max(seconds, 0)))

	return fmt.Sprintf("%d:%02d", total/secondsPerMinute, total%secondsPerMinute)
}

// minutesText is a whole number of minutes as «10 мин» (no plural forms needed), any other duration as m:ss.
func minutesText(seconds float64) string {
	total := int(math.Round(seconds))
	if total%secondsPerMinute != 0 {
		return clock(seconds)
	}

	return fmt.Sprintf("%d мин", total/secondsPerMinute)
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}

	return strings.TrimSpace(string(runes[:limit])) + "…"
}
