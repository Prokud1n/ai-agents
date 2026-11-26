package mediaworker

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/providers/whisper"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/subtitles"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// Transfer functions as ffprobe names them.
const (
	testHLG = "arib-std-b67"
	testSDR = "bt709"
)

var (
	testDeadline = time.Date(2026, 11, 23, 12, 45, 0, 0, time.UTC)
	errS3Down    = errors.New("s3 is down")
)

type pipelineFakes struct {
	store       *localStore
	prober      *fakeProber
	keys        fakeKeys
	transcriber *fakeTranscriber
	cutter      *fakeCutter
	burner      *fakeBurner
	// minSpeechShare of the pipeline; 0 turns the check of little speech off.
	minSpeechShare float64
	// loudness, denoise and zoom: MEDIA_WORKER_LOUDNESS, _DENOISE and _ZOOM; off by default.
	loudness string
	denoise  bool
	zoom     float64
}

// newPipelineFakes: a 3-minute source with a picture and sound, two words, a plan that cuts it to 2:21 with nothing
// to decide.
func newPipelineFakes(t *testing.T) *pipelineFakes {
	t.Helper()

	source := filepath.Join(t.TempDir(), "source.mov")
	require.NoError(t, os.WriteFile(source, []byte("source-video"), 0o600))

	words := []transcription.Word{{Word: "привет", Start: 0.5, End: 1}, {Word: "это", Start: 3, End: 3.5}}

	return &pipelineFakes{
		store:       newLocalStore(map[string]string{testSourceKey: source}),
		prober:      &fakeProber{info: ffmpeg.MediaInfo{Duration: 182, HasVideo: true, HasAudio: true}},
		transcriber: &fakeTranscriber{result: &transcription.Result{Language: testLanguage, Duration: 182, Words: words}},
		cutter: &fakeCutter{result: &roughcut.Result{
			Plan: roughcut.Plan{
				Duration: 141, Keep: []roughcut.Interval{{Start: 0.5, End: 1}, {Start: 3, End: 3.5}},
				Cuts: make([]roughcut.Cut, 3), Candidates: []roughcut.Candidate{},
			},
			Transcript: transcription.Result{Words: words},
			Render:     ffmpeg.RenderOptions{Fade: 0.01, FrameRate: ffmpeg.Rational{Num: 30, Den: 1}},
		}},
		burner: &fakeBurner{result: &subtitles.BurnResult{Cues: 1}},
	}
}

func (f *pipelineFakes) pipeline(t *testing.T, workDir string) *Pipeline {
	t.Helper()

	p, err := NewPipeline(&PipelineParams{
		Store: f.store, Prober: f.prober, Keys: f.keys, Transcriber: f.transcriber, Cutter: f.cutter, Burner: f.burner,
		Config: &config.MediaWorker{
			WorkDir: workDir, MaxSourceDuration: 10 * time.Minute, MaxSourceSize: 1 << 20, Fit: string(ffmpeg.PadFitMode),
			MinSpeechShare: f.minSpeechShare, Loudness: f.loudness, Denoise: f.denoise, Zoom: f.zoom,
		},
		Log: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)

	return p
}

func testJob() *editjob.Job {
	return &editjob.Job{
		ID: editjob.JobID(17, 1), TaskID: 17, Version: 1, SourceKey: testSourceKey, QueueDeadline: testDeadline,
		Timeout: time.Hour,
	}
}

func TestPipelineRun(t *testing.T) {
	t.Parallel()

	fakes := newPipelineFakes(t)
	workDir := t.TempDir()

	result, err := fakes.pipeline(t, workDir).Run(t.Context(), testJob())
	require.NoError(t, err)

	assert.Equal(t, editjob.DoneStatus, result.Status)
	assert.InDelta(t, 182, result.SourceDuration, 1e-9)
	assert.InDelta(t, 141, result.Duration, 1e-9)
	assert.Equal(t, 3, result.Cuts)
	assert.Zero(t, result.Candidates)
	assert.InDelta(t, 182, result.AudioSeconds, 1e-9)

	assert.True(t, fakes.cutter.got.PlanOnly, "the plan is rendered in the same pass as the subtitles")
	assert.Empty(t, fakes.cutter.got.AcceptCandidates, "disputed repeats are not cut")
	assert.Nil(t, fakes.cutter.got.Edit, "the automatic plan")
	assert.Equal(t, 1, fakes.transcriber.calls)
	require.NotNil(t, fakes.burner.got.Cut)
	assert.Equal(t, fakes.cutter.result.Segments(), fakes.burner.got.Cut.Segments)
	assert.Equal(t, fakes.cutter.result.Render, fakes.burner.got.Cut.Options)
	assert.Equal(t, ffmpeg.PadFitMode, fakes.burner.got.Fit)

	files, err := editjob.Files(fakes.store, testSourceKey, 1)
	require.NoError(t, err)
	assert.Equal(t, []string{files.Plan, files.Decisions, files.Transcript, files.Reel}, fakes.store.order,
		"the reel goes last")
	assert.Equal(t, editjob.VideoContentType, fakes.store.results[files.Reel].contentType)
	assert.Equal(t, "reel", string(fakes.store.results[files.Reel].data))
	assert.Contains(t, string(fakes.store.results[files.Transcript].data), "привет", "the source transcript")

	entries, err := os.ReadDir(workDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "the job dir is removed")
}

// TestPipelineToneMapsHDR: an HLG/PQ source is tone mapped, an SDR one is not.
func TestPipelineToneMapsHDR(t *testing.T) {
	t.Parallel()

	for _, transfer := range []string{testHLG, "smpte2084", testSDR} {
		fakes := newPipelineFakes(t)
		fakes.prober.info.ColorTransfer = transfer

		_, err := fakes.pipeline(t, t.TempDir()).Run(t.Context(), testJob())
		require.NoError(t, err)
		assert.Equal(t, transfer != testSDR, fakes.burner.got.ToneMap, transfer)
	}
}

// TestPipelineStopsForDecision: with the trainer's caption, disputed repeats or a talking head longer than the reel's
// target after the cleaning, the auto stage stops after the plan: the transcript, the plan and the log are stored for
// the editor, no reel is rendered.
func TestPipelineStopsForDecision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		decide bool
		hook   bool
		target float64
		change func(f *pipelineFakes)
	}{
		{name: "a caption to apply", decide: true},
		{name: "the hook title to propose", hook: true},
		{name: "longer than the target: 2:21 for 1:00", target: 60},
		{
			name:   "a disputed repeat",
			change: func(f *pipelineFakes) { f.cutter.result.Plan.Candidates = make([]roughcut.Candidate, 2) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fakes := newPipelineFakes(t)
			if tt.change != nil {
				tt.change(fakes)
			}

			job := testJob()
			job.Decide, job.Hook, job.TargetDuration = tt.decide, tt.hook, tt.target

			result, err := fakes.pipeline(t, t.TempDir()).Run(t.Context(), job)
			require.NoError(t, err)

			assert.Equal(t, editjob.PlannedStatus, result.Status)
			assert.Equal(t, len(fakes.cutter.result.Plan.Candidates), result.Candidates)
			assert.Nil(t, fakes.burner.got, "nothing is rendered before the decision")

			files, err := editjob.Files(fakes.store, testSourceKey, 1)
			require.NoError(t, err)
			assert.Equal(t, []string{files.Plan, files.Decisions, files.Transcript}, fakes.store.order)
		})
	}
}

// TestPipelineTargetDuration: a cleaned talking head within the target and its 10 % margin is rendered at once (no
// model call), a longer one stops for the editor's selection; without a target nothing is decided by the length.
func TestPipelineTargetDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		target   float64
		expected editjob.Status
	}{
		{name: "2:21 within 2:30", target: 150, expected: editjob.DoneStatus},
		{name: "2:21 exactly the target", target: 141, expected: editjob.DoneStatus},
		{name: "2:21 within the 10 % margin of 2:10", target: 130, expected: editjob.DoneStatus},
		{name: "2:21 longer than 2:00 with its margin", target: 120, expected: editjob.PlannedStatus},
		{name: "no target", expected: editjob.DoneStatus},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			job := testJob()
			job.TargetDuration = tt.target

			result, err := newPipelineFakes(t).pipeline(t, t.TempDir()).Run(t.Context(), job)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result.Status)
		})
	}
}

// TestPipelineLittleSpeech: a source where the talking-head cut keeps less than MEDIA_WORKER_MIN_SPEECH_SHARE, or with
// no words at all, may be a workout: the auto stage stops for the editor with the safe workout plan (nothing cut),
// instead of cutting the silence or refusing «no speech». With enough speech it is rendered at once, as before.
func TestPipelineLittleSpeech(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		share   float64
		noWords bool
		talkErr error
		status  editjob.Status
		workout bool
	}{
		{name: "speech keeps 77 % of the source: rendered at once", share: 0.77, status: editjob.DoneStatus},
		{name: "6 s of silence and 14 s of speech: rendered at once", share: 0.7, status: editjob.DoneStatus},
		{name: "speech keeps 16 % of the source: a workout plan to decide", share: 0.16, status: editjob.PlannedStatus, workout: true},
		{name: "no words: a workout plan to decide, not «no speech»", noWords: true, status: editjob.PlannedStatus, workout: true},
		{
			name:  "no talking-head cut (every word a filler, too many fragments): a workout plan, not «no speech»",
			share: 0.9, talkErr: fmt.Errorf("talk plan: %w", roughcut.ErrInvalidPlan), status: editjob.PlannedStatus, workout: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fakes := newPipelineFakes(t)
			fakes.minSpeechShare = 0.6
			fakes.cutter.result.Plan.SpeechShare = tt.share
			fakes.cutter.talkErr = tt.talkErr

			if tt.noWords {
				fakes.transcriber.result.Words = nil
			}

			result, err := fakes.pipeline(t, t.TempDir()).Run(t.Context(), testJob())
			require.NoError(t, err)

			assert.Equal(t, tt.status, result.Status)

			if !tt.workout {
				assert.Nil(t, fakes.cutter.got.Edit, "the talking-head plan")
				assert.NotNil(t, fakes.burner.got)

				return
			}

			require.NotNil(t, fakes.cutter.got.Edit, "the last plan is the workout one")
			assert.Equal(t, roughcut.WorkoutMode, fakes.cutter.got.Edit.Mode)
			assert.Nil(t, fakes.burner.got, "nothing is rendered before the decision")
		})
	}
}

// TestPipelineRendersTitlesAndMute: the exercise titles of the plan go to the subtitles; a muted plan has no speech
// subtitles.
func TestPipelineRendersTitlesAndMute(t *testing.T) {
	t.Parallel()

	fakes := newPipelineFakes(t)
	fakes.cutter.result.Titles = []roughcut.Title{{Text: "Выпады — 12 раз", Start: 0.2, End: 0.8}}
	fakes.cutter.result.Render.Mute = true

	_, err := fakes.pipeline(t, t.TempDir()).Run(t.Context(), testJob())
	require.NoError(t, err)

	require.NotNil(t, fakes.burner.got)
	assert.Equal(t, []subtitles.Title{{Text: "Выпады — 12 раз", Start: 0.2, End: 0.8}}, fakes.burner.got.Titles)
	assert.Empty(t, fakes.burner.got.Transcript.Words, "no speech subtitles without the sound")
	assert.True(t, fakes.burner.got.Cut.Options.Mute)
}

// TestPipelineRendersDecidedVersion: a render job (a revision) reads the stored transcript — the speech is not
// transcribed again, the OpenAI key is not needed — and builds the plan with the editor's edit; the reel of the
// version goes under its own key, the files of version 1 stay.
func TestPipelineRendersDecidedVersion(t *testing.T) {
	t.Parallel()

	fakes := newPipelineFakes(t)
	fakes.keys = fakeKeys{err: whisper.ErrAPIKeyMissing}

	first, err := editjob.Files(fakes.store, testSourceKey, 1)
	require.NoError(t, err)

	stored := `{"language":"ru","duration":182,"words":[{"word":"привет","start":0.5,"end":1}]}`
	require.NoError(t, fakes.store.PutResult(t.Context(), first.Transcript, strings.NewReader(stored), editjob.JSONContentType))
	require.NoError(t, fakes.store.PutResult(t.Context(), first.Reel, strings.NewReader("reel-v1"), editjob.VideoContentType))

	job := testJob()
	job.ID, job.Version, job.Stage = editjob.JobID(17, 2), 2, editjob.RenderStage
	job.Settings = &roughcut.Settings{MaxPause: 0.4, RetakeSimilarity: 0.7}
	job.Edit = &roughcut.Edit{Remove: []roughcut.WordRange{{From: 0, To: 0}}, Notes: []string{"начало убрано"}}

	result, err := fakes.pipeline(t, t.TempDir()).Run(t.Context(), job)
	require.NoError(t, err)

	assert.Equal(t, editjob.DoneStatus, result.Status)
	assert.Zero(t, fakes.transcriber.calls, "the stored transcript is used")
	assert.InDelta(t, 0, result.AudioSeconds, 1e-9, "nothing transcribed, nothing billed")
	assert.Equal(t, job.Edit, fakes.cutter.got.Edit)
	assert.Equal(t, job.Settings, fakes.cutter.got.Settings)
	require.NotNil(t, fakes.cutter.got.Transcript)
	assert.Equal(t, "привет", fakes.cutter.got.Transcript.Words[0].Word)

	second, err := editjob.Files(fakes.store, testSourceKey, 2)
	require.NoError(t, err)
	assert.Equal(t, []string{first.Transcript, first.Reel, second.Plan, second.Decisions, second.Reel}, fakes.store.order,
		"the transcript is not stored again")
	assert.Equal(t, "reel-v1", string(fakes.store.results[first.Reel].data), "version 1 is not overwritten")
	assert.Equal(t, "reel", string(fakes.store.results[second.Reel].data))
}

// TestPipelineRenderWithoutTranscript: a render job whose stored transcript is gone fails without a transcription.
func TestPipelineRenderWithoutTranscript(t *testing.T) {
	t.Parallel()

	fakes := newPipelineFakes(t)
	job := testJob()
	job.ID, job.Version, job.Stage = editjob.JobID(17, 2), 2, editjob.RenderStage
	job.Settings = &roughcut.Settings{MaxPause: 0.4, RetakeSimilarity: 0.7}

	_, err := fakes.pipeline(t, t.TempDir()).Run(t.Context(), job)
	require.Error(t, err)
	assert.Equal(t, editjob.SourceMissingCode, CodeOf(err))
	assert.Zero(t, fakes.transcriber.calls)
}

// TestPipelineRenderInvalidEdit: a decided edit the plan cannot be built with is the app's fault, not «no speech».
func TestPipelineRenderInvalidEdit(t *testing.T) {
	t.Parallel()

	fakes := newPipelineFakes(t)
	fakes.cutter.err = fmt.Errorf("plan: %w", roughcut.ErrInvalidPlan)

	first, err := editjob.Files(fakes.store, testSourceKey, 1)
	require.NoError(t, err)

	stored := `{"language":"ru","duration":182,"words":[{"word":"привет","start":0.5,"end":1}]}`
	require.NoError(t, fakes.store.PutResult(t.Context(), first.Transcript, strings.NewReader(stored), editjob.JSONContentType))

	job := testJob()
	job.ID, job.Version, job.Stage = editjob.JobID(17, 2), 2, editjob.RenderStage
	job.Settings = &roughcut.Settings{MaxPause: 0.4, RetakeSimilarity: 0.7}

	_, err = fakes.pipeline(t, t.TempDir()).Run(t.Context(), job)
	require.Error(t, err)
	assert.Equal(t, editjob.InvalidEditCode, CodeOf(err))
}

func TestPipelineFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		change        func(f *pipelineFakes)
		expected      editjob.FailureCode
		transcribed   bool
		checkDuration bool
	}{
		{
			name:     "no OPENAI_API_KEY: nothing is downloaded",
			change:   func(f *pipelineFakes) { f.keys = fakeKeys{err: whisper.ErrAPIKeyMissing} },
			expected: editjob.NotConfiguredCode,
		},
		{
			name:     "no source in S3",
			change:   func(f *pipelineFakes) { f.store.sources = map[string]string{} },
			expected: editjob.SourceMissingCode,
		},
		{
			name:     "unreadable source",
			change:   func(f *pipelineFakes) { f.prober.err = errors.New("invalid data found") },
			expected: editjob.BadSourceCode,
		},
		{
			name:     "no sound",
			change:   func(f *pipelineFakes) { f.prober.info.HasAudio = false },
			expected: editjob.NoAudioCode,
		},
		{
			name:          "longer than the limit: refused before the transcription",
			change:        func(f *pipelineFakes) { f.prober.info.Duration = 601 },
			expected:      editjob.SourceTooLongCode,
			checkDuration: true,
		},
		{
			name: "the API refused the key",
			change: func(f *pipelineFakes) {
				f.transcriber.err = fmt.Errorf("failed: %w", &whisper.StatusError{StatusCode: http.StatusUnauthorized})
			},
			expected:    editjob.TranscriptionRejectedCode,
			transcribed: true,
		},
		{
			name:        "the API is down: temporary",
			change:      func(f *pipelineFakes) { f.transcriber.err = &whisper.StatusError{StatusCode: http.StatusBadGateway} },
			expected:    editjob.TemporaryCode,
			transcribed: true,
		},
		{
			name:        "nothing to keep in the plan",
			change:      func(f *pipelineFakes) { f.cutter.err = fmt.Errorf("plan: %w", roughcut.ErrInvalidPlan) },
			expected:    editjob.NoSpeechCode,
			transcribed: true,
		},
		{
			name:        "a brand font was replaced: a configuration error",
			change:      func(f *pipelineFakes) { f.burner.result.FontFallbacks = []string{"Manrope -> DejaVuSans"} },
			expected:    editjob.FontsCode,
			transcribed: true,
		},
		{
			name:        "S3 fails on upload: temporary",
			change:      func(f *pipelineFakes) { f.store.putErr = errS3Down },
			expected:    editjob.TemporaryCode,
			transcribed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fakes := newPipelineFakes(t)
			tt.change(fakes)
			workDir := t.TempDir()

			result, err := fakes.pipeline(t, workDir).Run(t.Context(), testJob())
			require.Error(t, err)
			assert.Equal(t, tt.expected, CodeOf(err))
			assert.Equal(t, tt.transcribed, fakes.transcriber.calls > 0)

			if tt.checkDuration {
				assert.InDelta(t, 601, result.SourceDuration, 1e-9)
				assert.InDelta(t, 600, result.MaxDuration, 1e-9)
			}

			entries, err := os.ReadDir(workDir)
			require.NoError(t, err)
			assert.Empty(t, entries, "the job dir is removed after a failure too")
		})
	}
}

func TestNewPipelineRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input config.MediaWorker
	}{
		{name: "unknown fit", input: config.MediaWorker{Fit: "stretch", MaxSourceDuration: time.Minute, MaxSourceSize: 1}},
		{name: "no duration limit", input: config.MediaWorker{Fit: "pad", MaxSourceSize: 1}},
		{
			name:  "a speech share above 1",
			input: config.MediaWorker{Fit: "pad", MaxSourceDuration: time.Minute, MaxSourceSize: 1, MinSpeechShare: 1.5},
		},
		{
			name:  "a loudness that is not a number",
			input: config.MediaWorker{Fit: string(ffmpeg.PadFitMode), MaxSourceDuration: time.Minute, MaxSourceSize: 1, Loudness: "loud"},
		},
		{
			name:  "a loudness louder than loudnorm takes",
			input: config.MediaWorker{Fit: string(ffmpeg.PadFitMode), MaxSourceDuration: time.Minute, MaxSourceSize: 1, Loudness: "-3"},
		},
		{
			name:  "a loudness that is NaN",
			input: config.MediaWorker{Fit: string(ffmpeg.PadFitMode), MaxSourceDuration: time.Minute, MaxSourceSize: 1, Loudness: "NaN"},
		},
		{
			name:  "a zoom of 50 %",
			input: config.MediaWorker{Fit: string(ffmpeg.PadFitMode), MaxSourceDuration: time.Minute, MaxSourceSize: 1, Zoom: 50},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewPipeline(&PipelineParams{Config: &tt.input, Log: slog.New(slog.DiscardHandler)})
			require.ErrorIs(t, err, ErrInvalidConfig)
		})
	}
}

// TestPipelineDrawsHook: the hook title of the decided edit goes to the subtitles; a plan without one draws none.
func TestPipelineDrawsHook(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		edit     *roughcut.Edit
		expected string
	}{
		{name: "the hook of the edit", edit: &roughcut.Edit{Hook: hookText}, expected: hookText},
		{name: "an edit without one", edit: &roughcut.Edit{}, expected: ""},
		{name: "an automatic plan", edit: nil, expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fakes := newPipelineFakes(t)
			fakes.cutter.result.Plan.Edit = tt.edit

			result, err := fakes.pipeline(t, t.TempDir()).Run(t.Context(), testJob())
			require.NoError(t, err)
			assert.Equal(t, editjob.DoneStatus, result.Status)
			require.NotNil(t, fakes.burner.got)
			assert.Equal(t, tt.expected, fakes.burner.got.Hook)
		})
	}
}

// TestPipelineSoundAndZoom: the draft is brought to the loudness by the measurement of the source taken during the
// transcription (one gain and the limiter); a talking head is denoised (the noise floor by the loudness of the
// recording) and zoomed at the joints, a workout is not; a muted sound and a near-silent
// source are not raised; without a measurement (a transcript stored before it) loudnorm works in one pass. The
// decisions log tells what was done.
func TestPipelineSoundAndZoom(t *testing.T) {
	t.Parallel()

	measured := &ffmpeg.Loudness{Integrated: -27.6, Threshold: -38.1, Range: 6.4, TruePeak: -9.3}

	tests := []struct {
		name     string
		change   func(f *pipelineFakes)
		measured *ffmpeg.Loudness
		loudness bool
		// noiseFloor of the noise reduction, dBFS; 0 — none.
		noiseFloor float64
		zoom       bool
		decisions  string
	}{
		{
			// The floor of the noise reduction is 36 dB under the speech: -27.6 - 36 ≈ -64 dBFS.
			name: "a talking head", measured: measured, loudness: true, noiseFloor: -64, zoom: true,
			decisions: "- Громкость: исходник -27.6 LUFS (пики -9.3 dBTP), цель -14.0 LUFS: усиление +13.6 dB, " +
				"лимитер пиков на -2.0 dBTP; итог не измерен\n- Шумодав: лёгкий (речь), порог шума -64 dBFS\n" +
				"- Зумы на склейках: 0 (приближение 9%)\n",
		},
		{
			// The render fitted the gain by the sound of the cut and measured what it gave: the log has the measurement.
			name: "a talking head with the render's report",
			change: func(f *pipelineFakes) {
				f.burner.result.Sound = &ffmpeg.SoundReport{
					Gain: 14.9, Correction: 1.3, Passes: 2, Output: &ffmpeg.Loudness{Integrated: -14.1, TruePeak: -2.4},
				}
			},
			measured: measured, loudness: true, noiseFloor: -64, zoom: true,
			decisions: "- Громкость: исходник -27.6 LUFS (пики -9.3 dBTP), цель -14.0 LUFS: усиление +14.9 dB " +
				"(поправка +1.3 dB по предварительному замеру склейки, проходов: 2), лимитер пиков на -2.0 dBTP; " +
				"итог -14.1 LUFS, true peak -2.4 dBTP (замер готового файла, после AAC)\n",
		},
		{
			// The passes over the sound of the cut failed: the render went on with the gain by the measurement.
			name: "the loudness not fitted",
			change: func(f *pipelineFakes) {
				f.burner.result.Sound = &ffmpeg.SoundReport{
					Gain: 13.6, FitError: errors.New("measure failed"), PeakCut: 1.2,
					Output: &ffmpeg.Loudness{Integrated: -15.2, TruePeak: -2.1},
				}
			},
			measured: measured, loudness: true, noiseFloor: -64, zoom: true,
			decisions: "усиление +13.6 dB (без поправки: предварительный замер склейки не удался), лимитер пиков на " +
				"-2.0 dBTP; итог -15.2 LUFS, true peak -2.1 dBTP (замер готового файла, после AAC); кодек поднял пики, " +
				"звук файла снижен на 1.2 dB\n",
		},
		{
			name:     "a transcript without a measurement: one pass",
			change:   func(f *pipelineFakes) { f.transcriber.result.Loudness = nil },
			loudness: true, noiseFloor: -50, zoom: true,
			decisions: "- Громкость: исходник не измерен, цель -14.0 LUFS: loudnorm без измерения (в один проход), " +
				"пики до -2.0 dBTP; итог не измерен\n- Шумодав: лёгкий (речь), порог шума -50 dBFS\n",
		},
		{
			name:     "a workout: the loudness only",
			change:   func(f *pipelineFakes) { f.cutter.result.Plan.Mode = roughcut.WorkoutMode },
			measured: measured, loudness: true,
			decisions: "- Шумодав: нет (тренировка, музыка)\n- Зумы на склейках: нет (тренировка)\n",
		},
		{
			name:      "muted: silence stays silent",
			change:    func(f *pipelineFakes) { f.cutter.result.Render.Mute = true },
			zoom:      true,
			decisions: "- Громкость: звук исходника убран\n- Шумодав: нет (звук убран)\n",
		},
		{
			name: "a near-silent source is not raised",
			change: func(f *pipelineFakes) {
				f.transcriber.result.Loudness = &ffmpeg.Loudness{Integrated: -62, Threshold: -72, TruePeak: -40}
			},
			noiseFloor: -80, zoom: true,
			decisions: "- Громкость: исходник -62.0 LUFS — почти тишина, оставлена как есть\n",
		},
		{
			name:      "all off",
			change:    func(f *pipelineFakes) { f.loudness, f.denoise, f.zoom = "off", false, 0 },
			decisions: "- Громкость: как в исходнике (выравнивание выключено)\n- Шумодав: выключен\n- Зумы на склейках: выключены\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fakes := newPipelineFakes(t)
			fakes.loudness, fakes.denoise, fakes.zoom = testLoudness, true, testZoom
			fakes.transcriber.result.Loudness = measured

			if tt.change != nil {
				tt.change(fakes)
			}

			_, err := fakes.pipeline(t, t.TempDir()).Run(t.Context(), testJob())
			require.NoError(t, err)

			require.NotNil(t, fakes.burner.got)
			cut := fakes.burner.got.Cut
			if tt.noiseFloor != 0 {
				assert.Equal(t, &ffmpeg.Denoise{NoiseFloor: tt.noiseFloor}, cut.Options.Denoise)
			} else {
				assert.Nil(t, cut.Options.Denoise)
			}

			if tt.loudness {
				require.NotNil(t, cut.Options.Loudness)
				assert.Equal(t, ffmpeg.LoudnessTarget{Integrated: -14, TruePeak: -2, Range: 11, Measured: tt.measured},
					*cut.Options.Loudness)
			} else {
				assert.Nil(t, cut.Options.Loudness)
			}

			if tt.zoom {
				assert.Equal(t, &ffmpeg.Zoom{Factor: 1.09, MinSegment: 1.2}, cut.Zoom)
			} else {
				assert.Nil(t, cut.Zoom)
			}

			files, err := editjob.Files(fakes.store, testSourceKey, 1)
			require.NoError(t, err)
			log := string(fakes.store.results[files.Decisions].data)
			assert.True(t, strings.HasPrefix(log, roughcut.LogFile+"\nЗвук и кадр:\n"), log)
			assert.Contains(t, log, tt.decisions)
		})
	}
}
