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

// Gray levels of the trainer's style on a dark gray clip: the background, the peach hook and headings (about 178),
// the white frame of the animation and the subtitles, any text.
const (
	trainerGray  = "0x303030" // 48
	peachLow     = 140
	peachHigh    = 210
	whiteLevel   = 230
	inkLevel     = 100
	trainerReel  = 6
	rowTolerance = 12
)

// The places measured on the trainer's own reel (px of 1080×1920): the capitals of «СТРАХ 1» 386..433, the first
// line of her subtitles from 470, the top line «tg: @ana_prokudina» 272..301.
const (
	refSectionTop    = 386
	refSectionBottom = 433
	refSubtitlesTop  = 470
	refTopbarTop     = 272
	refTopbarBottom  = 301
)

// inkRows are the first and the last row of the box [x0, x1) × [y0, y1) with a pixel the check accepts; -1, -1 for
// none.
func inkRows(frame []byte, b area, check func(level byte) bool) (first, last int) {
	first, last = -1, -1

	for y := b.y0; y < b.y1; y++ {
		for x := b.x0; x < b.x1; x++ {
			if check(frame[y*FrameWidth+x]) {
				if first < 0 {
					first = y
				}

				last = y

				break
			}
		}
	}

	return first, last
}

func isPeach(v byte) bool { return v >= peachLow && v <= peachHigh }

func isWhite(v byte) bool { return v >= whiteLevel }

func isInk(v byte) bool { return v >= inkLevel }

// TestBurnTrainerStyleWithFFmpeg: libass draws the trainer's style (the default) as in her reel: the hook small at
// the right in its selection frame with the cursor, then large in the middle without them; the section heading above
// the question in the subtitles once the hook is gone; the top line over the reel — at the places measured on her reel
// within rowTolerance.
func TestBurnTrainerStyleWithFFmpeg(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	ctx := t.Context()
	runner := ffmpeg.NewRunner(&config.FFmpeg{Timeout: 2 * time.Minute})
	svc, err := NewService(runner, &config.Subtitles{FontsDir: testFontsDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)

	dir := t.TempDir()
	input, output := filepath.Join(dir, "gray.mp4"), filepath.Join(dir, "trainer.mp4")
	runTool(ctx, t, "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i",
		"color=c="+trainerGray+":s="+reelSize+":d=6:r=25", "-f", "lavfi", "-i", "sine=frequency=440:duration=6",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", input)

	// The hook until the heading at 2.5 s; the heading over the question (3.45..5.5 s) to the end of the reel.
	transcript := speech(hookPhrase, fear1Phrase, "А что если онлайн-тренировка менее эффективна?")
	transcript.Duration = trainerReel
	data := CreateSubtitlesData{
		Transcript: transcript, Hook: trainerHook, Notes: []Note{{Kind: SectionNote, Text: fear1, Start: 2.5}},
		Branding: &Branding{TopbarLeft: tgLeft, TopbarRight: speedRight},
	}

	doc, err := svc.CreateSubtitles(&data)
	require.NoError(t, err)
	t.Logf("the trainer's style document:\n%s", doc.ASS)

	result, err := svc.BurnSubtitles(ctx, &BurnSubtitlesData{CreateSubtitlesData: data, VideoPath: input, OutputPath: output})
	require.NoError(t, err)
	assert.Empty(t, result.FontFallbacks)
	assert.Empty(t, result.MissingGlyphs)

	hookBand := func(x0, x1 int) area { return area{x0: x0, y0: 440, x1: x1, y1: 640} }

	small := grayFrame(ctx, t, output, 0.2)
	assert.Zero(t, countPixels(small, hookBand(0, 400), isInk), "the small hook stands at the right")
	assert.Greater(t, countPixels(small, hookBand(FrameWidth/2, FrameWidth), isPeach), 300, "the small hook")
	assert.Greater(t, countPixels(small, hookBand(FrameWidth/2, FrameWidth), isWhite), 100, "its frame and cursor")

	grown := grayFrame(ctx, t, output, 1.6)
	assert.Greater(t, countPixels(grown, hookBand(0, 300), isPeach), 300, "the hook grew to the middle")
	assert.Greater(t, countPixels(grown, hookBand(FrameWidth-300, FrameWidth), isPeach), 300)
	assert.Zero(t, countPixels(grown, area{x0: 0, y0: 400, x1: FrameWidth, y1: 700}, isWhite), "no frame, no cursor")

	section := grayFrame(ctx, t, output, 4)
	top, bottom := inkRows(section, area{x0: 0, y0: 340, x1: FrameWidth, y1: 452}, isPeach)
	assert.InDelta(t, refSectionTop, top, rowTolerance, "the heading's capitals")
	assert.InDelta(t, refSectionBottom, bottom, rowTolerance)

	top, _ = inkRows(section, area{x0: 0, y0: 452, x1: FrameWidth, y1: 700}, isWhite)
	assert.InDelta(t, refSubtitlesTop, top, rowTolerance, "the subtitles under it")
	assert.Zero(t, countPixels(section, area{x0: 0, y0: 640, x1: FrameWidth, y1: 1100}, isInk), "the hook is gone")

	top, bottom = inkRows(section, area{x0: 0, y0: 240, x1: 400, y1: 340}, isInk)
	assert.InDelta(t, refTopbarTop, top, rowTolerance, "the top line at the left")
	assert.InDelta(t, refTopbarBottom, bottom, rowTolerance)
	assert.Positive(t, countPixels(section, area{x0: FrameWidth - 120, y0: 240, x1: FrameWidth, y1: 340}, isInk),
		"«2x» at the right")
}
