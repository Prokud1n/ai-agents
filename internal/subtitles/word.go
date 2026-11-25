package subtitles

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const (
	wordStyleName = "Word"
	// topLeftAlignment is ASS \an7: the text is placed by its top left corner.
	topLeftAlignment = 7
	// Limits of the word box values in a style file.
	minWordDuration  = 0.05
	maxWordDuration  = 1.0
	minWordBoxWidth  = 200
	defaultWordBoxX  = 140
	defaultWordBoxY  = 600
	defaultWordWidth = 560
	// maxWordShown: a word before a pause stays at most this long (Whisper may stretch the last word of a phrase over
	// the pause).
	maxWordShown = 1.5
	// wordTrailing are dropped at the end of a word shown alone (the end of a sentence or a dash said apart); «?», «!»
	// and «…» stay. wordQuotes are dropped at both ends.
	wordTrailing = " .,;:—–-"
	wordQuotes   = "«»\"„“”"
)

// WordBoxStyle is the look and the place of the word layout: one word at a time on an opaque plate (ASS BorderStyle 3:
// libass draws the plate around the glyphs of the word, Padding px wider on every side, so it always fits the word),
// its top left corner at X, Y; a word wider than MaxWidth (the plate included) is squeezed (\fscx).
type WordBoxStyle struct {
	TextStyle `yaml:",inline"`

	// Lowercase shows the words in small letters, as they are said (not with the capital of a sentence).
	Lowercase bool `yaml:"lowercase"`
	// Box is the color of the plate, "#RRGGBB[AA]"; Padding its margin around the text, px.
	Box     string  `yaml:"box"`
	Padding float64 `yaml:"padding"`
	// X and Y are the top left corner of the plate, px of the 1080×1920 frame; MaxWidth its widest.
	X        int `yaml:"x"`
	Y        int `yaml:"y"`
	MaxWidth int `yaml:"max_width"`
	// MinDuration: a word shown for less (said quickly before the next one) is shown together with the next one.
	MinDuration float64 `yaml:"min_duration"`
	// CharWidth is the average width of a character in font sizes, for the estimate of a word's width.
	CharWidth float64 `yaml:"char_width"`
}

func defaultWordBox() WordBoxStyle {
	return WordBoxStyle{
		TextStyle: TextStyle{Font: defaultFont, Size: 44, Color: "#111111"},
		Lowercase: true,
		Box:       white,
		Padding:   10,
		X:         defaultWordBoxX, Y: defaultWordBoxY, MaxWidth: defaultWordWidth,
		MinDuration: 0.15,
		CharWidth:   0.55,
	}
}

// rect is a box of the frame in pixels: [left, right) × [top, bottom).
type rect struct {
	left, top, right, bottom int
}

func (r rect) overlaps(o rect) bool {
	return r.left < o.right && o.left < r.right && r.top < o.bottom && o.top < r.bottom
}

// insideZones: the box is clear of the Instagram interface (the bars at the top and the bottom, the sides).
func (r rect) insideZones() bool {
	return r.left >= SafeZoneLeft && r.right <= FrameWidth-SafeZoneRight && !outsideZones(r.top, r.bottom)
}

// band is the full width of the frame between top and bottom: the hook, the notes and the titles are centered between
// the side margins, so only their height is known.
func band(top, bottom int) rect {
	return rect{left: 0, top: top, right: FrameWidth, bottom: bottom}
}

// wordBoxRect is the place of the plate of the widest word.
func (s *Style) wordBoxRect() rect {
	w := &s.WordBox
	height := int(math.Ceil(float64(w.Size)*lineHeightFactor + 2*w.Padding))

	return rect{left: w.X, top: w.Y, right: w.X + w.MaxWidth, bottom: w.Y + height}
}

// wordLineChars is how many characters of a word fit the plate unsqueezed.
func (s *Style) wordLineChars() int {
	w := &s.WordBox
	width := float64(w.MaxWidth) - 2*w.Padding

	return max(int(width/(float64(w.Size)*w.CharWidth)), 1)
}

// validateWordBox checks the word look and, in the word layout, its place: clear of the Instagram zones, of the hook
// and the screen notes (the hook's band of hookLines lines) and of the exercise titles.
func validateWordBox(s *Style) []error {
	w := &s.WordBox
	errs := validateText("word_box", &w.TextStyle)

	if w.Lowercase && w.Uppercase {
		errs = append(errs, errors.New("word_box.lowercase and word_box.uppercase exclude each other"))
	}

	if err := validateColor("word_box.box", w.Box); err != nil {
		errs = append(errs, err)
	} else if opacity(w.Box) < minTextOpacity {
		errs = append(errs, errors.New("word_box.box must be at least half opaque (AA >= 80)"))
	}

	if w.Padding < 0 || w.Padding > maxBorder {
		errs = append(errs, fmt.Errorf("word_box.padding must be 0..%d", maxBorder))
	}

	if w.MinDuration < minWordDuration || w.MinDuration > maxWordDuration {
		errs = append(errs, fmt.Errorf("word_box.min_duration must be %.2f..%.0f s", minWordDuration, maxWordDuration))
	}

	if w.CharWidth < minCharWidth || w.CharWidth > maxCharWidth {
		errs = append(errs, fmt.Errorf("word_box.char_width must be %.1f..%.1f", minCharWidth, maxCharWidth))
	}

	if w.MaxWidth < minWordBoxWidth || w.MaxWidth > FrameWidth {
		errs = append(errs, fmt.Errorf("word_box.max_width must be %d..%d", minWordBoxWidth, FrameWidth))
	}

	if len(errs) > 0 || !s.wordMode() {
		return errs
	}

	return validateWordPlace(s)
}

// validateWordPlace checks that the plate stays clear of the Instagram zones, the hook and the notes, and the titles.
func validateWordPlace(s *Style) []error {
	var errs []error

	box := s.wordBoxRect()
	if !box.insideZones() {
		errs = append(errs, fmt.Errorf("word_box (x %d..%d, y %d..%d px) must stay clear of the Instagram zones "+
			"(%d px on the left, %d px on the right, y %d..%d px)", box.left, box.right, box.top, box.bottom,
			SafeZoneLeft, SafeZoneRight, SafeZoneTop, FrameHeight-SafeZoneBottom))
	}

	if hookTop, hookBottom := s.hookSpan(); box.overlaps(band(hookTop, hookBottom)) {
		errs = append(errs, fmt.Errorf("word_box (y %d..%d px) overlaps the hook and the screen notes (y %d..%d px): "+
			"move word_box.y or reduce hook.size", box.top, box.bottom, hookTop, hookBottom))
	}

	if titleTop, titleBottom := s.titleSpan(); box.overlaps(band(titleTop, titleBottom)) {
		errs = append(errs, fmt.Errorf("word_box (y %d..%d px) overlaps the exercise titles (y %d..%d px)", box.top,
			box.bottom, titleTop, titleBottom))
	}

	return errs
}

// wordCue is a word (a number with its unit together) shown from Start to End; Pause: a pause or the end of a sentence
// follows it.
type wordCue struct {
	Text  string
	Start float64
	End   float64
	Pause bool
}

// wordCues are the words of the transcript as the word layout shows them, timed: a number with the word after it is
// one cue («10 повторений»), as the accents of the phrase layout find it; a word shorter than min_duration is shown
// together with the next one.
func wordCues(result *transcription.Result, style *Style) []wordCue {
	tokens := markAccents(tokensFromTranscript(result), nil, nil)
	words := mergeShortWords(groupWords(tokens, &style.WordBox), style.WordBox.MinDuration)

	return timeWords(words, style.WordBox.MinDuration)
}

// groupWords makes a cue of every word, of a number with its unit together.
func groupWords(tokens []token, w *WordBoxStyle) []wordCue {
	out := make([]wordCue, 0, len(tokens))

	for i, t := range tokens {
		text := wordText(t.Text, w)
		pause := t.Break == strongBreak || (i+1 < len(tokens) && gapBreaks(t, tokens[i+1]))

		if len(out) > 0 && t.Accent == numberAccent && tokens[i-1].Accent == numberAccent &&
			!endsPhrase(tokens[i-1], t) {
			last := out[len(out)-1]
			out[len(out)-1] = wordCue{Text: last.Text + " " + text, Start: last.Start, End: t.End, Pause: pause}

			continue
		}

		out = append(out, wordCue{Text: text, Start: t.Start, End: t.End, Pause: pause})
	}

	return out
}

// wordText is a word as the plate shows it: without the punctuation of its sentence and quotes, in small letters or
// capitals when the style says so; a word of punctuation only stays as it is.
func wordText(text string, w *WordBoxStyle) string {
	trimmed := strings.Trim(strings.TrimRight(text, wordTrailing), wordQuotes)
	if strings.TrimSpace(trimmed) == "" {
		trimmed = text
	}

	switch {
	case w.Lowercase:
		return strings.ToLower(trimmed)
	case w.Uppercase:
		return strings.ToUpper(trimmed)
	default:
		return trimmed
	}
}

// mergeShortWords shows a word that the next one follows within minDuration together with it, unless a pause is
// between them: a plate that flashes for a few frames is not read.
func mergeShortWords(words []wordCue, minDuration float64) []wordCue {
	out := make([]wordCue, 0, len(words))

	for i := 0; i < len(words); i++ {
		w := words[i]

		for !w.Pause && i+1 < len(words) && words[i+1].Start-w.Start < minDuration {
			next := words[i+1]
			w = wordCue{Text: w.Text + " " + next.Text, Start: w.Start, End: next.End, Pause: next.Pause}
			i++
		}

		out = append(out, w)
	}

	return out
}

// timeWords sets display times: a word appears leadIn before it is said and stays until the next one (no blinking
// between the words of a phrase); before a pause it stays hold after it is said, at least minDuration and at most
// maxWordShown. Times are rounded to ASS centiseconds and never overlap.
func timeWords(words []wordCue, minDuration float64) []wordCue {
	starts := make([]float64, len(words))

	for i, w := range words {
		starts[i] = roundCS(max(w.Start-leadIn, 0))
		if i > 0 {
			starts[i] = roundCS(max(starts[i], starts[i-1]+minShown))
		}
	}

	timed := make([]wordCue, 0, len(words))

	for i, w := range words {
		start := starts[i]
		end := max(min(w.End+hold, start+maxWordShown), start+minDuration)

		if i+1 < len(words) {
			next := starts[i+1]
			if !w.Pause || next-end < flickerGap {
				end = next
			}

			end = min(end, next)
		}

		timed = append(timed, wordCue{Text: w.Text, Start: start, End: roundCS(max(end, start+minShown)), Pause: w.Pause})
	}

	return timed
}

// wordEvents are the dialogue lines of the words: the Word style, one line each (\q2: never wrapped), a word wider
// than the plate squeezed. The text is plain: it is escaped like speech.
func wordEvents(words []wordCue, style *Style) string {
	var b strings.Builder

	maxChars := style.wordLineChars()

	for _, w := range words {
		tags := `\q2`
		if scale := charsScale(utf8.RuneCountInString(w.Text), maxChars); scale != fullScaleX {
			tags += `\fscx` + strconv.Itoa(scale)
		}

		fmt.Fprintf(&b, "Dialogue: 0,%s,%s,%s,,0,0,0,,{%s}%s\n", formatTime(w.Start), formatTime(w.End), wordStyleName,
			tags, escapeText(w.Text))
	}

	return b.String()
}

// wordStyleLine is the style of the word layout: the text on an opaque plate of the box color (BorderStyle 3 fills
// the outline, Padding wide, with OutlineColour), placed by its top left corner inside the plate.
func wordStyleLine(style *Style) string {
	w := &style.WordBox
	padding := int(math.Round(w.Padding))
	spec := styleSpec{
		name: wordStyleName, text: &w.TextStyle, outline: w.Box, back: w.Box, borderStyle: boxBorderStyle,
		outlineSize: w.Padding, alignment: topLeftAlignment, marginL: w.X + padding,
		marginR: max(FrameWidth-w.X-w.MaxWidth+padding, 0), marginV: w.Y + padding,
	}

	return spec.line()
}
