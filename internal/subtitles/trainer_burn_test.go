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
	"github.com/Prokud1n/ai-agents/internal/transcription"
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
	// Her hook takes two lines, the capitals 440..620 px; a hook a word longer takes two lines made smaller (T3.23),
	// above longHookBottom.
	refHookTop     = 440
	refHookBottom  = 620
	longHookBottom = 640
	// questionAt: the question of «the reading cut out» starts with the heading.
	questionAt = 1.9
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

// TestBurnTrainerStyleWithFFmpeg: libass draws the trainer's style (the default) as in her reel. Her hook alone (no
// subtitles under it), small at the right in its selection frame with the cursor, then large in the middle without
// them, in her hook's rows; the section heading alone while the phrase that reads it out is said, then above the
// question in the subtitles; the top line over the reel — at the places measured on her reel within rowTolerance. A
// hook a word longer takes two lines made smaller, a hook of two clauses too; with the reading cut out of the sound the
// heading starts on the question, and its subtitle is shown with it under the heading.
func TestBurnTrainerStyleWithFFmpeg(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	runner := ffmpeg.NewRunner(&config.FFmpeg{Timeout: 2 * time.Minute})
	svc, err := NewService(runner, &config.Subtitles{FontsDir: testFontsDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)

	t.Run("the reading said", func(t *testing.T) {
		t.Parallel()

		// The hook until the heading at 1.9 s, where «страх номер один» is said (1.9..2.95 s, not in the subtitles);
		// the heading stays to the end of the question (3.45..5.55 s).
		output := renderTrainer(t, svc, trainerHook, speech(hookPhrase, fear1Phrase, trainerQuestion))
		checkHook(t, output)

		grown := grayFrame(t.Context(), t, output, 1.7)
		top, bottom := inkRows(grown, area{x0: 0, y0: 380, x1: FrameWidth, y1: 800}, isPeach)
		t.Logf("her hook's capitals: %d..%d px (hers: %d..%d)", top, bottom, refHookTop, refHookBottom)
		assert.InDelta(t, refHookTop, top, rowTolerance, "her hook's rows")
		assert.InDelta(t, refHookBottom, bottom, rowTolerance)

		for _, at := range []float64{2.6, 3.0} {
			alone := grayFrame(t.Context(), t, output, at)
			assert.Positive(t, countPixels(alone, area{x0: 0, y0: 340, x1: FrameWidth, y1: 452}, isPeach), "the heading")
			assert.Zero(t, countPixels(alone, area{x0: 0, y0: 452, x1: FrameWidth, y1: 1100}, isInk),
				"«страх номер один» is not doubled in the subtitles: the heading stands alone at %.1f s", at)
		}

		checkSection(t, grayFrame(t.Context(), t, output, 4.6))
	})

	t.Run("the reading cut out", func(t *testing.T) {
		t.Parallel()

		output := renderTrainer(t, svc, fiveMythsHook, speech(hookPhrase, trainerQuestion))

		top, bottom := inkRows(grayFrame(t.Context(), t, output, 1.7), area{x0: 0, y0: 380, x1: FrameWidth, y1: 800},
			isPeach)
		t.Logf("the longer hook's capitals: %d..%d px", top, bottom)
		assert.Positive(t, top, "the hook is drawn")
		assert.LessOrEqual(t, bottom, longHookBottom, "two lines, no third one over the face")

		// The question 1.9..3.95 s, its subtitle from 2.1 s under the heading.
		checkSection(t, grayFrame(t.Context(), t, output, 3.0))
	})

	t.Run("a hook of two clauses", func(t *testing.T) {
		t.Parallel()

		output := renderTrainer(t, svc, twoClauseHook, speech(hookPhrase, trainerQuestion))

		top, bottom := inkRows(grayFrame(t.Context(), t, output, 1.7), area{x0: 0, y0: 380, x1: FrameWidth, y1: 800},
			isPeach)
		t.Logf("the hook of two clauses: %d..%d px", top, bottom)
		assert.Positive(t, top, "the hook is drawn")
		assert.LessOrEqual(t, bottom, longHookBottom, "two lines, no third one over the face")
	})
}

// trainerQuestion is the question of a section in the synthetic talk.
const trainerQuestion = "А что если онлайн-тренировка менее эффективна?"

// renderTrainer burns the trainer's style with the hook and a heading at questionAt over the speech onto a gray clip
// and returns the reel.
func renderTrainer(t *testing.T, svc *Service, hook string, transcript *transcription.Result) string {
	t.Helper()

	ctx := t.Context()
	dir := t.TempDir()
	input, output := filepath.Join(dir, "gray.mp4"), filepath.Join(dir, "trainer.mp4")
	runTool(ctx, t, "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i",
		"color=c="+trainerGray+":s="+reelSize+":d=6:r=25", "-f", "lavfi", "-i", "sine=frequency=440:duration=6",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", input)

	transcript.Duration = trainerReel
	data := CreateSubtitlesData{
		Transcript: transcript, Hook: hook, Notes: []Note{{Kind: SectionNote, Text: fear1, Start: questionAt}},
		Branding: &Branding{TopbarLeft: tgLeft, TopbarRight: speedRight},
	}

	doc, err := svc.CreateSubtitles(&data)
	require.NoError(t, err)
	t.Logf("the trainer's style document:\n%s", doc.ASS)

	result, err := svc.BurnSubtitles(ctx, &BurnSubtitlesData{CreateSubtitlesData: data, VideoPath: input, OutputPath: output})
	require.NoError(t, err)
	assert.Empty(t, result.FontFallbacks)
	assert.Empty(t, result.MissingGlyphs)

	return output
}

// checkHook: the hook stands small at the right in its frame with the cursor, then grows to the middle without them,
// with no subtitles under it.
func checkHook(t *testing.T, output string) {
	t.Helper()

	hookBand := func(x0, x1 int) area { return area{x0: x0, y0: 440, x1: x1, y1: 640} }

	small := grayFrame(t.Context(), t, output, 0.5)
	assert.Zero(t, countPixels(small, hookBand(0, 400), isInk), "the small hook stands at the right")
	assert.Greater(t, countPixels(small, hookBand(FrameWidth/2, FrameWidth), isPeach), 300, "the small hook")
	assert.Greater(t, countPixels(small, hookBand(FrameWidth/2, FrameWidth), isWhite), 100, "its frame and cursor")

	grown := grayFrame(t.Context(), t, output, 1.7)
	assert.Greater(t, countPixels(grown, hookBand(0, 300), isPeach), 300, "the hook grew to the middle")
	assert.Greater(t, countPixels(grown, hookBand(FrameWidth-300, FrameWidth), isPeach), 300)
	assert.Zero(t, countPixels(grown, area{x0: 0, y0: 400, x1: FrameWidth, y1: 700}, isWhite), "no frame, no cursor")
	assert.Zero(t, countPixels(grown, area{x0: 0, y0: 700, x1: FrameWidth, y1: 1100}, isInk), "no subtitles under it")
}

// checkSection: the heading above the subtitles of the question and the top line, at the places of her reel; the hook
// is gone.
func checkSection(t *testing.T, section []byte) {
	t.Helper()

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
