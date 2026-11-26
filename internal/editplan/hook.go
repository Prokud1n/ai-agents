package editplan

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

const (
	// maxHookRunes of the hook title: four lines of subtitles.HookStyle at most.
	maxHookRunes = 90
	// minHookWords and maxHookWords: a hook is a word, a phrase or one short sentence.
	minHookWords = 1
	maxHookWords = 14
	// firstPictograph: the emoji and pictograph blocks start here (U+1F000).
	firstPictograph = 0x1F000
	// emojiPresentation is the variation selector that turns a character into an emoji (U+FE0F).
	emojiPresentation = 0xFE0F
)

// hookSymbols are the symbols allowed in a hook although Unicode puts them with the pictographs (So).
const hookSymbols = "№°"

// checkHook checks the hook title of the decision: none (nil) or "" (removed) pass; a text is 1–14 words of at most
// maxHookRunes characters on one line, without markup, emoji or hashtags.
func checkHook(hook *string) error {
	if hook == nil {
		return nil
	}

	text := normalizeHook(*hook)
	words := len(strings.Fields(text))

	switch {
	case text == "":
		return nil
	case strings.ContainsFunc(*hook, forbiddenRune):
		return checkFailedf("заголовок «%s»: без переносов строк, невидимых символов и { } \\ < >",
			truncate(oneLine(*hook), maxHookRunes))
	case utf8.RuneCountInString(text) > maxHookRunes:
		return checkFailedf("заголовок «%s» длиннее %d символов: сократи до одной короткой фразы", truncate(text, maxHookRunes),
			maxHookRunes)
	case words < minHookWords || words > maxHookWords:
		return checkFailedf("заголовок «%s»: от %d до %d слов", text, minHookWords, maxHookWords)
	case strings.ContainsFunc(text, emojiRune) || strings.Contains(text, "#"):
		return checkFailedf("заголовок «%s»: без эмодзи и хештегов", text)
	}

	return nil
}

// emojiRune: an emoji, a pictograph or the selector that makes a character one (hookSymbols are text).
func emojiRune(r rune) bool {
	return r >= firstPictograph || r == emojiPresentation ||
		(unicode.Is(unicode.So, r) && !strings.ContainsRune(hookSymbols, r))
}

// hookQuotes are the quotes the model or the trainer may put around the whole hook: the reel shows it without them.
var hookQuotes = [][2]string{{"«", "»"}, {`"`, `"`}, {"„", "“"}, {"“", "”"}}

// normalizeHook is the hook as the reel shows it: spaces collapsed, without the quotes around it and a period at the
// end (an ellipsis stays). The screen notes are normalized so too; only the model's hook title is lowered (hookOf).
func normalizeHook(hook string) string {
	text := trimHookPeriod(strings.Join(strings.Fields(hook), " "))

	for _, q := range hookQuotes {
		if len(text) > len(q[0])+len(q[1]) && strings.HasPrefix(text, q[0]) && strings.HasSuffix(text, q[1]) {
			text = strings.TrimSpace(text[len(q[0]) : len(text)-len(q[1])])

			break
		}
	}

	return trimHookPeriod(text)
}

// hookOf is the hook title of the decision as the reel shows it: normalized and, unless the trainer dictated it (her
// caption or revision has the text as it is), from a small letter like the hooks of the reference reels.
func hookOf(req *Request, raw string) string {
	text := normalizeHook(raw)
	if text == "" || strings.Contains(trainerText(req), text) {
		return text
	}

	return lowerFirst(text)
}

// trainerText is the trainer's caption of the first version or her revision.
func trainerText(req *Request) string {
	if req.IsRevision() {
		return req.Revision
	}

	return req.Caption
}

// lowerFirst puts the first letter of the first word in lower case unless another letter of the word is a capital: an
// abbreviation («ЗОЖ», «SMART-цели») or a name written so («YouTube»). A proper name is not told from a word: the
// model is told to start the hook with another word.
func lowerFirst(text string) string {
	first, _, _ := strings.Cut(text, " ")
	at := -1

	for i, r := range first {
		switch {
		case !unicode.IsLetter(r):
		case at < 0 && !unicode.IsUpper(r):
			return text
		case at < 0:
			at = i
		case unicode.IsUpper(r):
			return text
		}
	}

	if at < 0 {
		return text
	}

	r, size := utf8.DecodeRuneInString(text[at:])

	return text[:at] + string(unicode.ToLower(r)) + text[at+size:]
}

// trimHookPeriod drops one period at the end; an ellipsis stays.
func trimHookPeriod(text string) string {
	if strings.HasSuffix(text, ".") && !strings.HasSuffix(text, "..") {
		return strings.TrimSpace(strings.TrimSuffix(text, "."))
	}

	return text
}

// checkHookOnly refuses cuts, trims, the workout mode, exercise titles and muting in a first version stopped only for
// the hook title (the cleanup off): there is no caption, the trainer asked for nothing, so the model does not change
// the reel on its own initiative; the hook and the screen notes (they cut nothing) pass.
func checkHookOnly(req *Request, in *Input) error {
	if !req.HookOnly() {
		return nil
	}

	cuts := in.TrimStart > 0 || in.TrimEnd > 0 || len(in.Remove) > 0 || len(in.CutSeconds) > 0 || in.TargetSeconds > 0
	extras := in.Mode == roughcut.WorkoutMode || len(in.AddTitles) > 0 || (in.Mute != nil && *in.Mute)

	if cuts || extras {
		return checkFailedf("в этом ролике ничего не вырезай, не обрезай и не меняй режим, подписи упражнений и звук: " +
			"подписи нет, тренер ни о чём не просила — укажи только hook_title и screen_notes (или не указывай их)")
	}

	return nil
}

// nextHook is the hook title of the new version: the decision's (checked), or the previous version's.
func nextHook(req *Request, previous *roughcut.Edit, in *Input) string {
	if in.HookTitle == nil {
		return previous.Hook
	}

	return hookOf(req, *in.HookTitle)
}

// hookLines: «Заголовок: «…».» when the decision sets a new hook title, «Убрал заголовок.» when it removes one.
func hookLines(req *Request, previous *roughcut.Edit, in *Input) []string {
	hook := nextHook(req, previous, in)

	switch hook {
	case previous.Hook:
		return nil
	case "":
		return []string{hookRemovedLine}
	default:
		return []string{HookLine(hook)}
	}
}

// HookLine is the line of the description about a hook title set: «Заголовок: «…».».
func HookLine(hook string) string {
	return "Заголовок: «" + hook + "»."
}

// checkedHook is the hook title of an input that failed the checks, when the hook itself passed them: a first version
// keeps it although the rest of the decision is not applied; "" otherwise.
func checkedHook(req *Request, in *Input) string {
	if req.IsRevision() || in.HookTitle == nil || checkHook(in.HookTitle) != nil {
		return ""
	}

	return hookOf(req, *in.HookTitle)
}

// hookRemovedLine: the revision removed the hook title.
const hookRemovedLine = "Убрал заголовок."
