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
	hookLines    = 4
	hookMaxRunes = 90
	// phraseTail: the hook stays at most this long after the end of the first phrase.
	phraseTail = 0.5
	// minHookKeyRunes: a word shorter than this is not the hook's key word (unless it has a digit).
	minHookKeyRunes = 5
	// maxHangingRunes: a word this short does not end the first line of the hook.
	maxHangingRunes = 2
	millisPerSecond = 1000
	// Limits of the hook values in a style file.
	minHookDuration = 1.0
	maxHookDuration = 10.0
	maxHookFade     = 500
	maxHookGap      = 200
	// minHookFitScale: the longest hook squeezed into hookLines lines no narrower than this (\fscx).
	minHookFitScale = 85
)

// HookStyle is the look of the hook title: text over the first seconds of the reel, at the subtitles' edge (at the
// top by default; at the top for subtitles in the middle). While it is shown, the subtitles stand right under it
// (above it at the bottom), clear of it.
type HookStyle struct {
	TextStyle `yaml:",inline"`

	// Highlight colors one key word of the hook (a number with its word, otherwise the longest word); empty: none.
	Highlight string `yaml:"highlight"`
	// The hook is shown until phraseTail after the first phrase, but at least MinDuration and at most MaxDuration
	// seconds (MinDuration without speech), and never past the reel.
	MinDuration float64 `yaml:"min_duration"`
	MaxDuration float64 `yaml:"max_duration"`
	// Box is the color of a plate under the lines, "#RRGGBBAA"; empty: none, the outline and the shadow of the
	// subtitles. BoxPadding is the plate around the text, in pixels.
	Box        string  `yaml:"box"`
	BoxPadding float64 `yaml:"box_padding"`
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

// hookHeight is the height of a hook of the lines, with its plate.
func (s *Style) hookHeight(lines int) int {
	height := float64(lines*s.Hook.Size) * lineHeightFactor
	if s.Hook.Box != "" {
		height += 2 * s.Hook.BoxPadding
	}

	return int(height)
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
// hookMaxRunes by whole words.
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

	words := strings.Fields(clean)

	// Cut to hookMaxRunes by whole words (a single long word is cut inside).
	for len(words) > 1 && lineRunes(words) > hookMaxRunes {
		words = words[:len(words)-1]
	}

	if runes := []rune(strings.Join(words, " ")); len(runes) > hookMaxRunes {
		words = []string{string(runes[:hookMaxRunes])}
	}
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
	errs = append(errs, validateHookBox(h)...)
	if len(errs) > 0 {
		return errs
	}

	return validateHookPlace(s)
}

// validateHookValues checks the duration, the fade, the gap and the character width of the hook.
func validateHookValues(h *HookStyle) []error {
	var errs []error

	if h.MaxDuration < minHookDuration || h.MaxDuration > maxHookDuration {
		errs = append(errs, fmt.Errorf("hook.max_duration must be %.0f..%.0f s", minHookDuration, maxHookDuration))
	}

	if h.MinDuration < minHookDuration || h.MinDuration > h.MaxDuration {
		errs = append(errs, fmt.Errorf("hook.min_duration must be %.0f s..hook.max_duration", minHookDuration))
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

// validateHookBox checks the plate under the hook.
func validateHookBox(h *HookStyle) []error {
	var errs []error

	if h.BoxPadding < 0 || h.BoxPadding > maxBorder {
		errs = append(errs, fmt.Errorf("hook.box_padding must be 0..%d", maxBorder))
	}

	if h.Box != "" {
		if err := validateColor("hook.box", h.Box); err != nil {
			errs = append(errs, err)
		}
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

// hookEnd is when the hook disappears: phraseTail after the first phrase of the speech, clamped to the style's
// min_duration..max_duration (min_duration without speech), and at the end of the reel at the latest; 0 without a
// hook.
func hookEnd(hook string, result *transcription.Result, style *Style) float64 {
	if len(hookWords(hook, style)) == 0 {
		return 0
	}

	h := &style.Hook
	end := h.MinDuration

	if phrase, ok := firstPhraseEnd(result); ok {
		end = min(max(phrase+phraseTail, h.MinDuration), h.MaxDuration)
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

// splitHook wraps the words by words into at most hookLines lines as even as possible, without a short word (a
// preposition: «в», «за») left at the end of a line when there is another way; a line is [from, to) of the words.
// Lines longer than maxChars (a long hook or word) are squeezed by the caller.
func splitHook(words []string, maxChars int) [][2]int {
	lines := min(len(wrapHook(words, maxChars)), hookLines)

	for width := (lineRunes(words) + lines - 1) / lines; ; width++ {
		if wrapped := wrapHook(words, width); len(wrapped) <= lines {
			return wrapped
		}
	}
}

// wrapHook wraps the words greedily into lines of at most width characters (a longer word takes a line of its own).
func wrapHook(words []string, width int) [][2]int {
	var lines [][2]int

	for from := 0; from < len(words); {
		to := from + 1
		for to < len(words) && lineRunes(words[from:to+1]) <= width {
			to++
		}

		if to < len(words) && to-from > 1 && utf8.RuneCountInString(words[to-1]) <= maxHangingRunes {
			to--
		}

		lines = append(lines, [2]int{from, to})
		from = to
	}

	return lines
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

// hookStyleLine is the style of the hook at the hook's edge: on its plate when the style has one, otherwise with the
// outline and the shadow of the subtitles.
func hookStyleLine(style *Style) string {
	h := &style.Hook
	alignment, marginV := placement(&Layout{
		Position: style.hookPosition(), MarginTop: style.hookMargin(), MarginBottom: style.hookMargin(),
	})

	borderStyle, outlineColor, outline := outlineBorderStyle, style.Outline.Color, style.Outline.Size
	shadowColor, shadow := style.Shadow.Color, style.Shadow.Size

	if h.Box != "" {
		borderStyle, outlineColor, outline, shadow = boxBorderStyle, h.Box, h.BoxPadding, 0
	}

	bold := 0
	if h.Bold {
		bold = assBold
	}

	fields := []string{
		hookStyleName, h.Font, strconv.Itoa(h.Size),
		assColor(h.Color), assColor(h.Color), assColor(outlineColor), assColor(shadowColor),
		strconv.Itoa(bold), "0", "0", "0", "100", "100", "0", "0", strconv.Itoa(borderStyle),
		formatFloat(outline), formatFloat(shadow),
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

// shadeEvent is the drawing that extends the vignette under the hook and the subtitles shown with it, from the start
// of the reel to the end of the hook or of the last of those subtitles, fading like the hook; "" without a hook, a
// vignette or subtitles to move. It is the first event, on layer 0, under every text.
func shadeEvent(cues []cue, style *Style, hook hookLayout) string {
	v := style.Vignette
	if hook.end <= 0 || hook.margin == 0 || !v.Enabled || style.Layout.Position == MiddlePosition {
		return ""
	}

	end := hook.end

	for _, c := range cues {
		if c.Start < hook.end {
			end = max(end, c.End)
		}
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
