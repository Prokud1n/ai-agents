package editor

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/editplan"
)

const (
	secondsPerMinute = 60
	secondsPerHour   = 60 * secondsPerMinute
	// maxCaptionRunes of the trainer's caption shown in the description: the whole caption is kept in the task, the
	// description stays within the Telegram caption of a video (1024 characters).
	maxCaptionRunes = 300
	// noCutSeconds: less than this cut is "nothing to cut".
	noCutSeconds = 0.5
)

// Texts for the trainer. A failure is shown after «Не получилось выполнить задачу.» (tasks).
const (
	noSourceText = "Не нашёл видео в задаче. Пришлите исходник в «Исходники» ещё раз."
	// editFailedText: a temporary failure after all the retries of the task.
	editFailedText = "Монтаж не удался из-за технической ошибки, я уже пробовал несколько раз. " +
		"Пришлите исходник ещё раз чуть позже."
	// technicalText: the setup is broken (no key, the key refused, no brand fonts, no worker); the team is told in
	// «Техничка», the trainer gets no technical details.
	technicalText = "Монтаж сейчас недоступен из-за технической неполадки — я уже передал её технической команде. " +
		"Пришлите исходник ещё раз чуть позже."
	// otherFailureText: a failure without a text of its own.
	otherFailureText   = "Не получилось смонтировать это видео. Пришлите исходник ещё раз чуть позже."
	sourceMissingText  = "Не нашёл исходник в хранилище. Пришлите видео ещё раз."
	sourceTooLargeText = "Исходник слишком большой, такой я не скачиваю. Сожмите видео или разрежьте его на части."
	badSourceText      = "Не смог прочитать видео. Пришлите его ещё раз — лучше исходным файлом из галереи."
	noAudioText        = "В видео нет звука, а монтирую я по речи. Пришлите видео со звуком."
	noSpeechText       = "Не нашёл в видео речи, по которой можно резать. Пришлите видео, где вы говорите в кадре."

	// cannotRevisionText and unclearRevisionText: a revision the editor cannot do or did not understand (shown after
	// «Не получилось применить правку.» above the previous draft).
	cannotRevisionText = "Такое я пока не умею: могу обрезать, убрать или вернуть фрагмент, выбрать дубль и поправить " +
		"слово в субтитрах. Напишите правку по-другому — сделаю новую версию."
	unclearRevisionText = "Не понял, что именно поменять. Напишите, например: „убери первые 3 секунды“ или " +
		"„оставь второй дубль“."
	// modelFailedText: the decision failed for a technical reason after all the retries of the task.
	modelFailedText = "Не получилось решить, как смонтировать видео, из-за технической ошибки — я уже пробовал " +
		"несколько раз. Пришлите исходник или правку ещё раз чуть позже."
	// artifactsMissingText: the transcript or the plan of the earlier edit is gone from the storage.
	artifactsMissingText = "Не нашёл материалы прошлого монтажа этого видео. Пришлите исходник ещё раз — " +
		"смонтирую заново."
	// captionCannotNote and captionUnclearNote: the first draft is made without the caption's instruction.
	captionCannotNote = "Подпись как указание к монтажу не применил: такое я пока не умею. Могу обрезать, убрать или " +
		"вернуть фрагмент, выбрать дубль и поправить слово в субтитрах — ответьте на черновик правкой."
	captionUnclearNote = "Подпись как указание к монтажу не применил: не понял, что именно поменять. Если нужна " +
		"правка, ответьте на черновик, например: „убери первые 3 секунды“."
)

// failureText is the trainer's reason of a failed job, by its code (technical codes get technicalText before).
func (e *Executor) failureText(r *editjob.Result) string {
	switch r.Code {
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
		return runningTimeoutText(e.jobTimeout)
	case editjob.QueueTimeoutCode:
		return queueTimeoutText(e.queueTimeout)
	default:
		return otherFailureText
	}
}

// techReportText is the report in «Техничка»: the task and the code, the details are in the logs.
func techReportText(taskID int64, code string) string {
	return fmt.Sprintf("Монтажёр: задача %d не выполнена, код %s. Подробности — в логах app и media-worker.", taskID, code)
}

// tooLongText: «Исходник длится 12:30, а монтирую я видео до 10 мин. Разрежьте его на части и пришлите по одной.»
func tooLongText(duration, limit float64) string {
	return fmt.Sprintf("Исходник длится %s, а монтирую я видео до %s. Разрежьте его на части и пришлите по одной.",
		clock(duration), minutesText(limit))
}

// runningTimeoutText: the edit ran longer than EDITOR_JOB_TIMEOUT after a worker took it — the video is too heavy.
func runningTimeoutText(timeout time.Duration) string {
	return fmt.Sprintf("Монтаж этого видео занял больше %s и был остановлен — скорее всего, оно слишком длинное. "+
		"Разрежьте его на части и пришлите по одной.", minutesText(timeout.Seconds()))
}

// queueTimeoutText: no worker took the job within EDITOR_QUEUE_TIMEOUT — the queue was too long, not the video.
func queueTimeoutText(timeout time.Duration) string {
	return fmt.Sprintf("Очередь на монтаж была слишком длинной: за %s до этого видео дело не дошло. "+
		"Пришлите исходник ещё раз чуть позже.", minutesText(timeout.Seconds()))
}

// Describe is the description of the video draft. The first version: the durations before and after, what the
// editor changed by the caption and decided on the disputed repeats (summary), the trainer's caption. A revision: the
// durations of the previous version and this one and what was changed. summary is nil for an automatic cut.
func Describe(r *editjob.Result, version int, caption string, summary *editjob.Summary) string {
	if version > 1 {
		return describeRevision(r, summary)
	}

	lines := make([]string, 0, 3)

	removed := r.SourceDuration - r.Duration
	if removed < noCutSeconds {
		lines = append(lines, fmt.Sprintf("Черновик рилс готов: %s, вырезать было почти нечего.", clock(r.Duration)))
	} else {
		lines = append(lines, fmt.Sprintf("Черновик рилс готов: было %s, стало %s — вырезал %s (паузы, оговорки "+
			"и неудачные дубли).", clock(r.SourceDuration), clock(r.Duration), clock(removed)))
	}

	switch {
	case summary != nil:
		lines = append(lines, summary.Changes...)
	case r.Candidates > 0:
		lines = append(lines, fmt.Sprintf("Спорные повторы оставил в ролике: %d.", r.Candidates))
	}

	if text := strings.TrimSpace(caption); text != "" {
		lines = append(lines, "Ваша подпись к исходнику: «"+truncate(text, maxCaptionRunes)+"»")
	}

	if summary != nil && summary.Note != "" {
		lines = append(lines, summary.Note)
	}

	return strings.Join(lines, "\n")
}

// describeRevision: «Новая версия рилс: было 0:45, стало 0:42.» and what changed.
func describeRevision(r *editjob.Result, summary *editjob.Summary) string {
	if summary == nil {
		summary = &editjob.Summary{}
	}

	header := fmt.Sprintf("Новая версия рилс: %s.", clock(r.Duration))
	if summary.PreviousDuration > 0 {
		header = fmt.Sprintf("Новая версия рилс: было %s, стало %s.", clock(summary.PreviousDuration), clock(r.Duration))
	}

	lines := append([]string{header}, summary.Changes...)
	if summary.Note != "" {
		lines = append(lines, summary.Note)
	}

	return strings.Join(lines, "\n")
}

// firstSummary is the summary of a decided first version: what the caption changed, how the disputed repeats were
// decided, and why the caption was not applied if it was not.
func firstSummary(req *editplan.Request, outcome *editplan.Outcome) *editjob.Summary {
	candidates := len(req.Plan.Candidates)
	summary := &editjob.Summary{}

	if outcome.Kind != editplan.EditOutcome {
		if candidates > 0 {
			summary.Changes = []string{fmt.Sprintf("Спорные повторы оставил в ролике: %d.", candidates)}
		}

		if strings.TrimSpace(req.Caption) != "" {
			summary.Note = captionUnclearNote
			if outcome.Kind == editplan.CannotOutcome {
				summary.Note = captionCannotNote
			}
		}

		return summary
	}

	summary.Changes = append(summary.Changes, outcome.Changes...)

	if candidates > 0 {
		summary.Changes = append(summary.Changes, fmt.Sprintf("Спорные повторы: вырезал %d, оставил %d.",
			outcome.Cut, candidates-outcome.Cut))
	}

	return summary
}

// clock formats seconds as m:ss.
func clock(seconds float64) string {
	total := int(math.Round(max(seconds, 0)))

	return fmt.Sprintf("%d:%02d", total/secondsPerMinute, total%secondsPerMinute)
}

// minutesText is a whole number of hours as «3 ч», of minutes as «10 мин» (no plural forms needed), any other
// duration as m:ss.
func minutesText(seconds float64) string {
	total := int(math.Round(seconds))

	switch {
	case total > 0 && total%secondsPerHour == 0:
		return fmt.Sprintf("%d ч", total/secondsPerHour)
	case total%secondsPerMinute == 0:
		return fmt.Sprintf("%d мин", total/secondsPerMinute)
	default:
		return clock(seconds)
	}
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}

	return strings.TrimSpace(string(runes[:limit])) + "…"
}
