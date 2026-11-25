package editplan

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

const (
	// maxHookRunes of the hook title: two lines of large capitals (subtitles.HookStyle) at most.
	maxHookRunes = 40
	// minHookWords and maxHookWords: a hook is a short phrase, not a word or a sentence.
	minHookWords = 2
	maxHookWords = 6
	// firstPictograph: the emoji and pictograph blocks start here (U+1F000).
	firstPictograph = 0x1F000
	// emojiPresentation is the variation selector that turns a character into an emoji (U+FE0F).
	emojiPresentation = 0xFE0F
)

// hookSymbols are the symbols allowed in a hook although Unicode puts them with the pictographs (So).
const hookSymbols = "№°"

// checkHook checks the hook title of the decision: none (nil) or "" (removed) pass; a text is 2–6 words of at most
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
		return checkFailedf("заголовок «%s» длиннее %d символов: сократи до 2–6 слов", truncate(text, maxHookRunes),
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

// normalizeHook is the hook as the reel shows it: spaces collapsed.
func normalizeHook(hook string) string {
	return strings.Join(strings.Fields(hook), " ")
}

// nextHook is the hook title of the new version: the decision's (checked), or the previous version's.
func nextHook(previous *roughcut.Edit, in *Input) string {
	if in.HookTitle == nil {
		return previous.Hook
	}

	return normalizeHook(*in.HookTitle)
}

// hookLines: «Заголовок: «…».» when the decision sets a new hook title, «Убрал заголовок.» when it removes one.
func hookLines(previous *roughcut.Edit, in *Input) []string {
	hook := nextHook(previous, in)

	switch hook {
	case previous.Hook:
		return nil
	case "":
		return []string{hookRemovedLine}
	default:
		return []string{"Заголовок: «" + hook + "»."}
	}
}

// hookRemovedLine: the revision removed the hook title.
const hookRemovedLine = "Убрал заголовок."
