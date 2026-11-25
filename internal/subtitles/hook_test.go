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

const hookPlank = "Три ошибки в планке"

func TestHookEnd(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	long := speech("Сегодня разберём три ошибки, которые все делают в планке каждый день.")
	short := speech("Привет!", "Сегодня разберём три ошибки, которые все делают в планке.")
	tiny := speech("Привет!")

	tests := []struct {
		name     string
		hook     string
		input    *transcription.Result
		expected float64
	}{
		{name: "no hook", hook: " ", input: long, expected: 0},
		{name: "the style's duration", hook: hookPlank, input: long, expected: 2.5},
		{name: "half a second after the first phrase", hook: hookPlank, input: short, expected: 0.85},
		{name: "not longer than the reel", hook: hookPlank, input: tiny, expected: 0.85},
		{
			name: "no speech: the style's duration", hook: hookPlank,
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
	plain := style
	plain.Hook.Highlight = ""
	plain.Hook.Uppercase = false

	tests := []struct {
		name     string
		hook     string
		end      float64
		style    *Style
		expected string
	}{
		{
			name: "capitals, the longest word highlighted, fading", hook: hookPlank, end: 2.5, style: &style,
			expected: `Dialogue: 2,0:00:00.00,0:00:02.50,Hook,,0,0,0,,{\fad(150,150)}ТРИ {\1c&H4BB9F2&\1a&H00&}` +
				`ОШИБКИ{\1c&HE1F0F5&\1a&H00&}\NВ ПЛАНКЕ` + "\n",
		},
		{
			name: "no preposition at the end of the first line", hook: "Три ошибки в планке за 10 минут", end: 2.5,
			style: &plain,
			expected: `Dialogue: 2,0:00:00.00,0:00:02.50,Hook,,0,0,0,,{\fad(150,150)}{\fscx94}Три ошибки в планке\N` +
				`{\fscx100}за 10 минут` + "\n",
		},
		{
			name: "a number with its word", hook: "Пресс за 10 минут", end: 2.5, style: &style,
			expected: `Dialogue: 2,0:00:00.00,0:00:02.50,Hook,,0,0,0,,{\fad(150,150)}ПРЕСС ЗА {\1c&H4BB9F2&\1a&H00&}` +
				`10 МИНУТ{\1c&HE1F0F5&\1a&H00&}` + "\n",
		},
		{
			name: "two even lines, the longer one squeezed", hook: "Как убрать живот без диет и голодовок", end: 2,
			style: &plain,
			expected: `Dialogue: 2,0:00:00.00,0:00:02.00,Hook,,0,0,0,,{\fad(150,150)}Как убрать живот\N` +
				`{\fscx90}без диет и голодовок` + "\n",
		},
		{
			name: "markup is text, the fade fits a short hook", hook: `Планка {\an7} \N`, end: 0.2, style: &plain,
			expected: `Dialogue: 2,0:00:00.00,0:00:00.20,Hook,,0,0,0,,{\fad(100,100)}Планка \{an7\} N` + "\n",
		},
		{
			name: "a long line is squeezed", hook: "Суперинтенсивнейшаятренировкапресса", end: 2.5, style: &plain,
			expected: `Dialogue: 2,0:00:00.00,0:00:02.50,Hook,,0,0,0,,{\fad(150,150)}{\fscx51}` +
				`Суперинтенсивнейшаятренировкапресса` + "\n",
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
		{name: "top", style: &top, hook: [2]int{260, 480}, shift: 500, shifts: true},
		{name: "bottom", style: &bottom, hook: [2]int{1260, 1480}, shift: 680, shifts: true},
		{name: "middle", style: &middle, hook: [2]int{260, 480}, shift: 0},
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
			assert.Equal(t, tt.shift, tt.style.hookSubtitlesMargin())

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
		{name: "too large for two lines", change: func(s *Style) { s.Hook.Size = 120 }},
		{name: "the subtitles under it reach the titles", change: func(s *Style) { s.Hook.Gap = 60; s.Title.Margin = 950 }},
		{name: "bad highlight", change: func(s *Style) { s.Hook.Highlight = "yellow" }},
		{name: "translucent highlight", change: func(s *Style) { s.Hook.Highlight = "#F2B94B40" }},
		{name: "too long", change: func(s *Style) { s.Hook.Duration = 8 }},
		{name: "fade longer than half", change: func(s *Style) { s.Hook.Duration = 1; s.Hook.Fade = 500 }},
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
	result := speech("Сегодня разберём три ошибки, которые все делают в планке. Держим спину ровно и дышим.",
		"А теперь самое главное.")

	ass, cues := buildASS(result, &style, nil, false, nil, hookPlank)
	require.Greater(t, cues, 2)

	assert.Contains(t, ass, "Style: Hook,Fira Sans Extra Condensed ExtraBold,88,&H00E1F0F5,&H00E1F0F5,&H00000000,"+
		"&H99000000,0,0,0,0,100,100,0,0,1,0,2,8,140,140,260,1\n")
	assert.Contains(t, ass, `Hook,,0,0,0,,{\fad(150,150)}ТРИ`)

	shifted, kept := 0, 0

	for line := range strings.SplitSeq(ass, "\n") {
		if !strings.Contains(line, ",Main,,") {
			continue
		}

		start := strings.Split(line, ",")[1]
		if start < "0:00:02.50" {
			assert.Contains(t, line, ",Main,,0,0,500,,", "starts with the hook: under it")
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
	assert.NotContains(t, plain, ",Main,,0,0,500,,")
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

	// Speech until 2.1 s: the hook stays until 2.5 s, nothing is on screen at 2.8 s.
	result, err := svc.BurnSubtitles(ctx, &BurnSubtitlesData{
		CreateSubtitlesData: CreateSubtitlesData{
			Transcript: speech("Сегодня разберём три ошибки в планке"), Hook: "Три ошибки в планке за 10 минут",
		},
		VideoPath: input, OutputPath: output,
	})
	require.NoError(t, err)
	assert.Empty(t, result.FontFallbacks)

	style := DefaultStyle()
	hookTop, hookBottom := style.hookSpan()
	shown := grayFrame(ctx, t, output, 1)

	top, bottom, left, right, found := brightBounds(shown)
	require.True(t, found)
	assert.GreaterOrEqual(t, top, SafeZoneTop)
	assert.Less(t, bottom, FrameHeight-SafeZoneBottom)
	assert.Greater(t, left, SafeZoneLeft)
	assert.Less(t, right, FrameWidth-SafeZoneRight)
	assert.Positive(t, rowsBright(shown, hookTop, hookBottom), "the hook is drawn in its band")
	assert.Positive(t, rowsBright(shown, hookBottom+style.Hook.Gap, FrameHeight/2), "the subtitles under it")

	assert.Zero(t, rowsBright(grayFrame(ctx, t, output, 2.8), 0, FrameHeight), "the hook is gone after its time")
}
