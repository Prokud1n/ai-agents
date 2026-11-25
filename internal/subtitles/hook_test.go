package subtitles

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const (
	hookPlank = "Три ошибки в планке"
	// shiftedUnderHook: a subtitle event under a hook of one line at the top.
	shiftedUnderHook = ",Main,,0,0,355,,"
)

func TestHookEnd(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	long := speech("Сегодня разберём три ошибки, которые все делают в планке каждый день.")
	short := speech("Привет!", "Сегодня разберём три ошибки, которые все делают в планке.")
	endless := speech(strings.Repeat("держим планку ровно и дышим спокойно ", 5))
	tiny := speech("Привет!")

	tests := []struct {
		name     string
		hook     string
		input    *transcription.Result
		expected float64
	}{
		{name: "no hook", hook: " ", input: long, expected: 0},
		{name: "half a second after the first phrase", hook: hookPlank, input: long, expected: 4.35},
		{name: "a short first phrase: min_duration", hook: hookPlank, input: short, expected: 2.5},
		{name: "a long first phrase: max_duration", hook: hookPlank, input: endless, expected: 8},
		{name: "not longer than the reel", hook: hookPlank, input: tiny, expected: 0.85},
		{
			name: "no speech: min_duration", hook: hookPlank,
			input: &transcription.Result{Duration: 10}, expected: 2.5,
		},
		{
			name: "a reel shorter than the hook", hook: hookPlank,
			input: &transcription.Result{Duration: 1.2}, expected: 1.2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.InDelta(t, tt.expected, hookEnd(tt.hook, tt.input, &style), 1e-9)
		})
	}
}

func TestHookEvent(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	accented := style
	accented.Hook.Highlight = defaultAccentColor
	capitals := style
	capitals.Hook.Uppercase = true

	const head = `Dialogue: 2,0:00:00.00,0:00:02.50,Hook,,0,0,0,,{\fad(150,150)}`

	tests := []struct {
		name     string
		hook     string
		end      float64
		style    *Style
		expected string
	}{
		{name: "one line as it is", hook: "Мой ребёнок билингв", end: 2.5, style: &style, expected: head + "Мой ребёнок билингв\n"},
		{
			name: "two even lines", hook: "Видео для тех, кто боится начать тренироваться дома", end: 2.5, style: &style,
			expected: head + `Видео для тех, кто боится\Nначать тренироваться дома` + "\n",
		},
		{
			name: "four lines, no preposition at the end of a line", end: 2.5, style: &style,
			hook: "Как я перестала бояться спортзала и за полгода пробежала первый полумарафон в сорок лет",
			expected: head + `Как я перестала бояться\Nспортзала и за полгода\Nпробежала первый\N` +
				`полумарафон в сорок лет` + "\n",
		},
		{
			name: "cut to 90 characters by words", hook: strings.Repeat("пресс ", 20), end: 2.5, style: &style,
			expected: head + `пресс пресс пресс пресс\Nпресс пресс пресс пресс\Nпресс пресс пресс пресс\N` +
				`пресс пресс пресс` + "\n",
		},
		{
			name: "a number with its word highlighted when the style says so", hook: "Пресс за 10 минут", end: 2.5,
			style: &accented, expected: head + `Пресс за {\1c&H4BB9F2&\1a&H00&}10 минут{\1c&HFFFFFF&\1a&H00&}` + "\n",
		},
		{name: "capitals when the style says so", hook: hookPlank, end: 2.5, style: &capitals, expected: head + "ТРИ ОШИБКИ В ПЛАНКЕ\n"},
		{
			name: "markup is text, the fade fits a short hook", hook: `Планка {\an7} \N`, end: 0.2, style: &style,
			expected: `Dialogue: 2,0:00:00.00,0:00:00.20,Hook,,0,0,0,,{\fad(100,100)}Планка \{an7\} N` + "\n",
		},
		{
			name: "a long word is squeezed", hook: "Суперинтенсивнейшаятренировкапресса", end: 2.5, style: &style,
			expected: head + `{\fscx74}Суперинтенсивнейшаятренировкапресса` + "\n",
		},
		{
			name: "control and invisible characters dropped", hook: "\u200bПланка\nбез\u2066 ошибок", end: 2.5,
			style: &style, expected: head + "Планка без ошибок\n",
		},
		{name: "no hook", hook: "  ", end: 2.5, style: &style, expected: ""},
		{name: "no time", hook: hookPlank, end: 0, style: &style, expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, hookEvent(tt.hook, tt.end, tt.style))
		})
	}
}

func TestHookKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected [2]int
	}{
		{name: "the longest word", input: "ТРИ ОШИБКИ В ПЛАНКЕ", expected: [2]int{1, 2}},
		{name: "a number with its word", input: "ПРЕСС ЗА 10 МИНУТ", expected: [2]int{2, 4}},
		{name: "a number with a unit", input: "МИНУС 5КГ ЗА МЕСЯЦ", expected: [2]int{1, 2}},
		{name: "a number at the end", input: "ПЛАНКА 30", expected: [2]int{1, 2}},
		{name: "short words only", input: "ТАК НЕ НАДО", expected: [2]int{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, hookKey(strings.Fields(tt.input)))
		})
	}
}

// TestHookLayout: the hook and the subtitles shown with it do not overlap each other, the exercise titles or the
// Instagram zones, at the top and at the bottom; a hook too large for the frame fails the style.
func TestHookLayout(t *testing.T) {
	t.Parallel()

	top := DefaultStyle()
	bottom := DefaultStyle()
	bottom.Layout.Position = BottomPosition
	middle := DefaultStyle()
	middle.Layout.Position = MiddlePosition

	tests := []struct {
		name   string
		style  *Style
		hook   [2]int
		shift  int
		shifts bool
	}{
		{name: "at the top", style: &top, hook: [2]int{260, 560}, shift: 580, shifts: true},
		{name: "at the bottom", style: &bottom, hook: [2]int{1180, 1480}, shift: 760, shifts: true},
		{name: "in the middle", style: &middle, hook: [2]int{260, 560}, shift: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.NoError(t, tt.style.Validate())

			hookTop, hookBottom := tt.style.hookSpan()
			assert.Equal(t, tt.hook[0], hookTop)
			assert.Equal(t, tt.hook[1], hookBottom)
			assert.GreaterOrEqual(t, hookTop, SafeZoneTop)
			assert.LessOrEqual(t, hookBottom, FrameHeight-SafeZoneBottom)
			assert.Equal(t, tt.shift, tt.style.hookSubtitlesMargin(hookLines))

			subTop, subBottom := tt.style.subtitlesSpan()
			if tt.shifts {
				subTop, subBottom = blockSpan(tt.style.Layout.Position, tt.shift, tt.style.blockHeight())
			}

			titleTop, titleBottom := tt.style.titleSpan()

			assert.False(t, hookTop < subBottom && subTop < hookBottom, "the hook and the subtitles overlap")
			assert.False(t, hookTop < titleBottom && titleTop < hookBottom, "the hook and the titles overlap")
			assert.False(t, subTop < titleBottom && titleTop < subBottom, "the subtitles and the titles overlap")
			assert.GreaterOrEqual(t, subTop, SafeZoneTop)
			assert.LessOrEqual(t, subBottom, FrameHeight-SafeZoneBottom)
		})
	}
}

func TestValidateHook(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(s *Style)
	}{
		{name: "too large for four lines", change: func(s *Style) { s.Hook.Size = 120 }},
		{name: "bad box", change: func(s *Style) { s.Hook.Box = "orange" }},
		{name: "the subtitles under a boxed hook reach the titles", change: func(s *Style) {
			s.Hook.Box = "#F28C38E6"
			s.Title.Margin = 900
		}},
		{name: "the subtitles under it reach the titles", change: func(s *Style) { s.Hook.Gap = 60; s.Title.Margin = 950 }},
		{name: "bad highlight", change: func(s *Style) { s.Hook.Highlight = "yellow" }},
		{name: "translucent highlight", change: func(s *Style) { s.Hook.Highlight = "#F2B94B40" }},
		{name: "too long", change: func(s *Style) { s.Hook.MaxDuration = 12 }},
		{name: "fade longer than half of min_duration", change: func(s *Style) { s.Hook.MinDuration = 1; s.Hook.Fade = 500 }},
		{name: "min_duration over max_duration", change: func(s *Style) { s.Hook.MinDuration = 9 }},
		{name: "no min_duration", change: func(s *Style) { s.Hook.MinDuration = 0 }},
		{name: "comma in the font", change: func(s *Style) { s.Hook.Font = "Fira, Sans" }},
		{name: "over the titles at the bottom", change: func(s *Style) {
			s.Layout.Position = BottomPosition
			s.Title.Position = BottomPosition
			s.Title.Margin = SafeZoneBottom
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			style := DefaultStyle()
			tt.change(&style)
			require.ErrorIs(t, style.Validate(), ErrInvalidStyle)
		})
	}
}

// TestBuildASSWithHook: the subtitles that start while the hook is shown stand under it for all their time, the
// later ones at their place; the hook is drawn above them in its own style.
func TestBuildASSWithHook(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	result := speech("Сегодня разберём три ошибки.",
		"Все делают их в планке каждый день, держим спину ровно и дышим, а теперь самое главное.")

	ass, cues := buildASS(result, &style, nil, false, nil, hookPlank)
	require.Greater(t, cues, 2)

	assert.Contains(t, ass, "Style: Hook,Manrope,60,&H00FFFFFF,&H00FFFFFF,&H00000000,"+
		"&H99000000,0,0,0,0,100,100,0,0,1,0,2,8,140,140,260,1\n")
	assert.Contains(t, ass, `Hook,,0,0,0,,{\fad(150,150)}Три ошибки в планке`)

	shifted, kept := 0, 0

	for line := range strings.SplitSeq(ass, "\n") {
		if !strings.Contains(line, ",Main,,") {
			continue
		}

		start := strings.Split(line, ",")[1]
		if start < "0:00:02.50" {
			assert.Contains(t, line, shiftedUnderHook, "starts with the hook: under it")
			shifted++

			continue
		}

		assert.Contains(t, line, ",Main,,0,0,0,,", "after the hook: at its place")
		kept++
	}

	assert.Positive(t, shifted)
	assert.Positive(t, kept)

	plain, _ := buildASS(result, &style, nil, false, nil, "")
	assert.NotContains(t, plain, ",Hook,,")
	assert.NotContains(t, plain, shiftedUnderHook)
}

// rowsBright is how many rows of [from, to) have a bright pixel.
func rowsBright(frame []byte, from, to int) int {
	n := 0

	for y := from; y < to; y++ {
		for x := range FrameWidth {
			if frame[y*FrameWidth+x] >= brightLevel {
				n++

				break
			}
		}
	}

	return n
}

// TestBurnHookWithFFmpeg: libass draws the hook in its band over the first seconds with the subtitles under it, and
// nothing of it is left after it ends.
func TestBurnHookWithFFmpeg(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	ctx := t.Context()
	runner := ffmpeg.NewRunner(&config.FFmpeg{Timeout: 2 * time.Minute})
	svc, err := NewService(runner, &config.Subtitles{FontsDir: testFontsDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)

	dir := t.TempDir()
	input, output := filepath.Join(dir, "in.mp4"), filepath.Join(dir, "out.mp4")
	makeClip(ctx, t, input, reelSize, true)

	// Speech until 2.1 s (the reel is 2.6 s): the hook stays until 2.6 s, nothing is on screen at 2.8 s.
	const hook = "Три ошибки в планке за 10 минут"

	result, err := svc.BurnSubtitles(ctx, &BurnSubtitlesData{
		CreateSubtitlesData: CreateSubtitlesData{Transcript: speech("Сегодня разберём три ошибки в планке"), Hook: hook},
		VideoPath:           input, OutputPath: output,
	})
	require.NoError(t, err)
	assert.Empty(t, result.FontFallbacks)

	style := DefaultStyle()
	margin := style.hookSubtitlesMargin(style.hookLineCount(hook))
	shown := grayFrame(ctx, t, output, 1)

	top, bottom, left, right, found := brightBounds(shown)
	require.True(t, found)
	assert.GreaterOrEqual(t, top, SafeZoneTop)
	assert.Less(t, bottom, FrameHeight-SafeZoneBottom)
	assert.Greater(t, left, SafeZoneLeft)
	assert.Less(t, right, FrameWidth-SafeZoneRight)
	assert.Equal(t, 2, style.hookLineCount(hook))
	assert.Positive(t, rowsBright(shown, style.Layout.MarginTop, margin-style.Hook.Gap), "the hook is drawn in its band")
	assert.Positive(t, rowsBright(shown, margin, margin+style.blockHeight()), "the subtitles under it")

	assert.Zero(t, rowsBright(grayFrame(ctx, t, output, 2.8), 0, FrameHeight), "the hook is gone after its time")

	// The hook is set in Manrope Regular, not a silent fallback to the Medium of the subtitles.
	regular := false

	for _, f := range result.Fonts {
		if f.Family == style.Hook.Font {
			assert.Equal(t, "Manrope-Regular", f.PostScriptName, "the hook font: %s", f.Font)
			regular = true
		}
	}

	assert.True(t, regular, "libass reports the hook font")

	// On a white frame the vignette and the shade under the shifted subtitles darken the side of the frame (x=60, no
	// text) evenly: no band darker than the plateau under the hook.
	white, shaded := filepath.Join(dir, "white.mp4"), filepath.Join(dir, "white-out.mp4")
	runTool(ctx, t, "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i",
		"color=c=white:s="+reelSize+":d=3:r=25", "-f", "lavfi", "-i", "sine=frequency=440:duration=3", "-c:v", "libx264",
		"-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", white)

	_, err = svc.BurnSubtitles(ctx, &BurnSubtitlesData{
		CreateSubtitlesData: CreateSubtitlesData{Transcript: speech("Сегодня разберём три ошибки в планке"), Hook: hook},
		VideoPath:           white, OutputPath: shaded,
	})
	require.NoError(t, err)

	frame := grayFrame(ctx, t, shaded, 1)
	plateau := int(frame[shadePlateauRow*FrameWidth+shadeSideColumn])

	for y := shadeBandFrom; y < shadeBandTo; y++ {
		level := int(frame[y*FrameWidth+shadeSideColumn])
		assert.LessOrEqual(t, max(level-plateau, plateau-level), maxShadeStep, "row %d: %d against the plateau %d", y,
			level, plateau)
	}
}

// The side of the frame checked for an even shade: the plateau of the vignette and the band where the shade joins it.
const (
	shadeSideColumn = 60
	shadePlateauRow = 400
	shadeBandFrom   = 460
	shadeBandTo     = 560
	maxShadeStep    = 20
)

// TestHookShade: while the subtitles stand under the hook, a drawing extends the vignette down to them: the first
// event, from the start to the end of the last shifted subtitle, mirrored at the bottom; none in the middle or without
// a vignette. A one-line hook moves the subtitles less.
func TestHookShade(t *testing.T) {
	t.Parallel()

	top := DefaultStyle()
	bottom := DefaultStyle()
	bottom.Layout.Position = BottomPosition
	middle := DefaultStyle()
	middle.Layout.Position = MiddlePosition
	noVignette := DefaultStyle()
	noVignette.Vignette.Enabled = false

	result := speech("Сегодня разберём три ошибки, которые все делают в планке. Держим спину ровно и дышим.",
		"А теперь самое главное.")

	tests := []struct {
		name    string
		style   *Style
		hook    string
		shape   string
		marginV string
	}{
		{
			name: "one line at the top", style: &top, hook: hookPlank,
			shape: "m -100 600 l 1180 600 l 1180 645 l -100 645{\\p0}", marginV: shiftedUnderHook,
		},
		{
			name: "two lines move further", style: &top, hook: "Видео для тех, кто боится начать тренироваться дома",
			shape: "m -100 600 l 1180 600 l 1180 720 l -100 720", marginV: ",Main,,0,0,430,,",
		},
		{
			name: "at the bottom: mirrored", style: &bottom, hook: hookPlank,
			shape: "m -100 1095 l 1180 1095 l 1180 1140 l -100 1140", marginV: ",Main,,0,0,535,,",
		},
		{name: "in the middle: none", style: &middle, hook: hookPlank},
		{name: "no vignette: none", style: &noVignette, hook: hookPlank, marginV: shiftedUnderHook},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ass, _ := buildASS(result, tt.style, nil, false, nil, tt.hook)
			events := strings.Split(ass[strings.Index(ass, eventFormat)+len(eventFormat)+1:], "\n")

			if tt.shape == "" {
				assert.NotContains(t, ass, ",Shade,,")
				assert.NotContains(t, ass, "Style: Shade,")

				if tt.marginV != "" {
					assert.Contains(t, ass, tt.marginV)
				}

				return
			}

			require.True(t, strings.HasPrefix(events[0], "Dialogue: 0,0:00:00.00,"), "the shade is the first event")
			assert.Contains(t, events[0], ",Shade,,0,0,0,,{\\an7\\pos(0,0)\\fad(150,150)\\1c&H000000&\\1a&H66&\\blur60\\p1}")
			assert.Contains(t, events[0], tt.shape)
			assert.Contains(t, ass, "Style: Shade,")

			lastShifted := ""

			for _, line := range events[1:] {
				if strings.Contains(line, tt.marginV) {
					lastShifted = strings.Split(line, ",")[2]
				}
			}

			require.NotEmpty(t, lastShifted)
			assert.Equal(t, lastShifted, strings.Split(events[0], ",")[2], "the shade ends with the last shifted subtitle")
		})
	}
}
