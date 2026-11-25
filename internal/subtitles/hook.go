package subtitles

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const (
	hookStyleName = "Hook"
	// hookLayer draws the hook title above the subtitles and the exercise titles.
	hookLayer = 2
	// hookLines: the hook is at most this many lines; hookMaxRunes is the longest hook the editor writes (editplan).
	hookLines    = 2
	hookMaxRunes = 40
	// phraseTail: the hook stays at most this long after the end of the first phrase.
	phraseTail = 0.5
	// minHookKeyRunes: a word shorter than this is not the hook's key word (unless it has a digit).
	minHookKeyRunes = 5
	// maxHangingRunes: a word this short does not end the first line of the hook.
	maxHangingRunes = 2
	millisPerSecond = 1000
	// Limits of the hook values in a style file.
	minHookDuration = 1.0
	maxHookDuration = 5.0
	maxHookFade     = 500
	maxHookGap      = 200
	// minHookFitScale: the 40 characters of the longest hook squeezed into two lines no narrower than this (\fscx).
	minHookFitScale = 85
)

// HookStyle is the look of the hook title: large text over the first seconds of the reel, at the subtitles' edge
// (at the top for subtitles in the middle). While it is shown, the subtitles stand right under it (above it at the
// bottom), clear of it.
type HookStyle struct {
	TextStyle `yaml:",inline"`

	// Highlight colors one key word of the hook (a number with its word, otherwise the longest word); empty: none.
	Highlight string `yaml:"highlight"`
	// Duration is the longest the hook is shown, in seconds; it ends at most phraseTail after the first phrase and
	// at the end of the reel.
	Duration float64 `yaml:"duration"`
	// MinDuration: a short first phrase does not end the hook before this, in seconds (the reel still does).
	MinDuration float64 `yaml:"min_duration"`
	// Fade in and out, in milliseconds.
	Fade int `yaml:"fade"`
	// Gap between the hook and the subtitles shown with it, in pixels.
	Gap int `yaml:"gap"`
	// CharWidth is the average width of a character in font sizes, for the estimate of a line (as layout.char_width).
	CharWidth float64 `yaml:"char_width"`
}

// hookPosition is the edge the hook stands at: the subtitles' one, the top for subtitles in the middle.
func (s *Style) hookPosition() Position {
	if s.Layout.Position == BottomPosition {
		return BottomPosition
	}

	return TopPosition
}

// hookMargin is the distance of the hook from its edge: the subtitles' margin.
func (s *Style) hookMargin() int {
	if s.hookPosition() == BottomPosition {
		return s.Layout.MarginBottom
	}

	return s.Layout.MarginTop
}

// hookSpan is the vertical extent of the tallest hook, of hookLines lines.
func (s *Style) hookSpan() (top, bottom int) {
	return blockSpan(s.hookPosition(), s.hookMargin(), s.hookHeight(hookLines))
}

// hookHeight is the height of a hook of the lines.
func (s *Style) hookHeight(lines int) int {
	return int(float64(lines*s.Hook.Size) * lineHeightFactor)
}

// hookSubtitlesMargin is the margin of the subtitles shown with a hook of the lines: right after it from the same
// edge. 0 for subtitles in the middle (they stay where they are) or without a hook.
func (s *Style) hookSubtitlesMargin(lines int) int {
	if s.Layout.Position == MiddlePosition || lines <= 0 {
		return 0
	}

	return s.hookMargin() + s.hookHeight(lines) + s.Hook.Gap
}

// hookLineCount is how many lines the hook takes, 0 without one.
func (s *Style) hookLineCount(hook string) int {
	words := hookWords(hook, s)
	if len(words) == 0 {
		return 0
	}

	return len(splitHook(words, s.hookLineChars()))
}

// hookWords are the words of the hook as it is drawn, in capitals when the style says so. Defense in depth (the app
// checks the text): control characters become spaces, invisible format characters are dropped, the text is cut to
// hookMaxRunes.
func hookWords(hook string, style *Style) []string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r):
			return ' '
		case unicode.Is(unicode.Cf, r):
			return -1
		default:
			return r
		}
	}, hook)

	if runes := []rune(clean); len(runes) > hookMaxRunes {
		clean = string(runes[:hookMaxRunes])
	}

	words := strings.Fields(clean)
	if style.Hook.Uppercase {
		for i := range words {
			words[i] = strings.ToUpper(words[i])
		}
	}

	return words
}

// hookLineChars is how many characters of the hook fit on a line between the side margins.
func (s *Style) hookLineChars() int {
	width := float64(FrameWidth - 2*s.Layout.MarginSide)

	return max(int(width/(float64(s.Hook.Size)*s.Hook.CharWidth)), 1)
}

// validateHook checks the hook look and that the hook and the subtitles shown with it stay clear of each other, of the
// exercise titles and of the Instagram zones.
func validateHook(s *Style) []error {
	h := &s.Hook
	errs := validateText("hook", &h.TextStyle)

	if h.Highlight != "" {
		if err := validateColor("hook.highlight", h.Highlight); err != nil {
			errs = append(errs, err)
		} else if opacity(h.Highlight) < minTextOpacity {
			errs = append(errs, errors.New("hook.highlight must be at least half opaque (AA >= 80)"))
		}
	}

	errs = append(errs, validateHookValues(h)...)
	if len(errs) > 0 {
		return errs
	}

	return validateHookPlace(s)
}

// validateHookValues checks the duration, the fade, the gap and the character width of the hook.
func validateHookValues(h *HookStyle) []error {
	var errs []error

	if h.Duration < minHookDuration || h.Duration > maxHookDuration {
		errs = append(errs, fmt.Errorf("hook.duration must be %.0f..%.0f s", minHookDuration, maxHookDuration))
	}

	if h.MinDuration <= 0 || h.MinDuration > h.Duration {
		errs = append(errs, errors.New("hook.min_duration must be positive and at most hook.duration"))
	}

	if h.Fade < 0 || h.Fade > maxHookFade || float64(2*h.Fade) >= h.MinDuration*millisPerSecond {
		errs = append(errs, fmt.Errorf("hook.fade must be 0..%d ms and shorter than half of hook.min_duration",
			maxHookFade))
	}

	if h.Gap < 0 || h.Gap > maxHookGap {
		errs = append(errs, fmt.Errorf("hook.gap must be 0..%d", maxHookGap))
	}

	if h.CharWidth < minCharWidth || h.CharWidth > maxCharWidth {
		errs = append(errs, fmt.Errorf("hook.char_width must be %.1f..%.1f", minCharWidth, maxCharWidth))
	}

	return errs
}

// validateHookPlace checks the hook's place: the longest hook fits two lines, and the hook and the subtitles under it
// stay between the Instagram zones and clear of the exercise titles.
func validateHookPlace(s *Style) []error {
	var errs []error

	if need := (hookMaxRunes + hookLines - 1) / hookLines; s.hookLineChars()*percentBase < need*minHookFitScale {
		errs = append(errs, fmt.Errorf("a hook of %d characters does not fit %d lines between the margins: reduce "+
			"hook.size", hookMaxRunes, hookLines))
	}

	hookTop, hookBottom := s.hookSpan()
	titleTop, titleBottom := s.titleSpan()

	if outsideZones(hookTop, hookBottom) {
		errs = append(errs, fmt.Errorf("hook (%d..%d px) must stay between the Instagram zones (%d..%d px)",
			hookTop, hookBottom, SafeZoneTop, FrameHeight-SafeZoneBottom))
	}

	if hookTop < titleBottom && titleTop < hookBottom {
		errs = append(errs, fmt.Errorf("hook (%d..%d px) overlaps the titles (%d..%d px)", hookTop, hookBottom,
			titleTop, titleBottom))
	}

	margin := s.hookSubtitlesMargin(hookLines)
	if margin == 0 {
		if subTop, subBottom := s.subtitlesSpan(); hookTop < subBottom && subTop < hookBottom {
			errs = append(errs, fmt.Errorf("hook (%d..%d px) overlaps the subtitles in the middle (%d..%d px)",
				hookTop, hookBottom, subTop, subBottom))
		}

		return errs
	}

	subTop, subBottom := blockSpan(s.Layout.Position, margin, s.blockHeight())

	if outsideZones(subTop, subBottom) || (subTop < titleBottom && titleTop < subBottom) {
		errs = append(errs, fmt.Errorf("subtitles under the hook (%d..%d px) must stay between the Instagram zones "+
			"and clear of the titles (%d..%d px): reduce hook.size or hook.gap", subTop, subBottom, titleTop, titleBottom))
	}

	return errs
}

// outsideZones: the block leaves the space between the Instagram zones.
func outsideZones(top, bottom int) bool {
	return top < SafeZoneTop || bottom > FrameHeight-SafeZoneBottom
}

// hookEnd is when the hook disappears: after the style's duration, at most phraseTail after the first phrase of the
// speech but not before the style's min_duration, and at the end of the reel at the latest; 0 without a hook.
func hookEnd(hook string, result *transcription.Result, style *Style) float64 {
	if len(hookWords(hook, style)) == 0 {
		return 0
	}

	end := style.Hook.Duration

	if phrase, ok := firstPhraseEnd(result); ok {
		end = max(style.Hook.MinDuration, min(end, phrase+phraseTail))
	}

	if result.Duration > 0 {
		end = min(end, result.Duration)
	}

	return roundCS(max(end, 0))
}

// firstPhraseEnd is the end of the first phrase: the first segment of the transcript with words, or the first
// word's end without segments; false without speech.
func firstPhraseEnd(result *transcription.Result) (float64, bool) {
	if len(result.Words) == 0 {
		return 0, false
	}

	for _, seg := range result.Segments {
		if strings.TrimSpace(seg.Text) != "" && seg.End > 0 {
			return seg.End, true
		}
	}

	return result.Words[0].End, true
}

// hookEvent is the dialogue line of the hook: from the start of the reel to end, fading in and out, in capitals when
// the style says so, in at most two lines squeezed to fit, with its key word highlighted. The text is plain: it is
// escaped like speech.
func hookEvent(hook string, end float64, style *Style) string {
	words := hookWords(hook, style)
	if len(words) == 0 || end <= 0 {
		return ""
	}

	h := &style.Hook

	fade := min(h.Fade, int(end*millisPerSecond)/2)
	key := hookKey(words)
	maxChars := style.hookLineChars()

	var b strings.Builder

	fmt.Fprintf(&b, `{\fad(%d,%d)}`, fade, fade)

	prevScale := fullScaleX
	for i, line := range splitHook(words, maxChars) {
		if i > 0 {
			b.WriteString(assLineBreak)
		}

		if scale := charsScale(lineRunes(words[line[0]:line[1]]), maxChars); scale != prevScale {
			fmt.Fprintf(&b, `{\fscx%d}`, scale)
			prevScale = scale
		}

		writeHookWords(&b, words, line, key, h)
	}

	return fmt.Sprintf("Dialogue: %d,%s,%s,%s,,0,0,0,,%s\n", hookLayer, formatTime(0), formatTime(end), hookStyleName,
		b.String())
}

// writeHookWords writes the words [line[0], line[1]) with the key words in the highlight color.
func writeHookWords(b *strings.Builder, words []string, line, key [2]int, h *HookStyle) {
	for i := line[0]; i < line[1]; i++ {
		if i > line[0] {
			b.WriteByte(' ')
		}

		highlighted := h.Highlight != "" && i >= key[0] && i < key[1]
		if highlighted && (i == line[0] || i == key[0]) {
			b.WriteString(`{\1c` + assOverrideColor(h.Highlight) + `\1a` + assOverrideAlpha(h.Highlight) + `}`)
		}

		b.WriteString(escapeText(words[i]))

		if highlighted && (i+1 == line[1] || i+1 == key[1]) {
			b.WriteString(`{\1c` + assOverrideColor(h.Color) + `\1a` + assOverrideAlpha(h.Color) + `}`)
		}
	}
}

// hookKey is the key word of the hook as [from, to) of its words: a number with the word after it («10 МИНУТ»),
// otherwise the longest word of at least minHookKeyRunes letters (the first of equal ones); [0, 0) without one.
func hookKey(words []string) [2]int {
	for i, w := range words {
		if !strings.ContainsFunc(w, unicode.IsDigit) {
			continue
		}

		// A number alone is the key word with the word after it: «10 МИНУТ».
		if i+1 < len(words) && !strings.ContainsFunc(w, unicode.IsLetter) &&
			!strings.ContainsFunc(words[i+1], unicode.IsDigit) {
			return [2]int{i, i + 2}
		}

		return [2]int{i, i + 1}
	}

	best, bestLen := 0, 0

	for i, w := range words {
		if n := letterCount(w); n >= minHookKeyRunes && n > bestLen {
			best, bestLen = i, n
		}
	}

	if bestLen == 0 {
		return [2]int{}
	}

	return [2]int{best, best + 1}
}

func letterCount(w string) int {
	n := 0

	for _, r := range w {
		if unicode.IsLetter(r) {
			n++
		}
	}

	return n
}

// splitHook puts the words into one line when they fit, otherwise into two lines as even as possible, without a
// short word (a preposition: «В», «ЗА») left at the end of the first line when there is another way; a line is
// [from, to) of the words.
func splitHook(words []string, maxChars int) [][2]int {
	if lineRunes(words) <= maxChars || len(words) == 1 {
		return [][2]int{{0, len(words)}}
	}

	best, bestWidth := 1, math.MaxInt

	for i := 1; i < len(words); i++ {
		width := max(lineRunes(words[:i]), lineRunes(words[i:]))
		if utf8.RuneCountInString(words[i-1]) <= maxHangingRunes {
			width += maxChars // only if nothing else is left
		}

		if width < bestWidth {
			best, bestWidth = i, width
		}
	}

	return [][2]int{{0, best}, {best, len(words)}}
}

// lineRunes is the length of the words joined by spaces.
func lineRunes(words []string) int {
	n := len(words) - 1
	for _, w := range words {
		n += utf8.RuneCountInString(w)
	}

	return n
}

// charsScale squeezes a line of chars characters into maxChars (\fscx), no narrower than minScaleX.
func charsScale(chars, maxChars int) int {
	if chars <= maxChars {
		return fullScaleX
	}

	return max(percentBase*maxChars/chars, minScaleX)
}

// hookStyleLine is the style of the hook: the outline and the shadow of the subtitles, at the hook's edge.
func hookStyleLine(style *Style) string {
	h := &style.Hook
	alignment, marginV := placement(&Layout{
		Position: style.hookPosition(), MarginTop: style.hookMargin(), MarginBottom: style.hookMargin(),
	})

	bold := 0
	if h.Bold {
		bold = assBold
	}

	fields := []string{
		hookStyleName, h.Font, strconv.Itoa(h.Size),
		assColor(h.Color), assColor(h.Color), assColor(style.Outline.Color), assColor(style.Shadow.Color),
		strconv.Itoa(bold), "0", "0", "0", "100", "100", "0", "0", strconv.Itoa(outlineBorderStyle),
		formatFloat(style.Outline.Size), formatFloat(style.Shadow.Size),
		strconv.Itoa(alignment), strconv.Itoa(style.Layout.MarginSide), strconv.Itoa(style.Layout.MarginSide),
		strconv.Itoa(marginV), "1",
	}

	return "Style: " + strings.Join(fields, ",")
}

// hookLayout is the hook title of a document: shown until end (0: no hook), the subtitles that start before it stand
// at margin (0: where they are).
type hookLayout struct {
	end    float64
	margin int
}

// The shade under the subtitles shown with the hook: the vignette is static (burned by ffmpeg) and reaches only the
// usual place of the subtitles, so while they stand under the hook an ASS drawing darkens the frame down to them.
const (
	shadeStyleName = "Shade"
	// shadeOverlap: the shade starts this far inside the even part of the vignette, so the two merge.
	shadeOverlap = 80
	// shadePad: the shade goes this far past the subtitles under the hook.
	shadePad = 50
	// shadeBlur softens the edges of the shade (\blur), shadeOverhang keeps the blurred sides out of the frame.
	shadeBlur     = 40
	shadeOverhang = 100
	// assBlack is opaque black in the ASS &HAABBGGRR form.
	assBlack = "&H00000000"
)

// shadeEvent is the drawing that extends the vignette under the subtitles shown with the hook, from the start of the
// reel to the end of the last of them, fading like the hook; "" without a hook, a vignette or shifted subtitles. It is
// the first event, on layer 0, under every text.
func shadeEvent(cues []cue, style *Style, hook hookLayout) string {
	v := style.Vignette
	if hook.end <= 0 || hook.margin == 0 || !v.Enabled || style.Layout.Position == MiddlePosition {
		return ""
	}

	end := 0.0

	for _, c := range cues {
		if c.Start < hook.end {
			end = max(end, c.End)
		}
	}

	if end <= 0 {
		return ""
	}

	plateau, bottom := style.vignetteSpan()
	top, low := plateau-shadeOverlap, hook.margin+style.blockHeight()+shadePad

	if bottom {
		top, low = FrameHeight-low, FrameHeight-top
	}

	fade := min(style.Hook.Fade, int(end*millisPerSecond)/2)
	alpha := opaque - int(math.Round(v.Opacity*opaque))

	return fmt.Sprintf("Dialogue: 0,%s,%s,%s,,0,0,0,,{\\an7\\pos(0,0)\\fad(%d,%d)\\1c&H000000&\\1a&H%02X&\\blur%d\\p1}"+
		"m %d %d l %d %d l %d %d l %d %d{\\p0}\n", formatTime(0), formatTime(end), shadeStyleName, fade, fade, alpha,
		shadeBlur, -shadeOverhang, top, FrameWidth+shadeOverhang, top, FrameWidth+shadeOverhang, low, -shadeOverhang,
		low)
}

// shadeStyleLine is the style of the shade drawing: black, no outline or shadow (the text font only keeps libass from
// looking for another one).
func shadeStyleLine(style *Style) string {
	fields := []string{
		shadeStyleName, style.Text.Font, strconv.Itoa(style.Text.Size),
		assBlack, assBlack, assBlack, assBlack,
		"0", "0", "0", "0", "100", "100", "0", "0", strconv.Itoa(outlineBorderStyle), "0", "0",
		strconv.Itoa(topAlignment), "0", "0", "0", "1",
	}

	return "Style: " + strings.Join(fields, ",")
}
