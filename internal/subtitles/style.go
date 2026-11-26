package subtitles

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

// Reels frame and the Instagram interface over it, in pixels of the 1080×1920 frame. Text placed inside these
// zones is covered: the top bar ("Reels", camera), the caption, audio line and author at the bottom,
// and the like/comment/share buttons on the right.
const (
	FrameWidth  = 1080
	FrameHeight = 1920

	SafeZoneTop    = 250
	SafeZoneBottom = 420
	SafeZoneRight  = 140
	SafeZoneLeft   = 60
)

// Position is where the subtitles stand in the frame.
type Position string

const (
	TopPosition    Position = "top"
	MiddlePosition Position = "middle"
	BottomPosition Position = "bottom"
)

// LayoutMode is how the speech is shown.
type LayoutMode string

const (
	// WordMode shows one word at a time (a number with its unit together) on a small plate at a fixed place, as the
	// reels the trainer chose as references do.
	WordMode LayoutMode = "word"
	// PhraseMode shows phrases of a few words in lines, with key words accented and a vignette behind them.
	PhraseMode LayoutMode = "phrase"
)

// Limits of style values: they catch typos in a brand book file (size 800, outline 50), not taste.
const (
	minFontSize     = 20
	maxFontSize     = 200
	maxBorder       = 20
	maxLinesLimit   = 3
	minCharWidth    = 0.2
	maxCharWidth    = 1.0
	maxAccentsLimit = 5
	maxSideMargin   = FrameWidth/2 - 100 // leaves at least 200 px for the text
	maxVertMargin   = FrameHeight / 2
	minTextOpacity  = 0x80 // text and accents must stay readable
	// lineHeightFactor estimates a line's height from the font size (ascent, descent and gap).
	lineHeightFactor = 1.25
	minFade          = 50
	maxFade          = 1000

	// titleLines: a title is at most this many lines (its text is short: editplan limits it).
	titleLines = 2

	rgbHexLen  = 6
	rgbaHexLen = 8
	hexBase    = 16
	byteBits   = 8
	opaque     = 0xFF
)

var (
	ErrInvalidStyle = errors.New("invalid subtitle style")

	hexColorRe = regexp.MustCompile(`^#([0-9A-Fa-f]{6}|[0-9A-Fa-f]{8})$`)
)

// Style is the look of the subtitles: the brand book of the trainer goes here. It is read from YAML
// (assets/subtitles/style.yaml is the default with comments); fields missing in a file keep the defaults.
type Style struct {
	Text    TextStyle   `yaml:"text"`
	Accent  AccentStyle `yaml:"accent"`
	Outline Border      `yaml:"outline"`
	Shadow  Border      `yaml:"shadow"`
	// KaraokeColor highlights the word being spoken when karaoke is on; its AA part is the opacity.
	KaraokeColor string `yaml:"karaoke_color"`
	// Vignette darkens the frame softly behind the subtitles, for readability without an outline.
	Vignette Vignette `yaml:"vignette"`
	Layout   Layout   `yaml:"layout"`
	// Title is the look of the exercise titles of a workout («Выпады — 12 раз»), apart from the speech subtitles.
	Title TitleStyle `yaml:"title"`
	// Hook is the look of the hook title over the first seconds of the reel; the screen notes of a talking head (a
	// list, its items, short callouts) are drawn in it too, at its place after it.
	Hook HookStyle `yaml:"hook"`
	// WordBox is the look and the place of a word in the word layout (layout.mode word).
	WordBox WordBoxStyle `yaml:"word_box"`
	// Handle is the trainer's Instagram handle over the whole reel; EndCard the card with it over the last seconds.
	Handle  HandleStyle  `yaml:"handle"`
	EndCard EndCardStyle `yaml:"end_card"`
	// Section is the look of the section headings of a talking head («СТРАХ 1»): one at a time above the subtitles.
	Section SectionStyle `yaml:"section"`
	// Topbar is the small line at the top over the whole reel («tg: @ana_prokudina» on the left, «2x» on the right).
	Topbar TopbarStyle `yaml:"topbar"`
}

// TitleStyle is the look and the place of the exercise titles. They stand at the edge opposite the subtitles, clear
// of them and of the Instagram interface; a title is at most titleLines lines (libass wraps it within the side
// margins of the subtitles).
type TitleStyle struct {
	TextStyle `yaml:",inline"`

	// Position empty: the edge opposite the subtitles (bottom for subtitles at the top or in the middle).
	Position Position `yaml:"position"`
	// Margin from the frame edge of the position (top or bottom; unused in the middle).
	Margin int `yaml:"margin"`
	// Box is the color of the box under the title, "#RRGGBBAA"; empty: no box, the outline and shadow of the
	// subtitles. BoxPadding is the box around the text, in pixels.
	Box        string  `yaml:"box"`
	BoxPadding float64 `yaml:"box_padding"`
}

// Vignette darkens the frame from the edge where the subtitles stand (top or bottom; none for middle): evenly
// with Opacity from the edge to the bottom of the subtitle block (margin + block height), then fading out over
// Fade pixels.
type Vignette struct {
	Enabled bool    `yaml:"enabled"`
	Opacity float64 `yaml:"opacity"`
	Fade    int     `yaml:"fade"`
}

// TextStyle is a font with its look. Font is the family name as the font declares it (fc-scan shows it);
// its file must be in SUBTITLES_FONTS_DIR.
type TextStyle struct {
	Font      string `yaml:"font"`
	Size      int    `yaml:"size"`
	Bold      bool   `yaml:"bold"`
	Uppercase bool   `yaml:"uppercase"`
	Color     string `yaml:"color"` // #RRGGBB or #RRGGBBAA (AA is opacity)
}

// AccentStyle is the look of key words and how they are chosen.
type AccentStyle struct {
	TextStyle `yaml:",inline"`

	// Words is the trainer's dictionary: words or phrases always accented. An entry matches a word that starts
	// with it and has at most 3 more letters: "ягодиц" matches "ягодицы" and "ягодицами", "пресс" does not match "прессинговать".
	Words []string `yaml:"words"`
	// MaxPerCue limits automatic accents in one subtitle; an explicit list of accents is not limited.
	MaxPerCue int `yaml:"max_per_cue"`
	// LongestWord accents the longest meaningful word of a subtitle that has no number or dictionary word.
	LongestWord bool `yaml:"longest_word"`
}

// Border is an outline (Size is its width) or a shadow (Size is its offset), in pixels.
type Border struct {
	Color string  `yaml:"color"`
	Size  float64 `yaml:"size"`
}

// Layout places the subtitles and limits their length. The word layout (Mode word) has a place of its own (WordBox):
// the rest of Layout, the accents and the vignette are for the phrase layout.
type Layout struct {
	Mode     LayoutMode `yaml:"mode"`
	Position Position   `yaml:"position"`
	// Distance from the frame edge to the text for top and bottom positions; at least the safe zone.
	MarginTop    int `yaml:"margin_top"`
	MarginBottom int `yaml:"margin_bottom"`
	// MarginSide is the same on the left and right, so the text stays centered and clear of the buttons.
	MarginSide int `yaml:"margin_side"`
	MaxLines   int `yaml:"max_lines"`
	// MaxLineChars limits a line; 0 means as many as fit: the width between margins divided by the font size
	// and CharWidth, the average width of a character in font sizes (about 0.5 for Manrope). A larger value
	// than fits is an error. A subtitle has at most MaxLines lines plus a line for its accent.
	MaxLineChars int     `yaml:"max_line_chars"`
	CharWidth    float64 `yaml:"char_width"`
	// TrimEndPunctuation drops a period, comma, colon or semicolon at the end of a subtitle.
	TrimEndPunctuation bool `yaml:"trim_end_punctuation"`
	// LowercaseSentences writes the first word of a sentence with a small letter, as the trainer does in her reels;
	// names inside a sentence keep their capital («в Москве»).
	LowercaseSentences bool `yaml:"lowercase_sentences"`
}

// The accent font and color of the default style: the key words, the exercise titles and the hook's key word.
const (
	defaultAccentFont  = "Fira Sans Extra Condensed ExtraBold"
	defaultAccentColor = "#F2B94B"
	// defaultFont is the font of the speech, the word plate, the handle and the card; white is their light color.
	defaultFont = "Manrope Medium"
	// regularFont is Manrope Regular (Manrope-Regular.ttf): the hook of the phrase layout and the trainer's speech.
	regularFont = "Manrope"
	white       = "#FFFFFF"
	// softShadow is the shadow of the phrase layout (40 % black); strongShadow keeps white text without a vignette
	// readable on a light background (75 % black).
	softShadow   = "#00000066"
	strongShadow = "#000000BF"
	// wordHookShadow: the hook and the notes of the word layout have no vignette under them.
	wordHookShadow = 3
	// peach is the color of the trainer's hook and section headings.
	peach = "#E8A080"
	// topbarGray is the translucent light gray of the top line (55 % white).
	topbarGray = "#FFFFFF8C"
)

// DefaultStyle is the trainer's own look, measured on the reel she edited and published herself (T3.20): the speech in
// phrases of one or two lines, small white Manrope Regular in small letters, centered at the top under the top line,
// without accents, plates or a vignette (a light shadow only); the hook in large peach Fira Sans Extra Condensed
// ExtraBold capitals that appears as an object selected in an editor (a thin frame with handles and a cursor growing
// to its size) and stays until the first section; the section headings («СТРАХ 1») in the same peach capitals above
// the subtitles, one at a time; the top line «tg: @ana_prokudina … 2x» in small translucent gray. No Instagram handle
// with its icon and no end card. The looks of before are WordsStyle (the reference reels, style-words.yaml) and
// PhraseStyle (style-phrase.yaml).
//
// Sizes are ASS font sizes, the height of a line (for Manrope it is 1.37 of the em, for Fira 1.2): 60 is the 44 px
// letters of her subtitles, 122 the 102 px ones of her hook. While the hook is shown the speech is not (her hook stands
// alone), and the phrase that reads a section heading out («страх номер один») is not shown under it.
func DefaultStyle() Style {
	style := PhraseStyle()
	style.Text = TextStyle{Font: regularFont, Size: 60, Color: white}
	style.Accent.MaxPerCue, style.Accent.LongestWord = 0, false
	style.Shadow = Border{Color: softShadow, Size: 2}
	style.Vignette.Enabled = false
	style.Layout = Layout{
		Mode:               PhraseMode,
		Position:           TopPosition,
		MarginTop:          455,
		MarginBottom:       440,
		MarginSide:         SafeZoneRight,
		MaxLines:           2,
		MaxLineChars:       30,
		CharWidth:          0.41,
		TrimEndPunctuation: true,
		LowercaseSentences: true,
	}
	style.Hook = HookStyle{
		TextStyle:     TextStyle{Font: defaultAccentFont, Size: 122, Uppercase: true, Color: peach},
		MinDuration:   1.8,
		MaxDuration:   2.5,
		Fade:          150,
		Gap:           24,
		CharWidth:     0.41,
		BoxPadding:    12,
		Shadow:        Border{Color: softShadow, Size: 2},
		Margin:        410,
		MarginSide:    60,
		MaxLines:      3,
		Until:         SectionHookUntil,
		Animation:     SelectHookAnimation,
		HideSubtitles: true,
	}
	style.Handle.Enabled = false
	style.Section = SectionStyle{
		TextStyle:   TextStyle{Font: defaultAccentFont, Size: 78, Uppercase: true, Color: peach},
		Enabled:     true,
		Margin:      372,
		MinDuration: 2.5,
		MaxDuration: 6,
		Fade:        150,
	}
	style.Topbar = defaultTopbar()
	style.Topbar.Enabled = true

	return style
}

// WordsStyle is the look of the reference reels the owner chose (T3.17): the speech one word at a time, small black
// Manrope Medium on a white plate at the left edge right under the hook (about a third of the height), no accents
// and no vignette; the hook and the screen notes (the section headings among them, as callouts) in white Manrope at
// the top; the Instagram handle at the right edge. It is assets/subtitles/style-words.yaml.
func WordsStyle() Style {
	style := PhraseStyle()
	style.Layout.Mode = WordMode
	style.Vignette.Enabled = false
	style.Hook.Shadow = Border{Color: strongShadow, Size: wordHookShadow}

	return style
}

// PhraseStyle is the look chosen by the customer before the references: small ivory Manrope Medium at the top of the
// frame below the Instagram bar, key words in warm yellow Fira Sans Extra Condensed ExtraBold capitals, no outline or
// box — a dark vignette at the top (even under the subtitles, then fading) and a light shadow keep the text readable.
//
// Sizes: the 1080 px frame is about 390 pt wide on a phone, so 56 px is about 20 pt — larger than iOS body text
// (17 pt), a third smaller than typical Reels captions; condensed capitals need 64 px to look as large.
func PhraseStyle() Style {
	return Style{
		Text: TextStyle{Font: defaultFont, Size: 56, Color: "#F5F0E1"},
		Accent: AccentStyle{
			TextStyle:   TextStyle{Font: defaultAccentFont, Size: 64, Uppercase: true, Color: defaultAccentColor},
			Words:       []string{},
			MaxPerCue:   1,
			LongestWord: true,
		},
		Outline:      Border{Color: "#000000", Size: 0},
		Shadow:       Border{Color: softShadow, Size: 2},
		KaraokeColor: "#FF8C5A",
		Vignette:     Vignette{Enabled: true, Opacity: 0.6, Fade: 320},
		Layout: Layout{
			Mode:               PhraseMode,
			Position:           TopPosition,
			MarginTop:          260,
			MarginBottom:       440,
			MarginSide:         SafeZoneRight,
			MaxLines:           2,
			MaxLineChars:       22,
			CharWidth:          0.5,
			TrimEndPunctuation: true,
		},
		Title: TitleStyle{
			TextStyle:  TextStyle{Font: defaultAccentFont, Size: 72, Uppercase: true, Color: defaultAccentColor},
			Margin:     460,
			Box:        "#000000B3",
			BoxPadding: 14,
		},
		Hook: HookStyle{
			TextStyle:   TextStyle{Font: regularFont, Size: 60, Color: white},
			MinDuration: 2.5,
			MaxDuration: 8,
			Fade:        150,
			Gap:         20,
			CharWidth:   0.5,
			BoxPadding:  12,
			Shadow:      Border{Color: softShadow, Size: 2},
			Until:       PhraseHookUntil,
			Animation:   NoHookAnimation,
		},
		WordBox: defaultWordBox(),
		Handle:  defaultHandle(),
		EndCard: defaultEndCard(),
		Section: defaultSection(),
		Topbar:  defaultTopbar(),
	}
}

// LoadStyle reads a YAML style file over the defaults.
func LoadStyle(path string) (Style, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return Style{}, fmt.Errorf("failed to read style: %w", err)
	}

	style, err := ParseStyle(data)
	if err != nil {
		return Style{}, fmt.Errorf("failed to load style %s: %w", path, err)
	}

	return style, nil
}

// StyleBase names the built-in style a style file starts from (its field base).
type StyleBase string

const (
	// TrainerBase is DefaultStyle, the trainer's own look (assets/subtitles/style.yaml).
	TrainerBase StyleBase = "trainer"
	// WordsBase is WordsStyle, the reference reels of T3.17 (style-words.yaml).
	WordsBase StyleBase = "words"
	// PhraseBase is PhraseStyle (style-phrase.yaml); a file without base starts from it, so a style file written
	// before T3.20 keeps its look when the built-in default changes.
	PhraseBase StyleBase = "phrase"
)

// styleFile is a style file: the base it starts from and the fields it changes.
type styleFile struct {
	Base  StyleBase `yaml:"base"`
	Style `yaml:",inline"`
}

// ParseStyle decodes YAML strictly (unknown fields are errors) over the built-in style its base names (base: trainer,
// words or phrase; PhraseStyle without it) and validates the result.
func ParseStyle(data []byte) (Style, error) {
	var head struct {
		Base StyleBase `yaml:"base"`
	}

	if err := yaml.Unmarshal(data, &head); err != nil {
		return Style{}, fmt.Errorf("failed to parse style: %w: %w", ErrInvalidStyle, err)
	}

	var base Style

	switch head.Base {
	case TrainerBase:
		base = DefaultStyle()
	case WordsBase:
		base = WordsStyle()
	case PhraseBase, "":
		base = PhraseStyle()
	default:
		return Style{}, fmt.Errorf("base must be trainer, words or phrase, got %q: %w", head.Base, ErrInvalidStyle)
	}

	return parseStyleOver(&base, data)
}

// parseStyleOver decodes YAML over a copy of the base style strictly and validates the result.
func parseStyleOver(base *Style, data []byte) (Style, error) {
	file := styleFile{Style: *base}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	if err := decoder.Decode(&file); err != nil && !errors.Is(err, io.EOF) {
		return Style{}, fmt.Errorf("failed to parse style: %w: %w", ErrInvalidStyle, err)
	}

	style := file.Style
	if err := style.Validate(); err != nil {
		return Style{}, err
	}

	return style, nil
}

// Validate checks values and that the text stays out of the Instagram interface zones.
func (s *Style) Validate() error {
	var errs []error

	errs = append(errs, validateText("text", &s.Text)...)
	errs = append(errs, validateText("accent", &s.Accent.TextStyle)...)
	errs = append(errs, validateLayout(&s.Layout)...)
	errs = append(errs, validateFit(s)...)
	errs = append(errs, validateTitle(s)...)
	errs = append(errs, validateHook(s)...)
	errs = append(errs, validateWordBox(s)...)
	errs = append(errs, validateBrand(s)...)
	errs = append(errs, validateSection(s)...)
	errs = append(errs, validateTopbar(s)...)
	errs = append(errs,
		validateBorder("outline", s.Outline), validateBorder("shadow", s.Shadow),
		validateColor("karaoke_color", s.KaraokeColor), validateVignette(s))

	for i, w := range s.Accent.Words {
		if hasControl(w) {
			errs = append(errs, fmt.Errorf("accent.words[%d] has a control character", i))
		}
	}

	if s.Accent.MaxPerCue < 0 || s.Accent.MaxPerCue > maxAccentsLimit {
		errs = append(errs, fmt.Errorf("accent.max_per_cue must be 0..%d", maxAccentsLimit))
	}

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidStyle, err)
	}

	return nil
}

func validateText(name string, t *TextStyle) []error {
	var errs []error

	// A comma would break the ASS style line; braces and backslashes would be read as override tags.
	if strings.TrimSpace(t.Font) == "" || strings.ContainsAny(t.Font, ",{}\\") || hasControl(t.Font) {
		errs = append(errs, fmt.Errorf("%s.font must be a font family name without commas, braces, backslashes "+
			"or control characters", name))
	}

	if t.Size < minFontSize || t.Size > maxFontSize {
		errs = append(errs, fmt.Errorf("%s.size must be %d..%d", name, minFontSize, maxFontSize))
	}

	if err := validateColor(name+".color", t.Color); err != nil {
		return append(errs, err)
	}

	if opacity(t.Color) < minTextOpacity {
		errs = append(errs, fmt.Errorf("%s.color must be at least half opaque (AA >= 80)", name))
	}

	return errs
}

// validateFit checks that a line fits between the margins and the whole subtitle (MaxLines and an accent line)
// fits between the Instagram zones at its position.
func validateFit(s *Style) []error {
	if s.wordMode() {
		return nil
	}

	var errs []error

	l := &s.Layout
	if fit := s.fittingChars(); l.MaxLineChars > fit {
		errs = append(errs, fmt.Errorf("layout.max_line_chars must be at most %d: longer lines leave the margins", fit))
	}

	height := s.blockHeight()

	var room int

	switch l.Position {
	case TopPosition:
		room = FrameHeight - SafeZoneBottom - l.MarginTop
	case BottomPosition:
		room = FrameHeight - SafeZoneTop - l.MarginBottom
	default:
		// centered at the middle of the frame: both halves must stay out of the zones
		room = 2 * min(FrameHeight/2-SafeZoneTop, FrameHeight-SafeZoneBottom-FrameHeight/2)
	}

	if height > room {
		errs = append(errs, fmt.Errorf("subtitles of %d lines (%d px) do not fit the %d px between the margin "+
			"and the Instagram zones: reduce sizes or layout.max_lines", l.MaxLines+1, height, room))
	}

	return errs
}

func validateVignette(s *Style) error {
	v := s.Vignette
	if v.Fade < minFade || v.Fade > maxFade || v.Opacity < 0 || v.Opacity > 1 {
		return fmt.Errorf("vignette.fade must be %d..%d and vignette.opacity 0..1", minFade, maxFade)
	}

	if plateau, _ := s.vignetteSpan(); plateau+v.Fade > FrameHeight {
		return fmt.Errorf("vignette (%d px under the subtitles and %d px fade) is taller than the frame", plateau, v.Fade)
	}

	return nil
}

// blockHeight estimates the height of the tallest subtitle: MaxLines lines and an accent line.
func (s *Style) blockHeight() int {
	return int(float64((s.Layout.MaxLines+1)*max(s.Text.Size, s.Accent.Size)) * lineHeightFactor)
}

// vignetteSpan returns the even part of the vignette — from the frame edge to the far side of the subtitle
// block — and whether it starts at the bottom edge. In the middle position there is no vignette (0).
func (s *Style) vignetteSpan() (plateau int, bottom bool) {
	switch s.Layout.Position {
	case TopPosition:
		return s.Layout.MarginTop + s.blockHeight(), false
	case BottomPosition:
		return s.Layout.MarginBottom + s.blockHeight(), true
	default:
		return 0, false
	}
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, unicode.IsControl)
}

func validateBorder(name string, b Border) error {
	if b.Size < 0 || b.Size > maxBorder {
		return fmt.Errorf("%s.size must be 0..%d", name, maxBorder)
	}

	return validateColor(name+".color", b.Color)
}

func validateColor(name, color string) error {
	if hasControl(color) || !hexColorRe.MatchString(color) {
		return fmt.Errorf("%s must be #RRGGBB or #RRGGBBAA, got %q", name, color)
	}

	return nil
}

func validateLayout(l *Layout) []error {
	var errs []error

	switch l.Position {
	case TopPosition, MiddlePosition, BottomPosition:
	default:
		errs = append(errs, fmt.Errorf("layout.position must be top, middle or bottom, got %q", l.Position))
	}

	if l.Mode != WordMode && l.Mode != PhraseMode {
		errs = append(errs, fmt.Errorf("layout.mode must be word or phrase, got %q", l.Mode))
	}

	if l.MarginTop < SafeZoneTop || l.MarginTop > maxVertMargin {
		errs = append(errs, fmt.Errorf("layout.margin_top must be %d..%d (Instagram top bar)", SafeZoneTop, maxVertMargin))
	}

	if l.MarginBottom < SafeZoneBottom || l.MarginBottom > maxVertMargin {
		errs = append(errs, fmt.Errorf("layout.margin_bottom must be %d..%d (caption and buttons)", SafeZoneBottom, maxVertMargin))
	}

	if l.MarginSide < max(SafeZoneLeft, SafeZoneRight) || l.MarginSide > maxSideMargin {
		errs = append(errs, fmt.Errorf("layout.margin_side must be %d..%d (buttons on the right)", SafeZoneRight, maxSideMargin))
	}

	if l.MaxLines < 1 || l.MaxLines > maxLinesLimit {
		errs = append(errs, fmt.Errorf("layout.max_lines must be 1..%d", maxLinesLimit))
	}

	if l.MaxLineChars < 0 {
		errs = append(errs, errors.New("layout.max_line_chars must not be negative"))
	}

	if l.CharWidth < minCharWidth || l.CharWidth > maxCharWidth {
		errs = append(errs, fmt.Errorf("layout.char_width must be %.1f..%.1f", minCharWidth, maxCharWidth))
	}

	return errs
}

// lineChars is the line length limit: set in the style or as many as fit between the margins.
func (s *Style) lineChars() int {
	if s.Layout.MaxLineChars > 0 {
		return s.Layout.MaxLineChars
	}

	return s.fittingChars()
}

// fittingChars estimates how many characters fit between the margins.
func (s *Style) fittingChars() int {
	width := float64(FrameWidth - 2*s.Layout.MarginSide)

	return max(int(width/(float64(s.Text.Size)*s.Layout.CharWidth)), 1)
}

// opacity returns the AA part of #RRGGBBAA, or opaque for #RRGGBB. The color is validated beforehand.
func opacity(color string) uint64 {
	hex := strings.TrimPrefix(color, "#")
	if len(hex) != rgbaHexLen {
		return opaque
	}

	alpha, _ := strconv.ParseUint(hex[rgbHexLen:], hexBase, byteBits)

	return alpha
}

// assColor converts #RRGGBB[AA] (AA is opacity) to the ASS &HAABBGGRR form (AA is transparency).
// The color is validated beforehand.
func assColor(color string) string {
	hex := strings.TrimPrefix(color, "#")

	return fmt.Sprintf("&H%02X%s%s%s", opaque-opacity(color), strings.ToUpper(hex[4:6]), strings.ToUpper(hex[2:4]), strings.ToUpper(hex[0:2]))
}

// validateTitle checks the title look and that the title block stays clear of the subtitles and the Instagram zones.
func validateTitle(s *Style) []error {
	t := &s.Title
	errs := validateText("title", &t.TextStyle)

	switch t.Position {
	case "", TopPosition, MiddlePosition, BottomPosition:
	default:
		return append(errs, fmt.Errorf("title.position must be top, middle or bottom, got %q", t.Position))
	}

	if t.Margin < 0 || t.Margin > maxVertMargin {
		errs = append(errs, fmt.Errorf("title.margin must be 0..%d", maxVertMargin))
	}

	if t.BoxPadding < 0 || t.BoxPadding > maxBorder {
		errs = append(errs, fmt.Errorf("title.box_padding must be 0..%d", maxBorder))
	}

	if t.Box != "" {
		if err := validateColor("title.box", t.Box); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return errs
	}

	top, bottom := s.titleSpan()
	if top < SafeZoneTop || bottom > FrameHeight-SafeZoneBottom {
		errs = append(errs, fmt.Errorf("titles (%d..%d px) must stay between the Instagram zones (%d..%d px)",
			top, bottom, SafeZoneTop, FrameHeight-SafeZoneBottom))
	}

	if subTop, subBottom := s.subtitlesSpan(); !s.wordMode() && top < subBottom && subTop < bottom {
		errs = append(errs, fmt.Errorf("titles (%d..%d px) overlap the subtitles (%d..%d px): move title.position "+
			"or title.margin", top, bottom, subTop, subBottom))
	}

	return errs
}

// titleSpan is the vertical extent of a title block (titleLines lines with the box), from the top of the frame.
func (s *Style) titleSpan() (top, bottom int) {
	t := &s.Title
	height := int(float64(titleLines*t.Size)*lineHeightFactor + 2*t.BoxPadding)

	return blockSpan(s.titlePosition(), t.Margin, height)
}

// titlePosition is where the titles stand: as set, or at the edge opposite the subtitles (at the bottom in the word
// layout: the words stand at the top).
func (s *Style) titlePosition() Position {
	switch {
	case s.Title.Position != "":
		return s.Title.Position
	case s.Layout.Position == BottomPosition && !s.wordMode():
		return TopPosition
	default:
		return BottomPosition
	}
}

// subtitlesSpan is the vertical extent of the tallest subtitle.
func (s *Style) subtitlesSpan() (top, bottom int) {
	margin := s.Layout.MarginTop
	if s.Layout.Position == BottomPosition {
		margin = s.Layout.MarginBottom
	}

	return blockSpan(s.Layout.Position, margin, s.blockHeight())
}

// blockSpan places a block of the height at the position with the margin from its edge.
func blockSpan(position Position, margin, height int) (top, bottom int) {
	switch position {
	case TopPosition:
		return margin, margin + height
	case BottomPosition:
		return FrameHeight - margin - height, FrameHeight - margin
	default:
		return FrameHeight/2 - height/2, FrameHeight/2 + height/2
	}
}

// wordMode: the speech is shown one word at a time (layout.mode word).
func (s *Style) wordMode() bool {
	return s.Layout.Mode == WordMode
}
