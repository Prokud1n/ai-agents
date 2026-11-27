package roughcut

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
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

	// MaxSegments limits the fragments of one render: each is a branch of the ffmpeg filter graph. The editor's
	// edits are checked against it before they reach the media worker.
	MaxSegments = 500
	// maxSourceFrameRate caps the rate read from the file: a 120 fps slow-motion clip counts as 60 fps.
	maxSourceFrameRate = 60
	// MaxFrameRate caps the constant frame rate of the draft: a faster source drops every second (third…) frame,
	// 60 → 30, 59.94 → 29.97, 50 → 25. Half the frames are tone mapped, drawn and encoded: the render is about twice as
	// fast, and a talking head or a workout does not need more for a phone screen.
	MaxFrameRate = 30
	// minDividedFrameRate: a rate that only divides below it (40 → 20) is converted to MaxFrameRate instead.
	minDividedFrameRate = 24
	frameEpsilon        = 1e-6

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
	AudioLevels(ctx context.Context, path string) (*ffmpeg.Levels, error)
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
// Render is how the plan is rendered: with PlanOnly the caller renders it itself in one pass with the subtitles
// (Segments and Render go to ffmpeg as they are).
// Titles are the exercise titles of the edit on the timeline of the cut (RemapTitles); Render.Mute silences the
// source (Edit.Mute). ScreenNotes are the points and numbers of a talking head on the timeline of the cut
// (RemapScreenNotes).
type Result struct {
	Plan        Plan
	Transcript  transcription.Result
	Log         string
	Files       Files
	Render      ffmpeg.RenderOptions
	Titles      []Title
	ScreenNotes []ScreenNote
}

// Segments are the kept fragments of the plan for ffmpeg.
func (r *Result) Segments() []ffmpeg.Segment {
	return planSegments(&r.Plan)
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
	levels, soundLevels := s.levels(ctx, data)

	plan, err := s.plan(data, sourceDuration(&info), &cutGrid{levels: levels, fps: fps})
	if err != nil {
		return nil, err
	}

	plan = SnapToFrames(&plan, fps)
	plan.MinSpeechShare = data.MinSpeechShare
	plan.SoundLevels = soundLevels
	if err := checkSegments(&plan, fps); err != nil {
		return nil, err
	}

	source := data.Transcript
	if plan.SoundCuts {
		source = trimSilentEdges(source, plan.Keep, levels)
	}

	transcript := SubtitleTranscript(source, plan.Keep, plan.Edit)
	plan.LongPauses = countLongPauses(&plan, levels, transcript.Words)

	s.logPlan(ctx, &plan, data.Plan != nil)

	result := &Result{
		Plan: plan, Transcript: transcript, Log: DecisionsLog(&plan),
		Render: ffmpeg.RenderOptions{
			Fade: junctionFade, FrameRate: rate, Offset: audioOffset(&info), Mute: plan.Edit != nil && plan.Edit.Mute,
			Source: info.Frame(),
		},
		Titles:      RemapTitles(editTitles(plan.Edit), plan.Keep),
		ScreenNotes: RemapScreenNotes(editScreenNotes(plan.Edit), plan.Keep),
	}

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

	result.Files.Video, err = s.render(ctx, data.Source, data.OutDir, &plan, result.Render)
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

// targetFrameRate is the constant frame rate of the result, kept as a rational (30000/1001): the rate of the source
// (sourceFrameRate) capped at MaxFrameRate (capFrameRate).
func targetFrameRate(info *ffmpeg.MediaInfo) ffmpeg.Rational {
	return capFrameRate(sourceFrameRate(info))
}

// sourceFrameRate is the nominal rate of the source (r_frame_rate; a variable frame rate phone clip is converted to
// it), the average one if the nominal is unknown or above maxSourceFrameRate, and maxSourceFrameRate for slow-motion
// clips.
func sourceFrameRate(info *ffmpeg.MediaInfo) ffmpeg.Rational {
	for _, rate := range []ffmpeg.Rational{info.RealFrameRate, info.AvgFrameRate} {
		if f := rate.Float(); f > 0 && f <= maxSourceFrameRate {
			return rate
		}
	}

	if info.RealFrameRate.Float() > 0 || info.AvgFrameRate.Float() > 0 {
		return ffmpeg.Rational{Num: maxSourceFrameRate, Den: 1}
	}

	return ffmpeg.Rational{}
}

// capFrameRate divides a rate above MaxFrameRate by the smallest whole number (at least 2) that brings it under the
// cap, so every kept frame is a frame of the source and the motion stays even: 60 → 30, 60000/1001 → 30000/1001,
// 50 → 25, 120 → 30. A rate that would fall below minDividedFrameRate (40 → 20) becomes MaxFrameRate. Lower rates are
// kept, reduced (60/2 → 30/1), so the same rate is always written the same way.
func capFrameRate(rate ffmpeg.Rational) ffmpeg.Rational {
	f := rate.Float()
	if f <= MaxFrameRate {
		return reduceRational(rate)
	}

	divisor := max(2, int64(math.Ceil(f/MaxFrameRate-frameEpsilon)))

	capped := reduceRational(ffmpeg.Rational{Num: rate.Num, Den: rate.Den * divisor})
	if capped.Float() < minDividedFrameRate {
		return ffmpeg.Rational{Num: MaxFrameRate, Den: 1}
	}

	return capped
}

// reduceRational divides the numerator and the denominator by their greatest common divisor; unknown stays as is.
func reduceRational(r ffmpeg.Rational) ffmpeg.Rational {
	if r.Num <= 0 || r.Den <= 0 {
		return r
	}

	a, b := r.Num, r.Den
	for b != 0 {
		a, b = b, a%b
	}

	if a <= 1 {
		return r
	}

	return ffmpeg.Rational{Num: r.Num / a, Den: r.Den / a}
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

// checkSegments limits the render: at most MaxSegments fragments, each at least one frame long.
func checkSegments(p *Plan, fps float64) error {
	if len(p.Keep) > MaxSegments {
		return fmt.Errorf("failed to check plan: %d fragments, at most %d (raise -max-pause): %w",
			len(p.Keep), MaxSegments, ErrInvalidPlan)
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

// levels reads the sound level of the source for the cut points of a talking head, with its summary for the decisions
// log. The speech levels are nil (the margins around the words) if they cannot be read or tell speech from silence
// nothing; both are nil, and nothing is read, for a given plan (its cut points are set) or a workout (no pause is cut).
func (s *Service) levels(ctx context.Context, data *CutData) (*speechLevels, *SoundLevels) {
	if data.Plan != nil || (data.Edit != nil && data.Edit.Mode.Effective() == WorkoutMode) {
		return nil, nil
	}

	levels, err := s.media.AudioLevels(ctx, data.Source)
	if err != nil {
		s.log.WarnContext(ctx, "roughcut: no sound levels, cutting by the margins around the words", "error", err)

		return nil, nil
	}

	summary := soundLevelsOf(levels)
	if summary != nil {
		s.log.InfoContext(ctx, "roughcut: sound levels", "noise_floor", summary.NoiseFloor, "speech", summary.Speech,
			"threshold", summary.Threshold, "used", summary.Used)
	}

	return newSpeechLevels(levels), summary
}

// countLongPauses counts the pauses longer than longPause inside the reel: by the sound level, or by the gaps between
// the words of the reel without it.
func countLongPauses(p *Plan, levels *speechLevels, reelWords []transcription.Word) int {
	if levels != nil {
		return levels.longPauses(p.Keep)
	}

	count := 0

	for i := 1; i < len(reelWords); i++ {
		if reelWords[i].Start-reelWords[i-1].End > longPause {
			count++
		}
	}

	return count
}

func (s *Service) plan(data *CutData, duration float64, grid *cutGrid) (Plan, error) {
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

	edit := &Edit{Accept: data.AcceptCandidates}
	if data.Edit != nil {
		edit = cloneEdit(data.Edit)
		edit.Accept = append(edit.Accept, data.AcceptCandidates...)
	}

	if grid.levels == nil {
		grid = nil
	}

	plan, err := buildPlan(data.Transcript, duration, settings, edit, grid)
	if err != nil {
		return Plan{}, err
	}

	plan.Source = filepath.Base(data.Source)

	if data.Edit != nil {
		plan.Edit = edit
	}

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

	tmpVideo := filepath.Join(tmp, VideoFile)

	if err := s.media.RenderSegments(ctx, src, tmpVideo, planSegments(plan), opts); err != nil {
		return "", fmt.Errorf("failed to render video: %w", err)
	}

	video := filepath.Join(dir, VideoFile)
	if err := os.Rename(tmpVideo, video); err != nil {
		return "", fmt.Errorf("failed to save video: %w", err)
	}

	return video, nil
}

// editTitles are the titles of the edit, nil without one.
func editTitles(edit *Edit) []Title {
	if edit == nil {
		return nil
	}

	return edit.Titles
}

func planSegments(plan *Plan) []ffmpeg.Segment {
	segments := make([]ffmpeg.Segment, 0, len(plan.Keep))
	for _, iv := range plan.Keep {
		segments = append(segments, ffmpeg.Segment{Start: iv.Start, End: iv.End})
	}

	return segments
}
