package mediaworker

import (
	"context"
	"fmt"
	"math"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

// The colour of a talking-head draft as the trainer's own reels (T3.25, T3.28). Her frame is darker (mean luma 98
// against 130 of our source), its top darker than its bottom (78.5 / 93.5: the light wall in shadow, so the white
// subtitles there read), and only a little warmer.
const (
	// minConfigExposure and maxConfigTop bound MEDIA_WORKER_GRADE_EXPOSURE (EV) and _GRADE_TOP (percent).
	minConfigExposure = -0.5 // the power 1.5: a stronger curve crushes the shadows
	maxConfigTop      = 60.0
	// gradeWarmth, code values of the chroma: Cb − 2, Cr + 2 — a grey about 7 % more red than blue. ±3 of T3.25 was
	// warmer than the trainer's frame (live check 3).
	gradeWarmth = 2
	// reelLevel: the exposure curve is the weakest (in steps of exposureStep, at most the configured EV) at which the
	// mean luma of the reel, with the shadow of the top, is estimated (ffmpeg.Grade.Graded) at or under this — the
	// trainer's 98; a flat grey of our frame (130) gets −0.30 EV and the shadow of 30 % as in T3.25.
	reelLevel    = 99.0
	exposureStep = 0.01
	// reelFloor: the shadow of the top never takes the estimated mean of the reel under this (the criterion is 95, one
	// code value over it for the estimate).
	reelFloor = 96.0
	// verticalAspect of the reel's frame, 9:16, and the tolerance of a source "as tall" as it.
	verticalAspect  = 9.0 / 16
	aspectTolerance = 0.02
	// topGap: the upper third of the reel is estimated this much darker than its lower third (the criterion is 10, the
	// trainer's 15; 3 code values over 10 for the estimate and the encoding). The shadow of the top grows to it from
	// MEDIA_WORKER_GRADE_TOP, to maxFittedTop at most.
	topGap = 13.0
	// maxFittedTop: the top edge of the frame loses at most this share of its light (a light wall of 150 over dark
	// clothes of 80 needs 63 %); ffmpeg takes 80 % at most.
	maxFittedTop = 0.7
	// brightSource: a source of this mean or darker keeps its exposure (only the shadow and the warmth); darkSource:
	// a source darker than this is not darkened at all (only warmed) — her frame is 98. Between them the shadow of the
	// top grows with the brightness.
	brightSource = 110.0
	darkSource   = 100.0
	// lumaBlack and lumaRange of the limited range: black 16, white 235.
	lumaBlack = 16.0
	lumaRange = 219.0
	// gradeSamples: key frames of the source measured for the brightness (a 4K key frame decodes in a fraction of a
	// second).
	gradeSamples = 6
	half         = 0.5
)

// gradeSettings are MEDIA_WORKER_GRADE, _GRADE_EXPOSURE (EV) and _GRADE_TOP (a share).
type gradeSettings struct {
	on       bool
	exposure float64
	top      float64
}

func parseGradeSettings(cfg *config.MediaWorker) (gradeSettings, error) {
	if math.IsNaN(cfg.GradeExposure) || cfg.GradeExposure < minConfigExposure || cfg.GradeExposure > 0 {
		return gradeSettings{}, fmt.Errorf("MEDIA_WORKER_GRADE_EXPOSURE %.2f must be within %.1f..0 EV: %w",
			cfg.GradeExposure, minConfigExposure, ErrInvalidConfig)
	}

	if math.IsNaN(cfg.GradeTop) || cfg.GradeTop < 0 || cfg.GradeTop > maxConfigTop {
		return gradeSettings{}, fmt.Errorf("MEDIA_WORKER_GRADE_TOP %.1f must be within 0..%.0f: %w", cfg.GradeTop,
			maxConfigTop, ErrInvalidConfig)
	}

	return gradeSettings{on: cfg.Grade, exposure: cfg.GradeExposure, top: cfg.GradeTop / percent}, nil
}

// sourceLight is the measured brightness of the source (zero: not measured) and whether its thirds are the thirds of
// the frame of the reel.
type sourceLight struct {
	luma ffmpeg.Luma
	// thirds: false when the frame pads a source wider than 9:16 with bars above and below it, or the shape of the
	// source is unknown with the pad — the shadow of the top is then the configured one, as in T3.25.
	thirds bool
	// aspect of the picture of the source as shown, width over height (0 unknown).
	aspect float64
}

// measured: the brightness of the source is known (a mean above 0, no NaN).
func (l *sourceLight) measured() bool {
	return l.luma.Mean > 0 && !math.IsNaN(l.luma.Mean+l.luma.Top+l.luma.Bottom)
}

// framedThirds: the thirds of a source of the display aspect (width over height, 0 unknown) are the thirds of the
// reel's frame of the fit. The crop fills the frame: a wider source loses its sides, a taller one a little of its top
// and bottom edges (the thirds move a little). The pad of a source as tall as 9:16 or taller adds bars at the sides;
// only a wider padded source gets bars above and below.
func framedThirds(fit ffmpeg.FitMode, aspect float64) bool {
	return fit != ffmpeg.PadFitMode || (aspect > 0 && aspect <= verticalAspect*(1+aspectTolerance))
}

// Why the shadow of the top stops short of topGap (topFor).
const (
	noTopLimit = iota
	// meanTopLimit: a stronger shadow would take the estimated mean of the reel under reelFloor.
	meanTopLimit
	// strengthTopLimit: the shadow is maxFittedTop.
	strengthTopLimit
)

// forSource is the grade of a source of the light (not measured — the grade as configured). The shadow of the top
// (topFor) is the configured one, stronger when the top of the source is light over a darker bottom, never so strong
// that the reel is estimated darker than reelFloor; the exposure curve (ffmpeg.Grade.Exposure: x → x^(1 − EV)) of a
// bright source is the weakest that, with that shadow, brings the reel to reelLevel, at most the configured EV — the
// bottom is never darker than under the configured curve. A source at brightSource or darker keeps its exposure; the
// shadow fades out from brightSource to darkSource, under which only the warmth is left.
func (g *gradeSettings) forSource(light *sourceLight) *ffmpeg.Grade {
	if !light.measured() {
		return &ffmpeg.Grade{Exposure: g.exposure, Warmth: gradeWarmth, Top: g.top}
	}

	luma := light.luma
	grade := &ffmpeg.Grade{Warmth: gradeWarmth}

	// A mean at or over the white has no curve to fit: the exposure stays, as it does in a source not brighter than
	// brightSource.
	if luma.Mean <= brightSource || luma.Mean >= lumaBlack+lumaRange {
		top, _ := g.topFor(grade, light)
		grade.Top = roundHundredths(top)

		return grade
	}

	steps := int(math.Round(-g.exposure / exposureStep))
	for i := range steps + 1 {
		grade.Exposure = roundHundredths(float64(-i) * exposureStep)
		top, _ := g.topFor(grade, light)
		grade.Top = roundHundredths(top)

		if grade.Graded(luma).Mean <= reelLevel {
			break
		}
	}

	return grade
}

// topFor is the shadow of the top under the exposure of the grade and why it stops short of topGap. It is the
// configured one (none if it is 0), stronger when without it the upper third of the reel would not be topGap darker
// than its lower third, at most maxFittedTop, faded by the brightness of the source (shadowShare); and never so strong
// that the estimated mean of the reel falls under reelFloor (the configured shadow neither). Both the upper third and
// the mean of the estimate darken in proportion to the shadow, from no shadow to the full one. A source whose thirds
// are not the frame's gets the configured shadow.
func (g *gradeSettings) topFor(grade *ffmpeg.Grade, light *sourceLight) (top float64, limit int) {
	share := shadowShare(light.luma.Mean)
	if g.top == 0 || !light.thirds {
		return share * g.top, noTopLimit
	}

	open := (&ffmpeg.Grade{Exposure: grade.Exposure}).Graded(light.luma)
	dark := (&ffmpeg.Grade{Exposure: grade.Exposure, Top: 1}).Graded(light.luma)

	need, limit := g.top, noTopLimit
	if open.Top > dark.Top {
		need = math.Max(g.top, (open.Top-(open.Bottom-topGap))/(open.Top-dark.Top))
	}

	if need > maxFittedTop {
		need, limit = maxFittedTop, strengthTopLimit
	}

	need *= share

	if open.Mean > dark.Mean {
		if floor := math.Max(0, (open.Mean-reelFloor)/(open.Mean-dark.Mean)); need > floor {
			return floor, meanTopLimit
		}
	}

	return need, limit
}

// shadowShare of the shadow of the top a source of the mean gets: none at darkSource, all of it from brightSource.
func shadowShare(mean float64) float64 {
	return math.Min(1, math.Max(0, (mean-darkSource)/(brightSource-darkSource)))
}

// roundHundredths keeps two decimals of the exposure and of the shadow: the log and the graph show the same numbers.
func roundHundredths(v float64) float64 {
	const hundredths = 100

	return math.Round(v*hundredths) / hundredths
}

// note is the line of the decisions log about the colour of the draft: what was measured in the source (the mean, the
// upper and the lower third), the grade, what it is estimated to give the reel and, when the top is not topGap darker
// than the bottom, what held the shadow back.
func (g *gradeSettings) note(grade *ffmpeg.Grade, workout bool, light *sourceLight) string {
	switch {
	case workout:
		return "Цвет кадра: как в исходнике (тренировка)"
	case grade == nil:
		return "Цвет кадра: как в исходнике (пресет выключен)"
	}

	luma := light.luma
	if light.measured() && luma.Mean < darkSource {
		return fmt.Sprintf("Цвет кадра: исходник уже тёмный (средняя яркость исходника %.0f) — без затемнения, только "+
			"тепло +%d", luma.Mean, grade.Warmth)
	}

	source := "яркость исходника не измерена — пресет как задан"
	if light.measured() {
		graded := grade.Graded(luma)
		source = fmt.Sprintf("средняя яркость исходника %.0f, верхняя/нижняя треть %.0f/%.0f; в рилсе ожидается "+
			"средняя %.0f, верх/низ %.0f/%.0f", luma.Mean, luma.Top, luma.Bottom, graded.Mean, graded.Top, graded.Bottom) +
			g.topNote(grade, light, graded)
	}

	return fmt.Sprintf("Цвет кадра: как у тренера — экспозиция %.2f EV, тепло +%d, верх кадра темнее на %.0f%% "+
		"(%s; задано %.2f EV и %.0f%%)", grade.Exposure, grade.Warmth, grade.Top*percent, source, g.exposure,
		g.top*percent)
}

// topNote says why the shadow of the top did not take the top topGap under the bottom ("" — it did, or it was not
// fitted).
func (g *gradeSettings) topNote(grade *ffmpeg.Grade, light *sourceLight, graded ffmpeg.Luma) string {
	switch {
	case !light.thirds && light.aspect <= 0:
		return "; размер кадра неизвестен — тень как задана"
	case !light.thirds:
		return "; кадр с полями сверху и снизу — тень как задана"
	}

	_, limit := g.topFor(grade, light)

	var why string

	switch limit {
	case meanTopLimit:
		why = "тень на пределе по средней"
	case strengthTopLimit:
		why = fmt.Sprintf("тень на пределе (%.0f%%)", grade.Top*percent)
	default:
		return ""
	}

	gap := graded.Bottom - graded.Top

	switch {
	case math.Abs(gap) < half:
		return "; " + why + ", верх и низ одной яркости"
	case gap > 0:
		return fmt.Sprintf("; %s, верх темнее низа на %.0f", why, gap)
	}

	return fmt.Sprintf("; %s, верх светлее низа на %.0f", why, graded.Top-graded.Bottom)
}

// lumaMeter measures the brightness of the source, its mean and its thirds (ffmpeg.Runner.MeanLuma).
type lumaMeter interface {
	MeanLuma(ctx context.Context, path string, moments []float64, toneMap bool) (ffmpeg.Luma, error)
}

// sourceLuma is the brightness of the picture of the source (the mean and the upper and the lower third) and whether
// its thirds are the frame's (framedThirds). The key frames are taken at gradeSamples moments spread evenly over the
// whole source, so every version of a draft (a revision cuts differently) gets the grade of the first one. Not
// measured (the grade as configured) without a meter, with the grade off, in a workout or when the measurement fails
// (logged: the draft is still made); only a cancelled context is an error.
func (p *Pipeline) sourceLuma(ctx context.Context, w *jobWork) (*sourceLight, error) {
	light := &sourceLight{thirds: framedThirds(p.fit, w.aspect), aspect: w.aspect}
	if p.meter == nil || !p.sound.grade.on || w.cut.Plan.Mode.Effective() != roughcut.TalkMode {
		return light, nil
	}

	started := p.now()

	luma, err := p.meter.MeanLuma(ctx, w.source, sampleMoments(w.result.SourceDuration), w.hdr)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if err != nil {
		p.log.ErrorContext(ctx, "mediaworker: failed to measure the brightness of the source, graded as configured",
			"job_id", w.job.ID, "error", err)

		return light, nil
	}

	p.log.InfoContext(ctx, "mediaworker: brightness of the source", "job_id", w.job.ID, "mean_luma", luma.Mean,
		"top_luma", luma.Top, "bottom_luma", luma.Bottom, "thirds", light.thirds,
		"seconds", roundTime(p.now().Sub(started).Seconds()))

	light.luma = luma

	return light, nil
}

// sampleMoments are gradeSamples moments in the middles of equal parts of a source of the duration, seconds.
func sampleMoments(duration float64) []float64 {
	if duration <= 0 {
		return []float64{0}
	}

	moments := make([]float64, 0, gradeSamples)
	for i := range gradeSamples {
		moments = append(moments, duration*(float64(i)+half)/gradeSamples)
	}

	return moments
}
