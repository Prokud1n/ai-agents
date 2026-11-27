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

// The grey of our own frame (RGB 133: Y' ≈ 130) and the trainer's mean luma 96: the defaults aim at 95–105.
const (
	greyColor      = "0x858585"
	gradedMinLuma  = 95.0
	gradedMaxLuma  = 105.0
	greySourceLuma = 130.0
)

// TestIntegrationGradeDefaults: the worker with the defaults of MEDIA_WORKER_* (the grade on, its exposure and shadow)
// measures a grey talking head of the brightness of our frame, picks the exposure by it (gradeSettings.forSource) and
// renders a reel as dark as the trainer's on average, with the real ffmpeg. The reel has one key frame: MeanLuma
// measures its first frame, before the first subtitle.
func TestIntegrationGradeDefaults(t *testing.T) {
	t.Parallel()
	requireIntegrationFFmpeg(t)

	var defaults config.MediaWorker
	require.NoError(t, env.ParseWithOptions(&defaults, env.Options{Environment: map[string]string{}}))
	require.True(t, defaults.Grade)

	source := filepath.Join(t.TempDir(), "grey.mkv")
	makeGreyClip(t, source)

	runner := ffmpeg.NewRunner(&config.FFmpeg{Timeout: 2 * time.Minute})
	measured, err := runner.MeanLuma(t.Context(), source, sampleMoments(clipDuration), false)
	require.NoError(t, err)
	assert.InDelta(t, greySourceLuma, measured, 4)

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

	mean, err := runner.MeanLuma(t.Context(), reel, []float64{0}, false)
	require.NoError(t, err)

	log := string(store.results[files.Decisions].data)
	_, grade, found := strings.Cut(log, "- Цвет кадра")
	require.True(t, found, log)
	grade, _, _ = strings.Cut(grade, "\n")

	t.Logf("grade defaults: source %.1f → reel %.1f; %s", measured, mean, grade)
	assert.GreaterOrEqual(t, mean, gradedMinLuma)
	assert.LessOrEqual(t, mean, gradedMaxLuma)
	assert.Contains(t, grade, ": как у тренера")
	assert.Contains(t, grade, "средняя яркость исходника "+strconv.Itoa(int(measured+0.5)))
}

// makeGreyClip is makeSpeechClip with a still grey picture of the brightness of our own frame.
func makeGreyClip(t *testing.T, path string) {
	t.Helper()

	makeClip(t, path, "color=c="+greyColor+":size="+verticalSize+":rate=30:duration="+strconv.Itoa(clipDuration), false)
}
