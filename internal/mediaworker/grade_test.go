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

// TestGradeForSource: a bright source is darkened to about the trainer's frame, at most by the configured exposure; a
// source already near it keeps its exposure, and a dark one is only warmed. Without a measurement the grade is as
// configured.
func TestGradeForSource(t *testing.T) {
	t.Parallel()

	settings := gradeSettings{on: true, exposure: -0.3, top: 0.3}

	tests := []struct {
		name     string
		luma     float64
		expected ffmpeg.Grade
	}{
		{name: "not measured", luma: 0, expected: ffmpeg.Grade{Exposure: -0.3, Warmth: 3, Top: 0.3}},
		// The power that takes (131 − 16) / 219 to (110 − 16) / 219 is 1.31: −0.31 EV, capped at the configured −0.3.
		{name: "a bright frame as ours", luma: 131, expected: ffmpeg.Grade{Exposure: -0.3, Warmth: 3, Top: 0.3}},
		// The power for (125 − 16) / 219 is 1.21: −0.21 EV.
		{name: "a frame a little brighter than the level", luma: 125, expected: ffmpeg.Grade{Exposure: -0.21, Warmth: 3, Top: 0.3}},
		{name: "a very bright frame: the exposure is capped", luma: 180, expected: ffmpeg.Grade{Exposure: -0.3, Warmth: 3, Top: 0.3}},
		{name: "at the level: only the shadow of the top", luma: 110, expected: ffmpeg.Grade{Warmth: 3, Top: 0.3}},
		{name: "between: half the shadow", luma: 105, expected: ffmpeg.Grade{Warmth: 3, Top: 0.15}},
		{name: "a dark frame: only the warmth", luma: 92, expected: ffmpeg.Grade{Warmth: 3}},
		{name: "under the black (superblack, noise): only the warmth", luma: 12, expected: ffmpeg.Grade{Warmth: 3}},
		{name: "over the white: no curve, the shadow of the top", luma: 240, expected: ffmpeg.Grade{Warmth: 3, Top: 0.3}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, &tt.expected, settings.forSource(tt.luma))
		})
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
			change:   func(f *pipelineFakes) { f.meter.luma = 131 },
			expected: &ffmpeg.Grade{Exposure: -0.3, Warmth: 3, Top: 0.3}, measured: true,
			decisions: "- Цвет кадра: как у тренера — экспозиция -0.30 EV, тепло +3, верх кадра темнее на 30% " +
				"(средняя яркость исходника 131; задано -0.30 EV и 30%)\n",
		},
		{
			name:     "a dark talking head",
			change:   func(f *pipelineFakes) { f.meter.luma = 90 },
			expected: &ffmpeg.Grade{Warmth: 3}, measured: true,
			decisions: "- Цвет кадра: исходник уже тёмный (средняя яркость исходника 90) — без затемнения, только тепло +3\n",
		},
		{
			name:     "the measurement failed",
			change:   func(f *pipelineFakes) { f.meter.err = errors.New("no frame") },
			expected: &ffmpeg.Grade{Exposure: -0.3, Warmth: 3, Top: 0.3}, measured: true,
			decisions: "- Цвет кадра: как у тренера — экспозиция -0.30 EV, тепло +3, верх кадра темнее на 30% " +
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
