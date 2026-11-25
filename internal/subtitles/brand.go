package subtitles

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	handleStyleName = "Handle"
	cardStyleName   = "Card"
	// handleLayer draws the handle above the notes; the card darkens everything under it.
	handleLayer    = 3
	cardShadeLayer = 4
	cardLayer      = 5
	// topRightAlignment and centerAlignment are ASS \an9 and \an5.
	topRightAlignment = 9
	centerAlignment   = 5
	// rightButtonsTop: the like, comment and share buttons of Instagram stand at the right edge below about this
	// height; above it the right edge is free for the handle.
	rightButtonsTop = 1000
	// untilTheEnd: the end of the handle of a reel of unknown length (no card): any time past it.
	untilTheEnd = 10 * 60 * 60
	// minCardLead: a reel shorter than the card and this is shown without the card.
	minCardLead = 1.0
	// The icon: the corner of its rounded square, its circle and its dot, in shares of its size; the stroke too.
	iconCorner = 0.28
	iconCircle = 0.23
	iconDotX   = 0.77
	iconDotY   = 0.23
	iconDot    = 0.05
	iconStroke = 0.08
	// bezierCircle: the control points of a quarter circle drawn by a cubic curve, in radii.
	bezierCircle = 0.5523
	// capHeight of the text in font sizes: the icon stands centered on the capitals of the handle.
	capHeight = 0.72
	// Limits of the handle and card values in a style file.
	minIconSize        = 12
	maxIconSize        = 400
	minHandleWidth     = 100
	minCardDuration    = 0.5
	maxCardDuration    = 5.0
	defaultHandleY     = 840
	defaultWordHandleY = 620
)

// handleRe is an Instagram handle as the app accepts it (REELS_HANDLE): «@», then 1–30 small letters, digits, dots and
// underscores. Nothing else reaches the ASS document.
var handleRe = regexp.MustCompile(`^@[a-z0-9._]{1,30}$`)

// ErrInvalidHandle: a handle that is not «@» and 1–30 small Latin letters, digits, dots and underscores.
var ErrInvalidHandle = errors.New("invalid instagram handle")

// CheckHandle checks an Instagram handle («@prokudina.ana»).
func CheckHandle(handle string) error {
	if !handleRe.MatchString(handle) {
		return fmt.Errorf("handle %q must be @ and 1-30 of a-z, 0-9, . and _: %w", handle, ErrInvalidHandle)
	}

	return nil
}

// HandleStyle is the look and the place of the handle over the reel: small translucent text with the icon before it,
// right-aligned Right px from the right edge, its top at Y; a handle wider than MaxWidth (the icon included) is
// squeezed.
type HandleStyle struct {
	TextStyle `yaml:",inline"`

	Right    int `yaml:"right"`
	Y        int `yaml:"y"`
	MaxWidth int `yaml:"max_width"`
	// IconSize of the icon (a rounded square, a circle and a dot, drawn in the text color), px.
	IconSize int `yaml:"icon_size"`
	// CharWidth is the average width of a character in font sizes, for the estimate of the handle's width.
	CharWidth float64 `yaml:"char_width"`
}

// EndCardStyle is the card over the last Duration seconds of the reel: the frame darkens (Shade is the opacity of the
// black, fading in over Fade ms) and the icon and the handle stand large in the middle.
type EndCardStyle struct {
	TextStyle `yaml:",inline"`

	Duration float64 `yaml:"duration"`
	Shade    float64 `yaml:"shade"`
	Fade     int     `yaml:"fade"`
	IconSize int     `yaml:"icon_size"`
}

func defaultHandle() HandleStyle {
	return HandleStyle{
		TextStyle: TextStyle{Font: defaultFont, Size: 30, Color: "#FFFFFFB3"},
		Right:     40,
		Y:         defaultHandleY,
		MaxWidth:  300,
		IconSize:  34,
		CharWidth: 0.55,
	}
}

func defaultEndCard() EndCardStyle {
	return EndCardStyle{
		TextStyle: TextStyle{Font: defaultFont, Size: 64, Color: white},
		Duration:  2,
		Shade:     0.7,
		Fade:      500,
		IconSize:  120,
	}
}

// handleRect is the place of the widest handle with its icon.
func (s *Style) handleRect() rect {
	h := &s.Handle
	height := max(int(math.Ceil(float64(h.Size)*lineHeightFactor)), h.IconSize)

	return rect{left: FrameWidth - h.Right - h.MaxWidth, top: h.Y, right: FrameWidth - h.Right, bottom: h.Y + height}
}

// validateBrand checks the handle and the card: the values, and that the handle stays clear of the Instagram zones
// and the buttons, the hook and the notes, the exercise titles and the speech.
func validateBrand(s *Style) []error {
	errs := validateText("handle", &s.Handle.TextStyle)
	errs = append(errs, validateText("end_card", &s.EndCard.TextStyle)...)
	errs = append(errs, validateBrandValues(&s.Handle, &s.EndCard)...)

	if len(errs) > 0 {
		return errs
	}

	return validateHandlePlace(s)
}

// validateBrandValues checks the sizes of the handle and the times of the card.
func validateBrandValues(h *HandleStyle, c *EndCardStyle) []error {
	var errs []error

	if h.Right < 0 || h.Right > maxSideMargin || h.MaxWidth < minHandleWidth || h.MaxWidth > FrameWidth/2 {
		errs = append(errs, fmt.Errorf("handle.right must be 0..%d and handle.max_width %d..%d", maxSideMargin,
			minHandleWidth, FrameWidth/2))
	}

	if h.IconSize < minIconSize || h.IconSize > maxIconSize || c.IconSize < minIconSize || c.IconSize > maxIconSize {
		errs = append(errs, fmt.Errorf("handle.icon_size and end_card.icon_size must be %d..%d", minIconSize, maxIconSize))
	}

	if h.CharWidth < minCharWidth || h.CharWidth > maxCharWidth {
		errs = append(errs, fmt.Errorf("handle.char_width must be %.1f..%.1f", minCharWidth, maxCharWidth))
	}

	if err := validateCardTimes(c); err != nil {
		errs = append(errs, err)
	}

	return errs
}

// validateCardTimes checks the duration, the shade and the fade of the card.
func validateCardTimes(c *EndCardStyle) error {
	if c.Duration < minCardDuration || c.Duration > maxCardDuration || c.Shade < 0 || c.Shade > 1 || c.Fade < 0 ||
		float64(c.Fade) >= c.Duration*millisPerSecond {
		return fmt.Errorf("end_card.duration must be %.1f..%.0f s, end_card.shade 0..1 and end_card.fade 0 ms..the "+
			"duration", minCardDuration, maxCardDuration)
	}

	return nil
}

// validateHandlePlace checks the place of the handle against the zones and what else stands in the frame.
func validateHandlePlace(s *Style) []error {
	var errs []error

	handle := s.handleRect()
	if handle.left < SafeZoneLeft || handle.top < SafeZoneTop || handle.bottom > rightButtonsTop {
		errs = append(errs, fmt.Errorf("handle (x %d..%d, y %d..%d px) must stay below the Instagram top bar (%d px) "+
			"and above the buttons on the right (%d px)", handle.left, handle.right, handle.top, handle.bottom,
			SafeZoneTop, rightButtonsTop))
	}

	hookTop, hookBottom := s.hookSpan()
	titleTop, titleBottom := s.titleSpan()
	others := map[string]rect{
		"the hook and the screen notes": band(hookTop, hookBottom),
		"the exercise titles":           band(titleTop, titleBottom),
	}

	if s.wordMode() {
		others["the words"] = s.wordBoxRect()
	} else {
		names := []string{"the subtitles", "the subtitles under the hook"}
		for i, span := range s.phraseSpans() {
			others[names[i]] = rect{
				left: s.Layout.MarginSide, top: span[0], right: FrameWidth - s.Layout.MarginSide, bottom: span[1],
			}
		}
	}

	for _, name := range sortedKeys(others) {
		if other := others[name]; handle.overlaps(other) {
			errs = append(errs, fmt.Errorf("handle (x %d..%d, y %d..%d px) overlaps %s (x %d..%d, y %d..%d px): move "+
				"handle.y", handle.left, handle.right, handle.top, handle.bottom, name, other.left, other.right,
				other.top, other.bottom))
		}
	}

	return errs
}

// phraseSpans are the places of the subtitles of the phrase layout: where they stand, and under the hook (when they
// move under it).
func (s *Style) phraseSpans() [][2]int {
	top, bottom := s.subtitlesSpan()
	spans := [][2]int{{top, bottom}}

	if margin := s.hookSubtitlesMargin(hookLines); margin > 0 {
		shiftedTop, shiftedBottom := blockSpan(s.Layout.Position, margin, s.blockHeight())
		spans = append(spans, [2]int{shiftedTop, shiftedBottom})
	}

	return spans
}

// sortedKeys are the keys of the map in order, so the errors come in the same order every time.
func sortedKeys(m map[string]rect) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	return keys
}

// brandEvents are the handle over the reel and the end card over its last seconds; "" without a valid handle. A reel
// of unknown length (reelEnd 0) or shorter than the card and minCardLead gets no card. The handle fades out as the card
// fades in.
func brandEvents(branding *Branding, reelEnd float64, style *Style) string {
	if branding == nil || CheckHandle(branding.Handle) != nil {
		return ""
	}

	c := &style.EndCard
	end := float64(untilTheEnd)

	if reelEnd > 0 {
		end = reelEnd
	}

	if !branding.EndCard || reelEnd < c.Duration+minCardLead {
		return handleEvent(branding, end, 0, style)
	}

	start := roundCS(reelEnd - c.Duration)

	return handleEvent(branding, start, c.Fade, style) + cardEvents(branding, start, reelEnd, style)
}

// handleEvent is the handle from the start of the reel to end, fading out over fadeOut ms: the icon (when on) and the
// text, squeezed to the style's width.
func handleEvent(branding *Branding, end float64, fadeOut int, style *Style) string {
	h := &style.Handle
	width := float64(h.MaxWidth)

	if branding.Icon {
		width -= float64(h.IconSize)
	}

	fit := max(int(width/(float64(h.Size)*h.CharWidth)), 1)
	text := handleText(branding, charsScale(utf8.RuneCountInString(branding.Handle), fit), " ",
		func() string { return iconDrawing(h.IconSize, h.Color, h.Size) })

	return fmt.Sprintf("Dialogue: %d,%s,%s,%s,,0,0,0,,{\\q2%s}%s\n", handleLayer, formatTime(0),
		formatTime(end), handleStyleName, fadTag(0, fadeOut), text)
}

// handleText is the escaped handle squeezed to scale, after the icon (when on) and the separator: {\r} after the icon
// resets the wrapping and the scale, so they are set again.
func handleText(branding *Branding, scale int, separator string, icon func() string) string {
	text := escapeText(branding.Handle)
	if tag := scaleTag(scale); tag != "" {
		text = "{" + tag + "}" + text
	}

	if !branding.Icon {
		return text
	}

	return icon() + separator + `{\q2}` + text
}

// cardEvents darken the frame from start to end and put the icon and the handle large in the middle, fading in.
func cardEvents(branding *Branding, start, end float64, style *Style) string {
	c := &style.EndCard
	alpha := opaque - int(math.Round(c.Shade*opaque))
	shade := fmt.Sprintf("Dialogue: %d,%s,%s,%s,,0,0,0,,{\\an7\\pos(0,0)\\fad(%d,0)\\1c&H000000&\\1a&H%02X&\\bord0"+
		"\\shad0\\p1}m 0 0 l %d 0 l %d %d l 0 %d{\\p0}\n", cardShadeLayer, formatTime(start), formatTime(end),
		cardStyleName, c.Fade, alpha, FrameWidth, FrameWidth, FrameHeight, FrameHeight)

	fit := max(int(float64(FrameWidth-2*SafeZoneRight)/(float64(c.Size)*style.Handle.CharWidth)), 1)
	text := handleText(branding, charsScale(utf8.RuneCountInString(branding.Handle), fit), assLineBreak,
		func() string { return iconDrawing(c.IconSize, c.Color, c.Size) })

	return shade + fmt.Sprintf("Dialogue: %d,%s,%s,%s,,0,0,0,,{\\an5\\pos(%d,%d)\\fad(%d,0)\\q2}%s\n", cardLayer,
		formatTime(start), formatTime(end), cardStyleName, FrameWidth/2, FrameHeight/2, c.Fade, text)
}

// scaleTag is \fscx of the scale, "" for the full width.
func scaleTag(scale int) string {
	if scale == fullScaleX {
		return ""
	}

	return `\fscx` + strconv.Itoa(scale)
}

// iconDrawing is the Instagram icon of the size as an inline ASS drawing filled with the color: the ring of a rounded
// square, the ring of a circle in it and a dot (a ring is an outer contour and an inner one drawn the other way, so
// the fill leaves a hole: libass cuts the fill out of a border, so the rings cannot be drawn as borders). It is
// lowered to stand centered on the capitals of a text of textSize; the style's look comes back after it ({\r}).
func iconDrawing(size int, color string, textSize int) string {
	lower := int(math.Round((float64(size) - capHeight*float64(textSize)) / 2))

	return fmt.Sprintf(`{\1c%s\1a%s\bord0\shad0\pbo%d\p1}%s{\p0\r}`, assOverrideColor(color), assOverrideAlpha(color),
		lower, iconPath(float64(size)))
}

// iconPath is the icon in a square of the size from (0, 0).
func iconPath(size float64) string {
	stroke := max(size*iconStroke, 1)
	corner := size * iconCorner
	circle := size * iconCircle
	center := point{x: size / 2, y: size / 2}

	return strings.Join([]string{
		contour(roundedSquare(0, size, corner), false),
		contour(roundedSquare(stroke, size-2*stroke, corner-stroke), true),
		contour(circlePath(center, circle+stroke/2), false),
		contour(circlePath(center, circle-stroke/2), true),
		contour(circlePath(point{x: size * iconDotX, y: size * iconDotY}, size*iconDot), false),
	}, " ")
}

// point is a point of a drawing.
type point struct {
	x, y float64
}

// segment of a contour: a line to the point to, or a cubic curve through the control points c1 and c2.
type segment struct {
	c1, c2, to point
	curve      bool
}

// closedPath is a closed contour: from start through the segments back to start.
type closedPath struct {
	start    point
	segments []segment
}

// contour writes the path as ASS drawing commands, clockwise as it is or, reverse, the other way (an inner contour
// drawn the other way cuts a hole in the fill).
func contour(path closedPath, reverse bool) string {
	if reverse {
		path = reversed(path)
	}

	var b strings.Builder

	b.WriteString("m " + points(path.start))

	for _, s := range path.segments {
		if s.curve {
			b.WriteString(" b " + points(s.c1, s.c2, s.to))
		} else {
			b.WriteString(" l " + points(s.to))
		}
	}

	return b.String()
}

// reversed is the same contour drawn the other way.
func reversed(path closedPath) closedPath {
	out := make([]segment, 0, len(path.segments))

	for i := len(path.segments) - 1; i >= 0; i-- {
		from := path.start
		if i > 0 {
			from = path.segments[i-1].to
		}

		s := path.segments[i]
		out = append(out, segment{c1: s.c2, c2: s.c1, to: from, curve: s.curve})
	}

	return closedPath{start: path.start, segments: out}
}

// roundedSquare is a square of the size from (at, at) with corners of the radius, clockwise.
func roundedSquare(at, size, radius float64) closedPath {
	k := radius * bezierCircle
	near, far, end := at+radius, at+size-radius, at+size
	corner := func(c1, c2, to point) segment { return segment{c1: c1, c2: c2, to: to, curve: true} }

	return closedPath{start: point{near, at}, segments: []segment{
		{to: point{far, at}},
		corner(point{far + k, at}, point{end, near - k}, point{end, near}),
		{to: point{end, far}},
		corner(point{end, far + k}, point{far + k, end}, point{far, end}),
		{to: point{near, end}},
		corner(point{near - k, end}, point{at, far + k}, point{at, far}),
		{to: point{at, near}},
		corner(point{at, near - k}, point{near - k, at}, point{near, at}),
	}}
}

// circlePath is a circle of the radius around the center, clockwise.
func circlePath(center point, radius float64) closedPath {
	k := radius * bezierCircle
	x, y := center.x, center.y

	return closedPath{start: point{x, y - radius}, segments: []segment{
		{c1: point{x + k, y - radius}, c2: point{x + radius, y - k}, to: point{x + radius, y}, curve: true},
		{c1: point{x + radius, y + k}, c2: point{x + k, y + radius}, to: point{x, y + radius}, curve: true},
		{c1: point{x - k, y + radius}, c2: point{x - radius, y + k}, to: point{x - radius, y}, curve: true},
		{c1: point{x - radius, y - k}, c2: point{x - k, y - radius}, to: point{x, y - radius}, curve: true},
	}}
}

// tenthsPerUnit rounds the drawing coordinates to tenths of a pixel.
const tenthsPerUnit = 10

// points writes the coordinates of the points rounded to tenths of a pixel.
func points(ps ...point) string {
	out := make([]string, 0, 2*len(ps))
	for _, p := range ps {
		out = append(out, tenth(p.x), tenth(p.y))
	}

	return strings.Join(out, " ")
}

func tenth(v float64) string {
	return formatFloat(math.Round(v*tenthsPerUnit) / tenthsPerUnit)
}

// brandStyleLines are the styles of the handle (top right, Right px from the edge, its top at Y) and of the card
// (centered), with the shadow of the subtitles.
func brandStyleLines(style *Style) string {
	h, c := &style.Handle, &style.EndCard
	handle := styleSpec{
		name: handleStyleName, text: &h.TextStyle, outline: style.Outline.Color, back: style.Shadow.Color,
		borderStyle: outlineBorderStyle, shadow: style.Shadow.Size,
		alignment: topRightAlignment, marginR: h.Right, marginV: h.Y,
	}
	card := styleSpec{
		name: cardStyleName, text: &c.TextStyle, outline: style.Outline.Color, back: style.Shadow.Color,
		borderStyle: outlineBorderStyle, shadow: style.Shadow.Size, alignment: centerAlignment,
	}

	return handle.line() + "\n" + card.line() + "\n"
}

// styleSpec is a style line of the document.
type styleSpec struct {
	name                      string
	text                      *TextStyle
	outline, back             string
	borderStyle               int
	outlineSize, shadow       float64
	alignment                 int
	marginL, marginR, marginV int
}

func (sp *styleSpec) line() string {
	bold := 0
	if sp.text.Bold {
		bold = assBold
	}

	fields := []string{
		sp.name, sp.text.Font, strconv.Itoa(sp.text.Size),
		assColor(sp.text.Color), assColor(sp.text.Color), assColor(sp.outline), assColor(sp.back),
		strconv.Itoa(bold), "0", "0", "0", "100", "100", "0", "0", strconv.Itoa(sp.borderStyle),
		formatFloat(sp.outlineSize), formatFloat(sp.shadow),
		strconv.Itoa(sp.alignment), strconv.Itoa(sp.marginL), strconv.Itoa(sp.marginR), strconv.Itoa(sp.marginV), "1",
	}

	return "Style: " + strings.Join(fields, ",")
}
