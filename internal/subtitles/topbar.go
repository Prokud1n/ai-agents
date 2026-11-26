package subtitles

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Prokud1n/ai-agents/internal/editjob"
)

const (
	topbarStyleName = "Topbar"
	// middleLeftAlignment and middleRightAlignment are ASS \an4 and \an6: the top line stands by its middle.
	middleLeftAlignment  = 4
	middleRightAlignment = 6
)

// TopbarStyle is the look and the place of the top line over the whole reel, as the interface of a camera: a text at
// the left edge (REELS_TOPBAR_LEFT, «tg: @ana_prokudina») and one at the right edge (REELS_TOPBAR_RIGHT, «2x»), small
// translucent gray, their middle at Y, Side px from the edges; a text wider than MaxWidth is squeezed.
type TopbarStyle struct {
	TextStyle `yaml:",inline"`

	Enabled bool `yaml:"enabled"`
	// RightSize is the size of the right text («2x» is larger in her reels); 0: size.
	RightSize int `yaml:"right_size"`
	// Y is the middle of the line, px from the top of the frame.
	Y        int `yaml:"y"`
	Side     int `yaml:"side"`
	MaxWidth int `yaml:"max_width"`
	// CharWidth is the average width of a character in font sizes, for the estimate of a text's width.
	CharWidth float64 `yaml:"char_width"`
	Shadow    Border  `yaml:"shadow"`
}

// defaultTopbar is the top line of the trainer's reel: Manrope Regular, the left text 32 px letters and «2x» 42 px,
// 55 % white, the middle at 286 px, 60 px from the edges. Off outside the trainer's style.
func defaultTopbar() TopbarStyle {
	return TopbarStyle{
		TextStyle: TextStyle{Font: regularFont, Size: 44, Color: topbarGray},
		RightSize: 58,
		Y:         286,
		Side:      SafeZoneLeft,
		MaxWidth:  440,
		CharWidth: 0.42,
		Shadow:    Border{Color: softShadow, Size: 0},
	}
}

// rightSize is the size of the right text.
func (t *TopbarStyle) rightSize() int {
	if t.RightSize > 0 {
		return t.RightSize
	}

	return t.Size
}

// validateTopbar checks the look of the top line and, when it is on, its place.
func validateTopbar(s *Style) []error {
	t := &s.Topbar
	errs := validateText("topbar", &t.TextStyle)

	if t.RightSize != 0 && (t.RightSize < minFontSize || t.RightSize > maxFontSize) {
		errs = append(errs, fmt.Errorf("topbar.right_size must be 0 (topbar.size) or %d..%d", minFontSize, maxFontSize))
	}

	if t.Side < SafeZoneLeft || t.Side > maxSideMargin || t.MaxWidth < minHandleWidth || t.MaxWidth > FrameWidth/2 {
		errs = append(errs, fmt.Errorf("topbar.side must be %d..%d and topbar.max_width %d..%d", SafeZoneLeft,
			maxSideMargin, minHandleWidth, FrameWidth/2))
	}

	if t.CharWidth < minCharWidth || t.CharWidth > maxCharWidth {
		errs = append(errs, fmt.Errorf("topbar.char_width must be %.1f..%.1f", minCharWidth, maxCharWidth))
	}

	if t.Y < SafeZoneTop || t.Y > FrameHeight-SafeZoneBottom {
		errs = append(errs, fmt.Errorf("topbar.y must be %d..%d", SafeZoneTop, FrameHeight-SafeZoneBottom))
	}

	if err := validateBorder("topbar.shadow", t.Shadow); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 || !t.Enabled {
		return errs
	}

	return validateTopbarPlace(s)
}

// validateTopbarPlace: the top line stays below the Instagram bar and above the buttons on the right (it stands closer
// to the edges than the side margins), clear of the hook, the headings, the subtitles or the words, the titles and the
// handle.
func validateTopbarPlace(s *Style) []error {
	var errs []error

	top, bottom := s.topbarSpan()
	if top < SafeZoneTop || bottom > rightButtonsTop {
		errs = append(errs, fmt.Errorf("top line (%d..%d px) must stay below the Instagram top bar (%d px) and above "+
			"the buttons on the right (%d px)", top, bottom, SafeZoneTop, rightButtonsTop))
	}

	others := map[string]rect{
		hookPlaceName:   band(s.hookSpan()),
		titlesPlaceName: band(s.titleSpan()),
	}

	if s.Section.Enabled {
		others["the section headings"] = band(s.sectionSpan())
	}

	if s.Handle.Enabled {
		others["the handle"] = s.handleRect()
	}

	if s.wordMode() {
		others[wordsPlaceName] = s.wordBoxRect()
	} else {
		for i, span := range s.phraseSpans() {
			others[[]string{subtitlesPlaceName, shiftedPlaceName}[i]] = band(span[0], span[1])
		}
	}

	for _, name := range sortedKeys(others) {
		if other := others[name]; band(top, bottom).overlaps(other) {
			errs = append(errs, fmt.Errorf("top line (%d..%d px) overlaps %s (%d..%d px): move topbar.y", top, bottom,
				name, other.top, other.bottom))
		}
	}

	return errs
}

// topbarSpan is the band of the top line: the larger of its sizes with the line spacing, around Y.
func (s *Style) topbarSpan() (top, bottom int) {
	t := &s.Topbar
	height := int(float64(max(t.Size, t.rightSize())) * lineHeightFactor)

	return t.Y - height/2, t.Y + height - height/2
}

// topbarEvents are the texts of the top line from the start of the reel to end, fading out over fadeOut ms: each
// that is set and passes editjob.CheckTopbar, when the style has the line. A text wider than topbar.max_width is
// squeezed; the text is escaped like speech.
func topbarEvents(branding *Branding, end float64, fadeOut int, style *Style) string {
	t := &style.Topbar
	if !t.Enabled {
		return ""
	}

	sides := []struct {
		text            string
		alignment, x    int
		size            int
		sizeTagRequired bool
	}{
		{text: branding.TopbarLeft, alignment: middleLeftAlignment, x: t.Side, size: t.Size},
		{
			text: branding.TopbarRight, alignment: middleRightAlignment, x: FrameWidth - t.Side, size: t.rightSize(),
			sizeTagRequired: t.rightSize() != t.Size,
		},
	}

	var b strings.Builder

	for _, side := range sides {
		if side.text == "" || editjob.CheckTopbar(side.text) != nil {
			continue
		}

		fit := max(int(float64(t.MaxWidth)/(float64(side.size)*t.CharWidth)), 1)
		tags := fmt.Sprintf(`\an%d\pos(%d,%d)\q2`, side.alignment, side.x, t.Y)

		if side.sizeTagRequired {
			tags += `\fs` + strconv.Itoa(side.size)
		}

		tags += fadTag(0, fadeOut) + scaleTag(charsScale(utf8.RuneCountInString(side.text), fit))

		fmt.Fprintf(&b, "Dialogue: %d,%s,%s,%s,,0,0,0,,{%s}%s\n", handleLayer, formatTime(0), formatTime(end),
			topbarStyleName, tags, escapeText(side.text))
	}

	return b.String()
}

// topbarStyleLine is the style of the top line: no outline, its own shadow.
func topbarStyleLine(style *Style) string {
	t := &style.Topbar
	spec := styleSpec{
		name: topbarStyleName, text: &t.TextStyle, outline: style.Outline.Color, back: t.Shadow.Color,
		borderStyle: outlineBorderStyle, shadow: t.Shadow.Size, alignment: middleLeftAlignment,
	}

	return spec.line()
}
