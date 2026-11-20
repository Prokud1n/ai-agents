package roughcut

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// Files written to the output directory.
const (
	PlanFile       = "plan.json"
	TranscriptFile = "transcript.json"
	LogFile        = "decisions.log"
	VideoFile      = "roughcut.mp4"

	tempDirPattern = ".roughcut-*"
	outputDirMode  = 0o750

	// maxSegments limits the fragments of one render: each is a branch of the ffmpeg filter graph.
	maxSegments = 500
	// maxFrameRate caps the constant frame rate of the result: a 120 fps slow-motion clip becomes 60 fps.
	maxFrameRate = 60
	frameEpsilon = 1e-6

	// junctionFade is the audio fade in and out of every kept fragment, against clicks at the joints.
	junctionFade = 0.01
	// An edited plan may end a little after the probed duration: containers round it differently.
	durationTolerance = 0.05
)

var (
	ErrEmptySource     = errors.New("source video is not set")
	ErrEmptyTranscript = errors.New("transcript is not set")
	ErrEmptyOutDir     = errors.New("output directory is not set")
	ErrInvalidPlan     = errors.New("invalid plan")
	ErrInvalidSettings = errors.New("invalid rough cut settings")
	// ErrNoAudio: a video without sound has no speech to cut by; it is refused before anything is written.
	ErrNoAudio     = errors.New("source has no audio stream")
	ErrNoVideo     = errors.New("source has no video stream")
	ErrNoFrameRate = errors.New("source frame rate is unknown")
)

type mediaProcessor interface {
	Probe(ctx context.Context, path string) (ffmpeg.MediaInfo, error)
	RenderSegments(ctx context.Context, src, dst string, segments []ffmpeg.Segment, opts ffmpeg.RenderOptions) error
}

// Service makes a rough cut of a talking-head video from its transcript: it plans what to keep
// (BuildPlan), renders the kept fragments with ffmpeg and moves the transcript onto the new timeline.
type Service struct {
	media    mediaProcessor
	defaults Settings
	log      *slog.Logger
}

// Result is what Cut produced; Files has the paths of the written files (Video is empty in plan-only mode).
type Result struct {
	Plan       Plan
	Transcript transcription.Result
	Log        string
	Files      Files
}

type Files struct {
	Plan       string
	Transcript string
	Log        string
	Video      string
}

// NewService fails on invalid ROUGHCUT_* values, so a misconfigured command stops before any work.
func NewService(media mediaProcessor, cfg *config.RoughCut, log *slog.Logger) (*Service, error) {
	defaults := Settings{
		MaxPause:         cfg.MaxPause.Seconds(),
		PadBefore:        defaultPadBefore,
		PadAfter:         defaultPadAfter,
		RetakeSimilarity: cfg.RetakeSimilarity,
		Fillers:          append([]string(nil), cfg.Fillers...),
	}

	if err := ValidateSettings(&defaults); err != nil {
		return nil, fmt.Errorf("failed to configure rough cut (ROUGHCUT_*): %w", err)
	}

	return &Service{media: media, defaults: defaults, log: log}, nil
}

// ValidateSettings checks the thresholds: a positive pause and a similarity within (0, 1].
func ValidateSettings(s *Settings) error {
	switch {
	case s.MaxPause <= 0:
		return fmt.Errorf("max pause %.3f s must be positive: %w", s.MaxPause, ErrInvalidSettings)
	case s.RetakeSimilarity <= 0 || s.RetakeSimilarity > 1:
		return fmt.Errorf("retake similarity %.2f must be within (0, 1]: %w", s.RetakeSimilarity, ErrInvalidSettings)
	}

	return nil
}

// DefaultSettings returns the configured settings; callers override fields and pass them in CutData.
func (s *Service) DefaultSettings() Settings {
	out := s.defaults
	out.Fillers = append([]string(nil), s.defaults.Fillers...)

	return out
}

// Cut plans the rough cut (or takes data.Plan), writes the plan, the remapped transcript and the decisions
// log to data.OutDir and, unless data.PlanOnly, renders the video there. A roughcut.mp4 left by an earlier
// run is removed first, so it never sits next to a plan it was not made from.
func (s *Service) Cut(ctx context.Context, data *CutData) (*Result, error) {
	if err := validateCutData(data); err != nil {
		return nil, err
	}

	info, rate, err := s.probe(ctx, data.Source)
	if err != nil {
		return nil, err
	}

	fps := rate.Float()

	plan, err := s.plan(data, sourceDuration(&info))
	if err != nil {
		return nil, err
	}

	plan = SnapToFrames(&plan, fps)
	if err := checkSegments(&plan, fps); err != nil {
		return nil, err
	}

	s.logPlan(ctx, &plan, data.Plan != nil)

	result := &Result{Plan: plan, Transcript: RemapTranscript(data.Transcript, plan.Keep), Log: DecisionsLog(&plan)}

	if err := prepareOutDir(data.OutDir); err != nil {
		return nil, err
	}

	result.Files, err = writeOutputs(data.OutDir, result)
	if err != nil {
		return nil, err
	}

	if data.PlanOnly {
		return result, nil
	}

	result.Files.Video, err = s.render(ctx, data.Source, data.OutDir, &plan, ffmpeg.RenderOptions{
		Fade: junctionFade, FrameRate: rate, Offset: audioOffset(&info),
	})
	if err != nil {
		return nil, err
	}

	s.log.InfoContext(ctx, "roughcut: rendered", "file", result.Files.Video, "duration", plan.Duration)

	return result, nil
}

// probe checks the source before anything is written: it needs sound to cut by and a picture to render.
// It returns the constant frame rate of the result (see targetFrameRate).
func (s *Service) probe(ctx context.Context, src string) (ffmpeg.MediaInfo, ffmpeg.Rational, error) {
	info, err := s.media.Probe(ctx, src)
	if err != nil {
		return ffmpeg.MediaInfo{}, ffmpeg.Rational{}, fmt.Errorf("failed to read source: %w", err)
	}

	switch {
	case !info.HasAudio:
		return ffmpeg.MediaInfo{}, ffmpeg.Rational{}, fmt.Errorf("failed to read source: %w", ErrNoAudio)
	case !info.HasVideo:
		return ffmpeg.MediaInfo{}, ffmpeg.Rational{}, fmt.Errorf("failed to read source: %w", ErrNoVideo)
	}

	rate := targetFrameRate(&info)
	if rate.Float() <= 0 {
		return ffmpeg.MediaInfo{}, ffmpeg.Rational{}, fmt.Errorf("failed to read source: %w", ErrNoFrameRate)
	}

	s.log.InfoContext(ctx, "roughcut: source probed", "duration", info.Duration, "frame_rate", rate.String(),
		"avg_frame_rate", info.AvgFrameRate.String(), "r_frame_rate", info.RealFrameRate.String(),
		"format_start", info.FormatStart, "audio_start", info.AudioStart, "video_start", info.VideoStart)

	return info, rate, nil
}

// targetFrameRate is the constant frame rate of the result, kept as a rational (30000/1001): the nominal
// rate of the source (r_frame_rate; a variable frame rate phone clip is converted to it), the average one
// if the nominal is unknown or above maxFrameRate, and maxFrameRate for slow-motion clips.
func targetFrameRate(info *ffmpeg.MediaInfo) ffmpeg.Rational {
	for _, rate := range []ffmpeg.Rational{info.RealFrameRate, info.AvgFrameRate} {
		if f := rate.Float(); f > 0 && f <= maxFrameRate {
			return rate
		}
	}

	if info.RealFrameRate.Float() > 0 || info.AvgFrameRate.Float() > 0 {
		return ffmpeg.Rational{Num: maxFrameRate, Den: 1}
	}

	return ffmpeg.Rational{}
}

// audioOffset is where the first audio sample is in the filter graph: ffmpeg has already subtracted the
// start_time of the file from every stream.
func audioOffset(info *ffmpeg.MediaInfo) float64 {
	return info.AudioStart - info.FormatStart
}

// sourceDuration is the length of the source on the transcript's timeline, which starts at the first
// audio sample: if the audio starts later than the file, the file is that much shorter for it.
func sourceDuration(info *ffmpeg.MediaInfo) float64 {
	return info.Duration - max(audioOffset(info), 0)
}

// checkSegments limits the render: at most maxSegments fragments, each at least one frame long.
func checkSegments(p *Plan, fps float64) error {
	if len(p.Keep) > maxSegments {
		return fmt.Errorf("failed to check plan: %d fragments, at most %d (raise -max-pause): %w",
			len(p.Keep), maxSegments, ErrInvalidPlan)
	}

	if len(p.Keep) == 0 {
		return fmt.Errorf("failed to check plan: no fragment is a frame long: %w", ErrInvalidPlan)
	}

	for i, iv := range p.Keep {
		if iv.End-iv.Start < 1/fps-frameEpsilon {
			return fmt.Errorf("failed to check plan: keep[%d] %.3f–%.3f is shorter than a frame: %w",
				i, iv.Start, iv.End, ErrInvalidPlan)
		}
	}

	return nil
}

// prepareOutDir creates the output directory and removes the video of an earlier run.
func prepareOutDir(dir string) error {
	if err := os.MkdirAll(dir, outputDirMode); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	if err := os.Remove(filepath.Join(dir, VideoFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to remove the old video: %w", err)
	}

	return nil
}

func validateCutData(data *CutData) error {
	switch {
	case data.Source == "":
		return fmt.Errorf("failed to cut: %w", ErrEmptySource)
	case data.Transcript == nil:
		return fmt.Errorf("failed to cut: %w", ErrEmptyTranscript)
	case data.OutDir == "":
		return fmt.Errorf("failed to cut: %w", ErrEmptyOutDir)
	}

	return nil
}

func (s *Service) plan(data *CutData, duration float64) (Plan, error) {
	if data.Plan != nil {
		plan, err := checkPlan(data.Plan, duration)
		if err != nil {
			return Plan{}, err
		}

		plan.Source = filepath.Base(data.Source)

		return plan, nil
	}

	settings := s.DefaultSettings()
	if data.Settings != nil {
		settings = *data.Settings
	}

	if err := ValidateSettings(&settings); err != nil {
		return Plan{}, fmt.Errorf("failed to plan: %w", err)
	}

	plan, err := BuildPlanAccepting(data.Transcript, duration, settings, data.AcceptCandidates)
	if err != nil {
		return Plan{}, err
	}

	plan.Source = filepath.Base(data.Source)

	if len(plan.Keep) == 0 {
		return Plan{}, fmt.Errorf("failed to plan: no words to keep in the transcript: %w", ErrInvalidPlan)
	}

	return plan, nil
}

// checkPlan validates an edited plan against the source and returns a copy with the duration recounted.
func checkPlan(p *Plan, duration float64) (Plan, error) {
	if p.Version != PlanVersion {
		return Plan{}, fmt.Errorf("failed to check plan: version %d, expected %d: %w", p.Version, PlanVersion, ErrInvalidPlan)
	}

	if len(p.Keep) == 0 {
		return Plan{}, fmt.Errorf("failed to check plan: keep is empty: %w", ErrInvalidPlan)
	}

	prevEnd := 0.0

	for i, iv := range p.Keep {
		if iv.Start < prevEnd || iv.End <= iv.Start || iv.End > duration+durationTolerance {
			return Plan{}, fmt.Errorf(
				"failed to check plan: keep[%d] %.3f–%.3f must be increasing, non-overlapping and within %.3f s: %w",
				i, iv.Start, iv.End, duration, ErrInvalidPlan)
		}

		prevEnd = iv.End
	}

	out := *p
	out.Keep = append([]Interval(nil), p.Keep...)
	out.SourceDuration = roundTime(duration)
	out.Duration = totalDuration(out.Keep)

	return out, nil
}

func (s *Service) logPlan(ctx context.Context, p *Plan, edited bool) {
	for _, t := range p.Takes {
		s.log.InfoContext(ctx, "roughcut: retake removed",
			"kind", t.Kind, "start", t.Removed.Start, "end", t.Removed.End, "kept_start", t.Kept.Start,
			"kept_end", t.Kept.End, "similarity", t.Similarity, "explanation", t.Explanation)
	}

	s.log.InfoContext(ctx, "roughcut: planned", "edited_plan", edited, "source_duration", p.SourceDuration,
		"duration", p.Duration, "fragments", len(p.Keep), "cuts", len(p.Cuts), "retakes", len(p.Takes))
}

func writeOutputs(dir string, r *Result) (Files, error) {
	files := Files{
		Plan:       filepath.Join(dir, PlanFile),
		Transcript: filepath.Join(dir, TranscriptFile),
		Log:        filepath.Join(dir, LogFile),
	}

	if err := WriteJSON(files.Plan, r.Plan); err != nil {
		return Files{}, err
	}

	if err := WriteJSON(files.Transcript, r.Transcript); err != nil {
		return Files{}, err
	}

	if err := writeFile(files.Log, []byte(r.Log)); err != nil {
		return Files{}, err
	}

	return files, nil
}

// WriteJSON writes v as indented JSON readable by a person.
func WriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode %s: %w", filepath.Base(path), err)
	}

	return writeFile(path, append(data, '\n'))
}

// writeFile creates a result file with the default permissions (0644 under the usual umask): the files are
// read later by the app running as another user.
func writeFile(path string, data []byte) error {
	f, err := os.Create(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", filepath.Base(path), err)
	}

	if _, err := f.Write(data); err != nil {
		_ = f.Close()

		return fmt.Errorf("failed to write %s: %w", filepath.Base(path), err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to write %s: %w", filepath.Base(path), err)
	}

	return nil
}

// render writes the video into a temporary directory next to the result and renames it, so a failed or
// interrupted render never leaves a half-written roughcut.mp4. ffmpeg creates the file, so it gets the
// default permissions (0644 under the usual umask).
func (s *Service) render(ctx context.Context, src, dir string, plan *Plan, opts ffmpeg.RenderOptions) (string, error) {
	tmp, err := os.MkdirTemp(dir, tempDirPattern)
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir: %w", err)
	}

	defer func() {
		if err := os.RemoveAll(tmp); err != nil {
			s.log.ErrorContext(ctx, "roughcut: failed to remove temp dir", "error", err, "dir", tmp)
		}
	}()

	segments := make([]ffmpeg.Segment, 0, len(plan.Keep))
	for _, iv := range plan.Keep {
		segments = append(segments, ffmpeg.Segment{Start: iv.Start, End: iv.End})
	}

	tmpVideo := filepath.Join(tmp, VideoFile)

	if err := s.media.RenderSegments(ctx, src, tmpVideo, segments, opts); err != nil {
		return "", fmt.Errorf("failed to render video: %w", err)
	}

	video := filepath.Join(dir, VideoFile)
	if err := os.Rename(tmpVideo, video); err != nil {
		return "", fmt.Errorf("failed to save video: %w", err)
	}

	return video, nil
}
