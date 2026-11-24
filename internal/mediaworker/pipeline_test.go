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
		transcriber: &fakeTranscriber{result: &transcription.Result{Language: "ru", Duration: 182, Words: words}},
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

// TestPipelineStopsForDecision: with the trainer's caption or disputed repeats the auto stage stops after the plan:
// the transcript, the plan and the log are stored for the editor, no reel is rendered.
func TestPipelineStopsForDecision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		decide bool
		change func(f *pipelineFakes)
	}{
		{name: "a caption to apply", decide: true},
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
			job.Decide = tt.decide

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
			name:        "no words",
			change:      func(f *pipelineFakes) { f.transcriber.result.Words = nil },
			expected:    editjob.NoSpeechCode,
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewPipeline(&PipelineParams{Config: &tt.input, Log: slog.New(slog.DiscardHandler)})
			require.ErrorIs(t, err, ErrInvalidConfig)
		})
	}
}
