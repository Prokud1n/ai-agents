package mediaworker

import (
	"fmt"
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
	// draftTruePeak: Instagram and the other networks encode the sound again (AAC); 1.5 dB under full scale leaves room
	// for the overshoot of that encode.
	draftTruePeak = -1.5
	// draftLoudnessRange: speech with natural dynamics fits it, so with a measurement loudnorm mostly changes only the
	// volume (its linear mode); a wider sound is compressed to it.
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
	if err != nil || target < minLoudnessTarget || target > maxLoudnessTarget {
		return soundSettings{}, fmt.Errorf("MEDIA_WORKER_LOUDNESS %q must be off or LUFS within %.0f..%.0f: %w",
			cfg.Loudness, minLoudnessTarget, maxLoudnessTarget, ErrInvalidConfig)
	}

	s.loudness, s.normalize = target, true

	return s, nil
}

// cut is the rough cut of the plan with the sound and the framing of the draft. The loudness is brought to the target
// in either mode (a talking head or a workout with music), by the measurement of the source when the transcript has
// one (no second analysis; a transcript stored before T3.15 has none — loudnorm then works in one pass). The noise is
// reduced and the framing changes at the joints only in a talking head: in a workout the noise reduction would eat into
// the music, and the cuts are rare and deliberate. A muted sound stays silent.
func (s *soundSettings) cut(plan *roughcut.Result, measured *ffmpeg.Loudness) *ffmpeg.Cut {
	opts := plan.Render
	talk := plan.Plan.Mode.Effective() == roughcut.TalkMode

	opts.Denoise = s.denoise && talk && !opts.Mute
	opts.Loudness = nil

	if s.normalize && !opts.Mute && (measured == nil || measured.Integrated >= quietestSource) {
		opts.Loudness = &ffmpeg.Loudnorm{
			Integrated: s.loudness, TruePeak: draftTruePeak, Range: draftLoudnessRange, Measured: measured,
		}
	}

	cut := &ffmpeg.Cut{Segments: plan.Segments(), Options: opts}
	if s.zoom > 0 && talk {
		cut.Zoom = &ffmpeg.Zoom{Factor: 1 + s.zoom/percent, MinSegment: minZoomSegment}
	}

	return cut
}

// notes are the lines of the decisions log about the sound and the framing of the rendered draft.
func (s *soundSettings) notes(cut *ffmpeg.Cut, measured *ffmpeg.Loudness, mode roughcut.Mode) []string {
	workout := mode.Effective() == roughcut.WorkoutMode
	l := cut.Options.Loudness

	var loudness string

	switch {
	case cut.Options.Mute:
		loudness = "Громкость: звук исходника убран"
	case !s.normalize:
		loudness = "Громкость: как в исходнике (выравнивание выключено)"
	case l == nil:
		loudness = fmt.Sprintf("Громкость: исходник %.1f LUFS — почти тишина, оставлена как есть", measured.Integrated)
	case measured == nil:
		loudness = fmt.Sprintf("Громкость: исходник не измерен → %.1f LUFS, пики до %.1f dBTP", l.Integrated, l.TruePeak)
	default:
		loudness = fmt.Sprintf("Громкость: исходник %.1f LUFS (пики %.1f dBTP) → %.1f LUFS, пики до %.1f dBTP",
			measured.Integrated, measured.TruePeak, l.Integrated, l.TruePeak)
	}

	denoise := "Шумодав: лёгкий (речь)"

	switch {
	case cut.Options.Mute:
		denoise = "Шумодав: нет (звук убран)"
	case workout && s.denoise:
		denoise = "Шумодав: нет (тренировка, музыка)"
	case !cut.Options.Denoise:
		denoise = "Шумодав: выключен"
	}

	zoom := fmt.Sprintf("Зумы на склейках: %d (приближение %.0f%%)", cut.Zooms(), s.zoom)

	switch {
	case workout:
		zoom = "Зумы на склейках: нет (тренировка)"
	case cut.Zoom == nil:
		zoom = "Зумы на склейках: выключены"
	}

	return []string{loudness, denoise, zoom}
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
