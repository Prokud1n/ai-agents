package mediaworker

import (
	"context"
	"fmt"
	"math"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

// The colour of a talking-head draft as the trainer's own reels (T3.25). Her frame is darker (mean luma 96 against
// 131 of ours), warmer, the light wall at the top in shadow, so the white subtitles there read.
const (
	minGradeExposure = -1.0
	maxGradeTop      = 60.0
	// gradeWarmth, code values of the chroma: Cb − 3, Cr + 3 turns a grey warmer by about 10 % of red over blue.
	gradeWarmth = 3
	// gradeLevel: the exposure brings the mean luma of the source (limited range, 8 bits) down to this, never lower
	// than the configured EV; the shadow of the top then takes the frame to about 100, the trainer's 96.
	gradeLevel = 110.0
	// darkSource: a source darker than this is not darkened more (only warmed): her frame is 96. Between it and
	// gradeLevel the shadow of the top grows with the brightness.
	darkSource = 100.0
	// lumaBlack of the limited range: the exposure scales the luma above it.
	lumaBlack = 16.0
	// gradeSamples: key frames of the cut measured for the brightness (a 4K key frame decodes in a fraction of a second).
	gradeSamples = 6
)

// gradeSettings are MEDIA_WORKER_GRADE, _GRADE_EXPOSURE (EV) and _GRADE_TOP (a share).
type gradeSettings struct {
	on       bool
	exposure float64
	top      float64
}

func parseGradeSettings(cfg *config.MediaWorker) (gradeSettings, error) {
	if math.IsNaN(cfg.GradeExposure) || cfg.GradeExposure < minGradeExposure || cfg.GradeExposure > 0 {
		return gradeSettings{}, fmt.Errorf("MEDIA_WORKER_GRADE_EXPOSURE %.2f must be within %.0f..0 EV: %w",
			cfg.GradeExposure, minGradeExposure, ErrInvalidConfig)
	}

	if math.IsNaN(cfg.GradeTop) || cfg.GradeTop < 0 || cfg.GradeTop > maxGradeTop {
		return gradeSettings{}, fmt.Errorf("MEDIA_WORKER_GRADE_TOP %.1f must be within 0..%.0f: %w", cfg.GradeTop,
			maxGradeTop, ErrInvalidConfig)
	}

	return gradeSettings{on: cfg.Grade, exposure: cfg.GradeExposure, top: cfg.GradeTop / percent}, nil
}

// forSource is the grade of a source of the mean luma (0: not measured — the grade as configured). A bright source is
// brought down to gradeLevel, at most by the configured exposure; a source at gradeLevel or darker keeps its exposure;
// the shadow of the top fades out from gradeLevel to darkSource, under which only the warmth is left.
func (g *gradeSettings) forSource(luma float64) *ffmpeg.Grade {
	if luma <= 0 {
		return &ffmpeg.Grade{Exposure: g.exposure, Warmth: gradeWarmth, Top: g.top}
	}

	gain := math.Min(1, math.Max(math.Exp2(g.exposure), (gradeLevel-lumaBlack)/(luma-lumaBlack)))
	share := math.Min(1, math.Max(0, (luma-darkSource)/(gradeLevel-darkSource)))

	return &ffmpeg.Grade{Exposure: roundEV(math.Log2(gain)), Warmth: gradeWarmth, Top: roundEV(g.top * share)}
}

// roundEV keeps two decimals: the log and the graph show the same numbers.
func roundEV(v float64) float64 {
	const hundredths = 100

	return math.Round(v*hundredths) / hundredths
}

// note is the line of the decisions log about the colour of the draft.
func (g *gradeSettings) note(grade *ffmpeg.Grade, workout bool, luma float64) string {
	switch {
	case workout:
		return "Цвет кадра: как в исходнике (тренировка)"
	case grade == nil:
		return "Цвет кадра: как в исходнике (пресет выключен)"
	}

	source := "яркость исходника не измерена — пресет как задан"
	if luma > 0 {
		source = fmt.Sprintf("средняя яркость исходника %.0f", luma)
	}

	if luma > 0 && luma < darkSource {
		return fmt.Sprintf("Цвет кадра: исходник уже тёмный (%s) — без затемнения, только тепло +%d", source,
			grade.Warmth)
	}

	return fmt.Sprintf("Цвет кадра: как у тренера — экспозиция %.2f EV, тепло +%d, верх кадра темнее на %.0f%% "+
		"(%s; задано %.2f EV и %.0f%%)", grade.Exposure, grade.Warmth, grade.Top*percent, source, g.exposure,
		g.top*percent)
}

// lumaMeter measures the mean brightness of the source (ffmpeg.Runner.MeanLuma).
type lumaMeter interface {
	MeanLuma(ctx context.Context, path string, moments []float64, toneMap bool) (float64, error)
}

// sourceLuma is the mean brightness of the picture the talking-head draft is made of: key frames in the middle of
// gradeSamples kept fragments spread over the cut. 0 (the grade as configured) without a meter, with the grade off,
// in a workout or when the measurement fails (logged: the draft is still made).
func (p *Pipeline) sourceLuma(ctx context.Context, w *jobWork) float64 {
	if p.meter == nil || !p.sound.grade.on || w.cut.Plan.Mode.Effective() != roughcut.TalkMode {
		return 0
	}

	started := p.now()

	luma, err := p.meter.MeanLuma(ctx, w.source, sampleMoments(w.cut.Segments()), w.hdr)
	if err != nil {
		p.log.ErrorContext(ctx, "mediaworker: failed to measure the brightness of the source, graded as configured",
			"job_id", w.job.ID, "error", err)

		return 0
	}

	p.log.InfoContext(ctx, "mediaworker: brightness of the source", "job_id", w.job.ID, "mean_luma", luma,
		"seconds", roundTime(p.now().Sub(started).Seconds()))

	return luma
}

// sampleMoments are the middles of at most gradeSamples segments spread evenly over the cut, seconds of the source.
func sampleMoments(segments []ffmpeg.Segment) []float64 {
	n := min(len(segments), gradeSamples)
	moments := make([]float64, 0, n)

	for i := range n {
		s := segments[i*len(segments)/n]
		moments = append(moments, (s.Start+s.End)/2)
	}

	return moments
}
