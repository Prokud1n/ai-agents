package mediaworker

import (
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
		// (110 − 16) / (131 − 16) = 0.817: −0.29 EV, within the configured −0.3.
		{name: "a bright frame as ours", luma: 131, expected: ffmpeg.Grade{Exposure: -0.29, Warmth: 3, Top: 0.3}},
		{name: "a very bright frame: the exposure is capped", luma: 180, expected: ffmpeg.Grade{Exposure: -0.3, Warmth: 3, Top: 0.3}},
		{name: "at the level: only the shadow of the top", luma: 110, expected: ffmpeg.Grade{Warmth: 3, Top: 0.3}},
		{name: "between: half the shadow", luma: 105, expected: ffmpeg.Grade{Warmth: 3, Top: 0.15}},
		{name: "a dark frame: only the warmth", luma: 92, expected: ffmpeg.Grade{Warmth: 3}},
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

	segments := make([]ffmpeg.Segment, 0, 12)
	for i := range 12 {
		segments = append(segments, ffmpeg.Segment{Start: float64(10 * i), End: float64(10*i + 2)})
	}

	assert.Equal(t, []float64{1, 21, 41, 61, 81, 101}, sampleMoments(segments), "6 of 12, spread over the cut")
	assert.Equal(t, []float64{1, 11}, sampleMoments(segments[:2]))
	assert.Empty(t, sampleMoments(nil))
}

// TestPipelineGrade: a talking head is graded by the measured brightness of its cut (key frames in the middle of its
// fragments, tone mapped for an HDR source), the zoom changes the framing at the start of each section; a workout is
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
			expected: &ffmpeg.Grade{Exposure: -0.29, Warmth: 3, Top: 0.3}, measured: true,
			decisions: "- Цвет кадра: как у тренера — экспозиция -0.29 EV, тепло +3, верх кадра темнее на 30% " +
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
				assert.Equal(t, []float64{0.75, 3.25}, fakes.meter.moments, "the middles of the kept fragments")
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
