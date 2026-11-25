package subtitles

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
)

// Gray levels of the reference render: the gray clip, the white plate and the black word on it.
const (
	referenceGray = "0x606060" // 96
	plateLevel    = 235
	wordLevel     = 60
	noteLevel     = 200
	handleLevel   = 150
	// cardDarkLevel: the gray under the card's 70 % black is about 29.
	cardDarkLevel = 45
)

// area is a rectangle of the frame for the pixel checks: [x0, x1) × [y0, y1).
type area struct {
	x0, y0, x1, y1 int
}

// countPixels counts the pixels of the box the check accepts.
func countPixels(frame []byte, b area, check func(level byte) bool) int {
	n := 0

	for y := b.y0; y < b.y1; y++ {
		for x := b.x0; x < b.x1; x++ {
			if check(frame[y*FrameWidth+x]) {
				n++
			}
		}
	}

	return n
}

// TestBurnReferenceStyleWithFFmpeg: libass draws the reference look (the default style) — the word black on its white
// plate at the left edge at a third of the height, a callout at the top, the handle with its icon at the right edge —
// and the end card darkens the last two seconds with the handle in the middle, the watermark and the words gone.
func TestBurnReferenceStyleWithFFmpeg(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	ctx := t.Context()
	runner := ffmpeg.NewRunner(&config.FFmpeg{Timeout: 2 * time.Minute})
	svc, err := NewService(runner, &config.Subtitles{FontsDir: testFontsDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)

	dir := t.TempDir()
	input, output := filepath.Join(dir, "gray.mp4"), filepath.Join(dir, "reference.mp4")
	runTool(ctx, t, "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i",
		"color=c="+referenceGray+":s="+reelSize+":d=6:r=25", "-f", "lavfi", "-i", "sine=frequency=440:duration=6",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", input)

	// «10 приседаний» is one word cue from 0.3 to 1.0 s; the speech goes on to the end of the reel (6 s), the card
	// takes its last 2 s and hides it.
	transcript := speech("Делаем 10 приседаний каждое утро.", "Держим спину ровно и дышим спокойно, не торопимся.")
	transcript.Duration = 6

	result, err := svc.BurnSubtitles(ctx, &BurnSubtitlesData{
		CreateSubtitlesData: CreateSubtitlesData{
			Transcript: transcript,
			Notes:      []Note{{Kind: CalloutNote, Text: "10 приседаний", Start: 0.5}},
			Branding:   &Branding{Handle: testHandle, Icon: true, EndCard: true},
		},
		VideoPath: input, OutputPath: output,
	})
	require.NoError(t, err)
	assert.Empty(t, result.FontFallbacks)
	assert.Empty(t, result.MissingGlyphs)

	style := DefaultStyle()
	plate := style.wordBoxRect()
	plateBox := area{x0: plate.left - 10, y0: plate.top - 10, x1: plate.right, y1: plate.bottom + 10}
	handle := style.handleRect()
	handleBox := area{x0: handle.left, y0: handle.top - 5, x1: handle.right + 5, y1: handle.bottom + 5}

	shown := grayFrame(ctx, t, output, 0.8)

	assert.Greater(t, countPixels(shown, plateBox, func(v byte) bool { return v >= plateLevel }), 4000,
		"the white plate under the word")
	assert.Greater(t, countPixels(shown, plateBox, func(v byte) bool { return v <= wordLevel }), 300,
		"the black word on it")
	assert.InDelta(t, style.WordBox.X, firstColumn(shown, (plate.top+plate.bottom)/2, plateLevel), 6,
		"the plate starts at word_box.x")
	assert.Greater(t, countPixels(shown, area{x0: 0, y0: style.Layout.MarginTop, x1: FrameWidth, y1: plate.top - 20},
		func(v byte) bool { return v >= noteLevel }), 300, "the callout at the top")
	assert.Greater(t, countPixels(shown, handleBox, func(v byte) bool { return v >= handleLevel }), 150,
		"the handle at the right edge")

	card := grayFrame(ctx, t, output, 4.8) // the card faded in, the speech goes on under it
	center := area{x0: FrameWidth/2 - 250, y0: FrameHeight/2 - 120, x1: FrameWidth/2 + 250, y1: FrameHeight/2 + 120}

	assert.Less(t, int(card[1500*FrameWidth+900]), cardDarkLevel, "the card darkens the frame")
	assert.Greater(t, countPixels(card, center, func(v byte) bool { return v >= noteLevel }), 1000,
		"the icon and the handle in the middle of the card")
	assert.Zero(t, countPixels(card, handleBox, func(v byte) bool { return v >= handleLevel }),
		"the watermark is gone under the card")
	assert.Zero(t, countPixels(card, plateBox, func(v byte) bool { return v >= plateLevel }), "no word under the card")
}

// firstColumn is the first column of the row at least as bright as the level, -1 for none.
func firstColumn(frame []byte, row int, level byte) int {
	for x := range FrameWidth {
		if frame[row*FrameWidth+x] >= level {
			return x
		}
	}

	return -1
}
