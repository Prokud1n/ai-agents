package mediaworker

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
)

// The grey of our own frame (RGB 133: Y' ≈ 130) and the trainer's mean luma 98: the defaults aim at 95–105.
const (
	greyColor      = "0x858585"
	gradedMinLuma  = 95.0
	gradedMaxLuma  = 105.0
	greySourceLuma = 130.0
	// The light wall over dark clothes of T3.28 (the thirds of our source about 161 / 100), synthetic: a light top of
	// Y' 150, a lighter middle of 160 and a dark bottom of 80 — the mean 130 as ours (RGB 160 / 172 / 79: the mpeg4 of
	// makeClip lands a few code values under the BT.709 luma of the colour, as with the grey).
	lightTopColor    = "0xa0a0a0"
	lightMiddleColor = "0xacacac"
	darkBottomColor  = "0x4f4f4f"
	lightTopLuma     = 150.0
	darkBottomLuma   = 80.0
	// minTopGap: the upper third of the reel is at least this much darker than its lower third (the criterion).
	minTopGap = 10.0
)

// TestIntegrationGradeDefaults: the worker with the defaults of MEDIA_WORKER_* (the grade on, its exposure and shadow)
// measures a talking head (its mean and thirds), picks the grade by it (gradeSettings.forSource) and renders a reel as
// dark as the trainer's on average, with the real ffmpeg: a flat grey of the brightness of our frame as in T3.25, and a
// light top over a dark bottom (T3.28) with its upper third at least 10 darker than the lower third. The reel has one
// key frame: MeanLuma measures its first frame, before the first subtitle and the first zoom.
func TestIntegrationGradeDefaults(t *testing.T) {
	t.Parallel()
	requireIntegrationFFmpeg(t)

	var defaults config.MediaWorker
	require.NoError(t, env.ParseWithOptions(&defaults, env.Options{Environment: map[string]string{}}))
	require.True(t, defaults.Grade)

	tests := []struct {
		name   string
		video  string
		top    float64
		bottom float64
	}{
		{name: "a flat grey", video: stillColor(greyColor, verticalSize), top: greySourceLuma, bottom: greySourceLuma},
		{
			name: "a light top over a dark bottom",
			// One source with two boxes: three sources stacked in one lavfi graph crash the ffmpeg of the media image.
			video: stillColor(lightMiddleColor, verticalSize) + ",drawbox=x=0:y=0:w=720:h=427:color=" + lightTopColor +
				":t=fill,drawbox=x=0:y=853:w=720:h=427:color=" + darkBottomColor + ":t=fill",
			top: lightTopLuma, bottom: darkBottomLuma,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			source := filepath.Join(t.TempDir(), "source.mkv")
			makeClip(t, source, tt.video, false)

			runner := ffmpeg.NewRunner(&config.FFmpeg{Timeout: 2 * time.Minute})
			measured, err := runner.MeanLuma(t.Context(), source, sampleMoments(clipDuration), false, 0)
			require.NoError(t, err)
			assert.InDelta(t, greySourceLuma, measured.Mean, 4, "the brightness of our source")
			assert.InDelta(t, tt.top, measured.Top, 4)
			assert.InDelta(t, tt.bottom, measured.Bottom, 4)

			reel, grade := renderDefaultGrade(t, &defaults, source)

			t.Logf("grade defaults, %s: source %.1f (top %.1f, bottom %.1f) → reel %.1f (top %.1f, bottom %.1f); %s",
				tt.name, measured.Mean, measured.Top, measured.Bottom, reel.Mean, reel.Top, reel.Bottom, grade)
			assert.GreaterOrEqual(t, reel.Mean, gradedMinLuma)
			assert.LessOrEqual(t, reel.Mean, gradedMaxLuma)
			assert.LessOrEqual(t, reel.Top, reel.Bottom-minTopGap, "the top of the frame is darker than its bottom")
			assert.Contains(t, grade, ": как у тренера")
			assert.Contains(t, grade, "средняя яркость исходника "+strconv.Itoa(int(measured.Mean+0.5)))
			assert.Contains(t, grade, "верхняя/нижняя треть ")
		})
	}
}

// renderDefaultGrade runs the worker of the defaults of the grade on the source and returns the luma of the first
// frame of its reel and the line of the colour in its decisions log.
func renderDefaultGrade(t *testing.T, defaults *config.MediaWorker, source string) (luma ffmpeg.Luma, grade string) {
	t.Helper()

	store := newLocalStore(map[string]string{testSourceKey: source})
	// The trainer's style (the default): small white phrases, no plates.
	pipeline := realPipelineConfigured(t, store, &fakeTranscriber{result: clipTranscript()}, "",
		func(cfg *config.MediaWorker) {
			cfg.Grade, cfg.GradeExposure, cfg.GradeTop, cfg.Zoom = defaults.Grade, defaults.GradeExposure,
				defaults.GradeTop, defaults.Zoom
		})

	result, err := pipeline.Run(t.Context(), testJob())
	require.NoError(t, err)
	require.Equal(t, editjob.DoneStatus, result.Status)

	files, err := editjob.Files(store, testSourceKey, 1)
	require.NoError(t, err)

	reel := filepath.Join(t.TempDir(), "reel.mp4")
	require.NoError(t, os.WriteFile(reel, store.results[files.Reel].data, 0o600))

	luma, err = ffmpeg.NewRunner(&config.FFmpeg{Timeout: 2 * time.Minute}).MeanLuma(t.Context(), reel, []float64{0}, false, 0)
	require.NoError(t, err)

	log := string(store.results[files.Decisions].data)
	_, grade, found := strings.Cut(log, "- Цвет кадра")
	require.True(t, found, log)
	grade, _, _ = strings.Cut(grade, "\n")

	return luma, grade
}

// stillColor is a lavfi source of a still colour of the size for the clip of makeClip.
func stillColor(color, size string) string {
	return "color=c=" + color + ":size=" + size + ":rate=30:duration=" + strconv.Itoa(clipDuration)
}
