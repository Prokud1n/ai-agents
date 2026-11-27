package mediaworker

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

// flatLuma is a picture of one brightness.
func flatLuma(v float64) ffmpeg.Luma {
	return ffmpeg.Luma{Mean: v, Top: v, Bottom: v}
}

// TestGradeForSource: a bright source is darkened to about the trainer's frame, at most by the configured exposure; a
// source already near it keeps its exposure, and a dark one is only warmed. A light top over a darker bottom gets a
// stronger shadow of the top (its upper third ends up topGap darker than the lower third) and a weaker curve, so the
// mean stays at the trainer's. Without a measurement the grade is as configured.
func TestGradeForSource(t *testing.T) {
	t.Parallel()

	settings := gradeSettings{on: true, exposure: -0.3, top: 0.3}

	tests := []struct {
		name     string
		settings gradeSettings
		luma     ffmpeg.Luma
		expected ffmpeg.Grade
	}{
		{name: "not measured", luma: ffmpeg.Luma{}, expected: ffmpeg.Grade{Exposure: -0.3, Warmth: 2, Top: 0.3}},
		// The grey of our frame: −0.30 EV and the configured 30 % estimate the reel at 100 (99.2 at 130), as in T3.25.
		{name: "a flat bright frame as ours", luma: flatLuma(131), expected: ffmpeg.Grade{Exposure: -0.3, Warmth: 2, Top: 0.3}},
		{name: "a flat frame a little brighter than the level", luma: flatLuma(125), expected: ffmpeg.Grade{Exposure: -0.22, Warmth: 2, Top: 0.3}},
		{name: "a very bright frame: the exposure is capped", luma: flatLuma(180), expected: ffmpeg.Grade{Exposure: -0.3, Warmth: 2, Top: 0.3}},
		{name: "at the level: only the shadow of the top", luma: flatLuma(110), expected: ffmpeg.Grade{Warmth: 2, Top: 0.3}},
		{name: "between: half the shadow", luma: flatLuma(105), expected: ffmpeg.Grade{Warmth: 2, Top: 0.15}},
		{name: "a dark frame: only the warmth", luma: flatLuma(92), expected: ffmpeg.Grade{Warmth: 2}},
		{name: "under the black (superblack, noise): only the warmth", luma: flatLuma(12), expected: ffmpeg.Grade{Warmth: 2}},
		{name: "over the white: no curve, the shadow of the top", luma: flatLuma(240), expected: ffmpeg.Grade{Warmth: 2, Top: 0.3}},
		// The source of live check 3 (a light wall over dark clothes: thirds about 161 / 100): the grade of T3.25
		// (−0.30 EV, 30 %) is estimated at 106.5 / 79 — the check measured 106.7 / 78.5. Now 83 / 96, the mean 99.
		{
			name: "a light wall over dark clothes", luma: ffmpeg.Luma{Mean: 130, Top: 161, Bottom: 100},
			expected: ffmpeg.Grade{Exposure: -0.05, Warmth: 2, Top: 0.54},
		},
		// 150 over 80: no curve (the bottom stays 80), the shadow of 63 % takes the top to 67, the mean 98.
		{
			name: "a light top over a dark bottom", luma: ffmpeg.Luma{Mean: 130, Top: 150, Bottom: 80},
			expected: ffmpeg.Grade{Warmth: 2, Top: 0.63},
		},
		{
			name: "the shadow is capped", luma: ffmpeg.Luma{Mean: 140, Top: 220, Bottom: 60},
			expected: ffmpeg.Grade{Warmth: 2, Top: maxFittedTop},
		},
		{
			name: "the top already darker than the bottom: the shadow as configured", luma: ffmpeg.Luma{Mean: 130, Top: 100, Bottom: 160},
			expected: ffmpeg.Grade{Exposure: -0.3, Warmth: 2, Top: 0.3},
		},
		{
			name: "MEDIA_WORKER_GRADE_TOP 0: no shadow at all", settings: gradeSettings{on: true, exposure: -0.3},
			luma: ffmpeg.Luma{Mean: 130, Top: 150, Bottom: 80}, expected: ffmpeg.Grade{Exposure: -0.3, Warmth: 2},
		},
		{
			name: "a weaker configured exposure is the limit", settings: gradeSettings{on: true, exposure: -0.1, top: 0.3},
			luma: flatLuma(131), expected: ffmpeg.Grade{Exposure: -0.1, Warmth: 2, Top: 0.3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := settings
			if tt.settings.on {
				g = tt.settings
			}

			assert.Equal(t, &tt.expected, g.forSource(tt.luma))
		})
	}
}

// TestGradeForSourceCriterion: the estimated reel of the fitted grade meets the criterion of T3.28 — the upper third
// at least 10 darker than the lower one, the mean within 95–105, the bottom not darker than under the grade of T3.25.
func TestGradeForSourceCriterion(t *testing.T) {
	t.Parallel()

	settings := gradeSettings{on: true, exposure: -0.3, top: 0.3}
	before := &ffmpeg.Grade{Exposure: -0.3, Warmth: 3, Top: 0.3}

	for _, luma := range []ffmpeg.Luma{
		{Mean: 130, Top: 161, Bottom: 100}, {Mean: 131, Top: 160, Bottom: 105}, {Mean: 130, Top: 150, Bottom: 80},
		flatLuma(130), {Mean: 128, Top: 145, Bottom: 105}, {Mean: 125, Top: 140, Bottom: 110},
	} {
		graded := settings.forSource(luma).Graded(luma)
		t.Logf("source %.0f (top %.0f, bottom %.0f) → reel %.1f (top %.1f, bottom %.1f)", luma.Mean, luma.Top,
			luma.Bottom, graded.Mean, graded.Top, graded.Bottom)
		assert.LessOrEqual(t, graded.Top, graded.Bottom-10, "the top is darker than the bottom")
		assert.GreaterOrEqual(t, graded.Mean, gradedMinLuma)
		assert.LessOrEqual(t, graded.Mean, gradedMaxLuma)
		assert.GreaterOrEqual(t, graded.Bottom, before.Graded(luma).Bottom-0.01, "the bottom is not darker than before")
	}
}

func TestSampleMoments(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []float64{10, 30, 50, 70, 90, 110}, sampleMoments(120), "the middles of six equal parts")
	assert.Equal(t, []float64{0}, sampleMoments(0), "the duration unknown: the first key frame")
}

// TestPipelineGrade: a talking head is graded by the measured brightness of its source (key frames spread over it,
// tone mapped for an HDR source), the zoom changes the framing at the start of each section; a workout is
// not graded and not measured; a failed measurement grades as configured. The decisions log says what was done.
func TestPipelineGrade(t *testing.T) {
	t.Parallel()

	sections := []roughcut.ScreenNote{
		{Kind: roughcut.ListTitleNote, Text: "3 вещи", Start: 0.2},
		{Kind: roughcut.SectionNote, Text: "страх 1", Start: 0.6},
	}

	tests := []struct {
		name      string
		change    func(f *pipelineFakes)
		expected  *ffmpeg.Grade
		measured  bool
		decisions string
	}{
		{
			name:     "a bright talking head",
			change:   func(f *pipelineFakes) { f.meter.luma = flatLuma(131) },
			expected: &ffmpeg.Grade{Exposure: -0.3, Warmth: 2, Top: 0.3}, measured: true,
			decisions: "- Цвет кадра: как у тренера — экспозиция -0.30 EV, тепло +2, верх кадра темнее на 30% " +
				"(средняя яркость исходника 131, верхняя/нижняя треть 131/131; в рилсе ожидается средняя 100, " +
				"верх/низ 83/111; задано -0.30 EV и 30%)\n",
		},
		{
			name:     "a light wall over dark clothes",
			change:   func(f *pipelineFakes) { f.meter.luma = ffmpeg.Luma{Mean: 130, Top: 161, Bottom: 100} },
			expected: &ffmpeg.Grade{Exposure: -0.05, Warmth: 2, Top: 0.54}, measured: true,
			decisions: "- Цвет кадра: как у тренера — экспозиция -0.05 EV, тепло +2, верх кадра темнее на 54% " +
				"(средняя яркость исходника 130, верхняя/нижняя треть 161/100; в рилсе ожидается средняя 99, " +
				"верх/низ 83/96; задано -0.30 EV и 30%)\n",
		},
		{
			name:     "a dark talking head",
			change:   func(f *pipelineFakes) { f.meter.luma = flatLuma(90) },
			expected: &ffmpeg.Grade{Warmth: 2}, measured: true,
			decisions: "- Цвет кадра: исходник уже тёмный (средняя яркость исходника 90) — без затемнения, только тепло +2\n",
		},
		{
			name:     "the measurement failed",
			change:   func(f *pipelineFakes) { f.meter.err = errors.New("no frame") },
			expected: &ffmpeg.Grade{Exposure: -0.3, Warmth: 2, Top: 0.3}, measured: true,
			decisions: "- Цвет кадра: как у тренера — экспозиция -0.30 EV, тепло +2, верх кадра темнее на 30% " +
				"(яркость исходника не измерена — пресет как задан; задано -0.30 EV и 30%)\n",
		},
		{
			name:      "a workout",
			change:    func(f *pipelineFakes) { f.cutter.result.Plan.Mode = roughcut.WorkoutMode },
			decisions: "- Цвет кадра: как в исходнике (тренировка)\n",
		},
		{
			name:      "off",
			change:    func(f *pipelineFakes) { f.grade = false },
			decisions: "- Цвет кадра: как в исходнике (пресет выключен)\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fakes := newPipelineFakes(t)
			fakes.grade, fakes.zoom, fakes.meter = true, testZoom, &fakeMeter{}
			fakes.prober.info.ColorTransfer = testHLG
			fakes.cutter.result.ScreenNotes = sections
			tt.change(fakes)

			_, err := fakes.pipeline(t, t.TempDir()).Run(t.Context(), testJob())
			require.NoError(t, err)

			cut := fakes.burner.got.Cut
			assert.Equal(t, tt.expected, cut.Grade)

			if tt.measured {
				assert.Equal(t, sampleMoments(182), fakes.meter.moments, "spread over the source, whatever the cut")
				assert.True(t, fakes.meter.toneMap, "an HDR source is measured as it is rendered")
				require.NotNil(t, cut.Zoom)
				assert.Equal(t, []float64{0.6}, cut.Zoom.Must, "the sections, not the other notes")
			} else {
				assert.Nil(t, fakes.meter.moments, "not measured")
			}

			files, err := editjob.Files(fakes.store, testSourceKey, 1)
			require.NoError(t, err)
			log := string(fakes.store.results[files.Decisions].data)
			assert.Contains(t, log, tt.decisions)
		})
	}
}

// TestPipelineGradeCancelled: a job stopped while the brightness is measured fails with the context, nothing is
// rendered.
func TestPipelineGradeCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	fakes := newPipelineFakes(t)
	fakes.grade, fakes.meter = true, &fakeMeter{err: context.Canceled, cancel: cancel}

	_, err := fakes.pipeline(t, t.TempDir()).Run(ctx, testJob())
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, fakes.burner.got, "not rendered")
}

// TestSectionInCutZoomsIn: a section heading read in a cut piece (T3.22: «страх номер два» cut from the sound) is put
// by the rough cut at the start of the next kept piece — its joint is where the framing moves closer.
func TestSectionInCutZoomsIn(t *testing.T) {
	t.Parallel()

	keep := []roughcut.Interval{{Start: 0, End: 3}, {Start: 5, End: 8}, {Start: 10, End: 13}, {Start: 15, End: 18}}
	notes := roughcut.RemapScreenNotes([]roughcut.ScreenNote{{Kind: roughcut.SectionNote, Text: "страх 2", Start: 9}}, keep)
	require.Len(t, notes, 1)

	segments := make([]ffmpeg.Segment, 0, len(keep))
	for _, k := range keep {
		segments = append(segments, ffmpeg.Segment{Start: k.Start, End: k.End})
	}

	cut := &ffmpeg.Cut{
		Segments: segments,
		Zoom:     &ffmpeg.Zoom{Factor: 1.3, MinSegment: minZoomSegment, Every: zoomEvery, Must: sectionStarts(notes)},
	}

	t.Logf("section at %.2f s of the reel", notes[0].Start)
	assert.Equal(t, []bool{false, false, true, true}, cut.ZoomedSegments(), "closer from the joint of the section")
}
