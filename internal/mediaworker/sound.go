package mediaworker

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

// The sound and the framing of a draft (T3.15).
const (
	loudnessOff = "off"
	// The range of loudnorm's target, LUFS.
	minLoudnessTarget = -70.0
	maxLoudnessTarget = -5.0
	// draftTruePeak the limiter (and the one-pass loudnorm) keeps the peaks at: the AAC encode of the draft overshoots
	// by a few tenths of a dB, so the reel stays at most -1.5 dBTP, the limit of Instagram and the other networks.
	draftTruePeak = -2.0
	// draftLoudnessRange the one-pass loudnorm (no measurement) leaves: speech with natural dynamics fits it.
	draftLoudnessRange = 11.0
	// quietestSource: a source quieter than this (LUFS) is silence or noise — a workout filmed without sound, a muted
	// phone. Raising it to the target would make the hiss loud, so its level is kept.
	quietestSource = -50.0

	// maxZoomPercent: ffmpeg takes a zoom up to 50 %; more than 20 % reads as another shot, not a jump-cut zoom.
	maxZoomPercent = 20.0
	percent        = 100.0
	// minZoomSegment: a piece of the reel shorter than this keeps the framing of the one before it (seconds): 1.2 s
	// is a short phrase, a change of framing more often than that flickers.
	minZoomSegment = 1.2

	decisionsFileMode = 0o600
	floatBits         = 64
)

// soundSettings are the sound and the framing of the drafts.
type soundSettings struct {
	// loudness is the target, LUFS; normalize false keeps the level of the source.
	loudness  float64
	normalize bool
	denoise   bool
	// zoom of the jump-cut zoom, percent; 0 is off.
	zoom float64
}

// parseSoundSettings reads MEDIA_WORKER_LOUDNESS ("off" or LUFS), _DENOISE and _ZOOM.
func parseSoundSettings(cfg *config.MediaWorker) (soundSettings, error) {
	s := soundSettings{denoise: cfg.Denoise, zoom: cfg.Zoom}

	if cfg.Zoom < 0 || cfg.Zoom > maxZoomPercent {
		return soundSettings{}, fmt.Errorf("MEDIA_WORKER_ZOOM %.1f must be within 0..%.0f: %w", cfg.Zoom, maxZoomPercent,
			ErrInvalidConfig)
	}

	value := strings.TrimSpace(cfg.Loudness)
	if value == "" || strings.EqualFold(value, loudnessOff) {
		return s, nil
	}

	target, err := strconv.ParseFloat(value, floatBits)
	if err != nil || math.IsNaN(target) || target < minLoudnessTarget || target > maxLoudnessTarget {
		return soundSettings{}, fmt.Errorf("MEDIA_WORKER_LOUDNESS %q must be off or LUFS within %.0f..%.0f: %w",
			cfg.Loudness, minLoudnessTarget, maxLoudnessTarget, ErrInvalidConfig)
	}

	s.loudness, s.normalize = target, true

	return s, nil
}

// cut is the rough cut of the plan with the sound and the framing of the draft. The loudness is brought to the target
// in either mode (a talking head or a workout with music): with the measurement of the source the transcript has (no
// second analysis) by one gain and a peak limiter; a transcript stored before T3.15 has none — loudnorm then works in
// one pass. The noise is reduced (its floor by the loudness of the recording) and the framing changes at the joints
// only in a talking head: in a workout the noise reduction would eat into the music, and the cuts are rare and
// deliberate. A muted sound stays silent.
func (s *soundSettings) cut(plan *roughcut.Result, measured *ffmpeg.Loudness) *ffmpeg.Cut {
	opts := plan.Render
	talk := plan.Plan.Mode.Effective() == roughcut.TalkMode

	opts.Denoise, opts.Loudness = nil, nil

	if s.denoise && talk && !opts.Mute {
		opts.Denoise = &ffmpeg.Denoise{NoiseFloor: ffmpeg.NoiseFloorFor(measured)}
	}

	if s.normalize && !opts.Mute && (measured == nil || measured.Integrated >= quietestSource) {
		opts.Loudness = &ffmpeg.LoudnessTarget{
			Integrated: s.loudness, TruePeak: draftTruePeak, Range: draftLoudnessRange, Measured: measured,
		}
	}

	cut := &ffmpeg.Cut{Segments: plan.Segments(), Options: opts}
	if s.zoom > 0 && talk {
		cut.Zoom = &ffmpeg.Zoom{Factor: 1 + s.zoom/percent, MinSegment: minZoomSegment}
	}

	return cut
}

// notes are the lines of the decisions log about the sound and the framing of the rendered draft; report is what the
// render did to the loudness (nil if it did not say).
func (s *soundSettings) notes(
	cut *ffmpeg.Cut,
	measured *ffmpeg.Loudness,
	mode roughcut.Mode,
	report *ffmpeg.SoundReport,
) []string {
	workout := mode.Effective() == roughcut.WorkoutMode

	zoom := fmt.Sprintf("Зумы на склейках: %d (приближение %.0f%%)", cut.Zooms(), s.zoom)

	switch {
	case workout:
		zoom = "Зумы на склейках: нет (тренировка)"
	case cut.Zoom == nil:
		zoom = "Зумы на склейках: выключены"
	}

	return []string{s.loudnessNote(cut, measured, report), s.denoiseNote(cut, workout), zoom}
}

// loudnessNote: the loudness before, the target, how it was reached and what the render measured (T3.19b: the result
// is the measurement of the processed sound, not the target).
func (s *soundSettings) loudnessNote(cut *ffmpeg.Cut, measured *ffmpeg.Loudness, report *ffmpeg.SoundReport) string {
	l := cut.Options.Loudness

	switch {
	case cut.Options.Mute:
		return "Громкость: звук исходника убран"
	case !s.normalize:
		return "Громкость: как в исходнике (выравнивание выключено)"
	case l == nil:
		return fmt.Sprintf("Громкость: исходник %.1f LUFS — почти тишина, оставлена как есть", measured.Integrated)
	case measured == nil:
		return fmt.Sprintf("Громкость: исходник не измерен, цель %.1f LUFS: loudnorm без измерения (в один проход), "+
			"пики до %.1f dBTP; %s", l.Integrated, l.TruePeak, loudnessResult(report))
	}

	gain, correction := l.Gain(), ""

	if report != nil {
		gain = report.Gain

		switch {
		case report.FitError != nil:
			correction = " (без поправки: предварительный замер склейки не удался)"
		case report.CorrectionCapped:
			correction = fmt.Sprintf(" (поправка %+.1f dB по предварительному замеру склейки упёрлась в предел — "+
				"громкость может не попасть в цель; проходов: %d)", report.Correction, report.Passes)
		case report.Passes > 0:
			correction = fmt.Sprintf(" (поправка %+.1f dB по предварительному замеру склейки, проходов: %d)",
				report.Correction, report.Passes)
		}
	}

	return fmt.Sprintf("Громкость: исходник %.1f LUFS (пики %.1f dBTP), цель %.1f LUFS: усиление %+.1f dB%s, "+
		"лимитер пиков на %.1f dBTP; %s", measured.Integrated, measured.TruePeak, l.Integrated, gain, correction,
		l.TruePeak, loudnessResult(report))
}

// loudnessResult is the loudness of the processed sound as the render measured it.
func loudnessResult(report *ffmpeg.SoundReport) string {
	if report == nil || report.Output == nil {
		return "итог не измерен"
	}

	result := fmt.Sprintf("итог %.1f LUFS, true peak %.1f dBTP (замер готового файла, после AAC)",
		report.Output.Integrated, report.Output.TruePeak)
	if report.PeakCut > 0 {
		result += fmt.Sprintf("; кодек поднял пики, звук пересобран с потолком лимитера ниже на %.1f dB",
			report.PeakCut)
	}

	if report.PeakError != nil {
		result += "; снизить пики после кодека не удалось, файл оставлен как есть"
	}

	return result
}

func (s *soundSettings) denoiseNote(cut *ffmpeg.Cut, workout bool) string {
	switch {
	case cut.Options.Mute:
		return "Шумодав: нет (звук убран)"
	case workout && s.denoise:
		return "Шумодав: нет (тренировка, музыка)"
	case cut.Options.Denoise == nil:
		return "Шумодав: выключен"
	default:
		return fmt.Sprintf("Шумодав: лёгкий (речь), порог шума %.0f dBFS", cut.Options.Denoise.NoiseFloor)
	}
}

// appendDecisions adds the notes to the decisions log of the plan.
func appendDecisions(path string, notes []string) error {
	f, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_APPEND, decisionsFileMode)
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", filepath.Base(path), err)
	}

	var b strings.Builder

	b.WriteString("\nЗвук и кадр:\n")

	for _, note := range notes {
		b.WriteString("- " + note + "\n")
	}

	_, err = f.WriteString(b.String())
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		return fmt.Errorf("failed to append to %s: %w", filepath.Base(path), err)
	}

	return nil
}
