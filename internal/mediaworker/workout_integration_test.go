package mediaworker

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/subtitles"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const (
	// defaultSpeechShare is the default of MEDIA_WORKER_MIN_SPEECH_SHARE.
	defaultSpeechShare = 0.6
	// workoutDuration of the synthetic workout: one spoken word, then the exercise shown in silence.
	workoutDuration = 12
	// noSpeechDuration of the synthetic workout to music: a tone all along, no words.
	noSpeechDuration = 8
	workoutTitle     = "Выпады — 12 раз"
	// silentVolume: a muted reel is far below any sound (digital silence is about -91 dB).
	silentVolume = -60.0
)

var maxVolumeRe = regexp.MustCompile(`max_volume: (\S+) dB`)

// capturingBurner renders with the real subtitles and keeps what the pipeline asked for.
type capturingBurner struct {
	service *subtitles.Service

	mu  sync.Mutex
	got *subtitles.BurnSubtitlesData
}

func (c *capturingBurner) BurnSubtitles(ctx context.Context, data *subtitles.BurnSubtitlesData) (*subtitles.BurnResult, error) {
	c.mu.Lock()
	c.got = data
	c.mu.Unlock()

	return c.service.BurnSubtitles(ctx, data)
}

func (c *capturingBurner) last() *subtitles.BurnSubtitlesData {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.got
}

// workoutPipeline is the real pipeline (ffmpeg, rough cut, subtitles with the brand fonts) with the default check of
// little speech; the speech recognition is faked.
func workoutPipeline(t *testing.T, files store, speech transcriber) (*Pipeline, *capturingBurner) {
	t.Helper()

	log := slog.New(slog.DiscardHandler)
	runner := ffmpeg.NewRunner(&config.FFmpeg{Timeout: 2 * time.Minute})

	cutter, err := roughcut.NewService(runner, &config.RoughCut{
		MaxPause: 400 * time.Millisecond, RetakeSimilarity: 0.7, Fillers: []string{"э", "эм"},
	}, log)
	require.NoError(t, err)

	fonts := os.Getenv("SUBTITLES_FONTS_DIR")
	if fonts == "" {
		fonts = repoFontsDir
	}

	service, err := subtitles.NewService(runner, &config.Subtitles{FontsDir: fonts}, log)
	require.NoError(t, err)

	burner := &capturingBurner{service: service}

	p, err := NewPipeline(&PipelineParams{
		Store: files, Prober: runner, Keys: fakeKeys{}, Transcriber: speech, Cutter: cutter, Burner: burner,
		Config: &config.MediaWorker{
			WorkDir: t.TempDir(), MaxSourceDuration: time.Minute, MaxSourceSize: 1 << 30,
			Fit: string(ffmpeg.PadFitMode), MinSpeechShare: defaultSpeechShare,
		},
		Log: log,
	})
	require.NoError(t, err)

	return p, burner
}

// makeToneClip writes a vertical 30 fps clip of the duration where a tone sounds during the spans and the rest is
// silence.
func makeToneClip(t *testing.T, path string, duration int, spans []roughcut.Interval) {
	t.Helper()

	expr := "0"

	if len(spans) > 0 {
		parts := make([]string, 0, len(spans))
		for _, s := range spans {
			parts = append(parts, "between(t\\,"+strconv.FormatFloat(s.Start, 'f', 3, 64)+"\\,"+
				strconv.FormatFloat(s.End, 'f', 3, 64)+")")
		}

		expr = "if(" + strings.Join(parts, "+") + "\\,0.5*sin(2*PI*440*t)\\,0)"
	}

	d := strconv.Itoa(duration)

	run(t, "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "aevalsrc="+expr+":s=48000:d="+d,
		"-f", "lavfi", "-i", "testsrc=duration="+d+":size=720x1280:rate=30",
		"-c:a", "aac", "-shortest", "-c:v", "mpeg4", path)
}

// storedPlan reads the plan of version 1 the auto stage stored.
func storedPlan(t *testing.T, s *localStore) roughcut.Plan {
	t.Helper()

	files, err := editjob.Files(s, testSourceKey, 1)
	require.NoError(t, err)

	var plan roughcut.Plan
	require.NoError(t, json.Unmarshal(s.results[files.Plan].data, &plan))

	return plan
}

// storedReel writes the reel of version 1 to a local file.
func storedReel(t *testing.T, s *localStore) string {
	t.Helper()

	files, err := editjob.Files(s, testSourceKey, 1)
	require.NoError(t, err)

	reel := filepath.Join(t.TempDir(), "reel.mp4")
	require.NoError(t, os.WriteFile(reel, s.results[files.Reel].data, 0o600))

	return reel
}

// renderJob is the decided render of version 1 with the edit.
func renderJob(plan *roughcut.Plan, edit *roughcut.Edit) *editjob.Job {
	job := testJob()
	job.Stage, job.Settings, job.Edit = editjob.RenderStage, &plan.Settings, edit

	return job
}

// TestIntegrationWorkoutKeepsPauses: a workout — one spoken word, then 11 s of the exercise in silence, no caption —
// is not cut as a talking head: the auto stage stops for the editor with the workout plan (the whole source); the
// decided render keeps the pauses (the reel is as long as the source) and draws the exercise title in its ASS style.
func TestIntegrationWorkoutKeepsPauses(t *testing.T) {
	t.Parallel()
	requireIntegrationFFmpeg(t)

	source := filepath.Join(t.TempDir(), "workout.mkv")
	word := transcription.Word{Word: "Выпады", Start: 0.5, End: 1.1}
	makeToneClip(t, source, workoutDuration, []roughcut.Interval{{Start: word.Start, End: word.End}})

	files := newLocalStore(map[string]string{testSourceKey: source})
	p, burner := workoutPipeline(t, files, &fakeTranscriber{result: &transcription.Result{
		Language: testLanguage, Duration: workoutDuration, Text: "Выпады.", Words: []transcription.Word{word},
		Segments: []transcription.Segment{{Start: word.Start, End: word.End, Text: "Выпады."}},
	}})

	planned, err := p.Run(t.Context(), testJob())
	require.NoError(t, err)
	assert.Equal(t, editjob.PlannedStatus, planned.Status, "little speech: the editor decides, nothing is rendered")
	assert.InDelta(t, workoutDuration, planned.Duration, 0.1, "the safe workout plan keeps the whole source")
	assert.Nil(t, burner.last())

	plan := storedPlan(t, files)
	assert.Equal(t, roughcut.WorkoutMode, plan.Mode)

	edit := &roughcut.Edit{Mode: roughcut.WorkoutMode, Titles: []roughcut.Title{{Text: workoutTitle, Start: word.Start}}}

	done, err := p.Run(t.Context(), renderJob(&plan, edit))
	require.NoError(t, err)
	assert.Equal(t, editjob.DoneStatus, done.Status)
	assert.InDelta(t, workoutDuration, done.Duration, 0.1, "the pauses are kept")

	checkReel(t, storedReel(t, files), 30, done.Duration)

	got := burner.last()
	require.NotNil(t, got)
	require.Len(t, got.Titles, 1)
	assert.Equal(t, workoutTitle, got.Titles[0].Text)
	assert.InDelta(t, word.Start, got.Titles[0].Start, 0.05)
	assert.InDelta(t, workoutDuration, got.Titles[0].End, 0.1, "the last title runs to the end")

	ass, err := burner.service.CreateSubtitles(got.CreateSubtitlesData)
	require.NoError(t, err)
	assert.Contains(t, string(ass.ASS), "Style: Title,Fira Sans Extra Condensed ExtraBold,")
	assert.Contains(t, string(ass.ASS), ",Title,,0,0,0,,ВЫПАДЫ — 12 РАЗ\n")
	assert.Contains(t, string(ass.ASS), ",Main,,0,0,0,,Выпады", "the speech subtitles stay")

	// The spoken word hidden in the subtitles only: the reel is as long, the word is not in the ASS, the title stays.
	edit.Hide = []roughcut.WordRange{{From: 0, To: 0}}

	hidden, err := p.Run(t.Context(), renderJob(&plan, edit))
	require.NoError(t, err)
	assert.InDelta(t, done.Duration, hidden.Duration, 0.01, "hiding a word cuts nothing")

	ass, err = burner.service.CreateSubtitles(burner.last().CreateSubtitlesData)
	require.NoError(t, err)
	assert.NotContains(t, string(ass.ASS), ",Main,,")
	assert.Contains(t, string(ass.ASS), ",Title,,0,0,0,,ВЫПАДЫ — 12 РАЗ\n")
	checkReel(t, storedReel(t, files), 30, hidden.Duration)
}

// TestIntegrationNoSpeechIsPlanned: a workout to music — sound all along, no words — is not refused as «no speech»:
// it is planned whole for the editor; a muted render keeps the whole video with a silent audio track and no speech
// subtitles.
func TestIntegrationNoSpeechIsPlanned(t *testing.T) {
	t.Parallel()
	requireIntegrationFFmpeg(t)

	source := filepath.Join(t.TempDir(), "music.mkv")
	makeToneClip(t, source, noSpeechDuration, []roughcut.Interval{{Start: 0, End: noSpeechDuration}})

	files := newLocalStore(map[string]string{testSourceKey: source})
	p, burner := workoutPipeline(t, files, &fakeTranscriber{result: &transcription.Result{
		Language: testLanguage, Duration: noSpeechDuration,
	}})

	planned, err := p.Run(t.Context(), testJob())
	require.NoError(t, err, "not no_speech")
	assert.Equal(t, editjob.PlannedStatus, planned.Status)
	assert.InDelta(t, noSpeechDuration, planned.Duration, 0.1)

	plan := storedPlan(t, files)
	require.Equal(t, roughcut.WorkoutMode, plan.Mode)

	done, err := p.Run(t.Context(), renderJob(&plan, &roughcut.Edit{Mode: roughcut.WorkoutMode, Mute: true}))
	require.NoError(t, err)
	assert.Equal(t, editjob.DoneStatus, done.Status)

	reel := storedReel(t, files)
	checkReel(t, reel, 30, done.Duration)
	assert.InDelta(t, noSpeechDuration, done.Duration, 0.1)
	assert.Less(t, maxVolume(t, reel), silentVolume, "the sound of the source is removed")
	assert.Greater(t, maxVolume(t, source), silentVolume, "the source itself is loud")

	require.NotNil(t, burner.last())
	assert.Empty(t, burner.last().Transcript.Words)
}

// maxVolume is the peak level of the audio of the file in dB (-Inf for digital silence).
func maxVolume(t *testing.T, path string) float64 {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-nostdin", "-i", path, "-vn", "-af", "volumedetect",
		"-f", "null", "-")

	var stderr bytes.Buffer

	cmd.Stderr = &stderr
	require.NoError(t, cmd.Run(), stderr.String())

	m := maxVolumeRe.FindStringSubmatch(stderr.String())
	require.NotNil(t, m, stderr.String())

	if m[1] == "-inf" {
		return math.Inf(-1)
	}

	volume, err := strconv.ParseFloat(m[1], 64)
	require.NoError(t, err)

	return volume
}

// TestIntegrationTalkRendersAtOnce: a dense talk (1.5 s of silence, then words with pauses of 0.2 s) keeps far more
// than the default share of speech: with the check of little speech on it is rendered at once, as before — no planned
// stage, no model.
func TestIntegrationTalkRendersAtOnce(t *testing.T) {
	t.Parallel()
	requireIntegrationFFmpeg(t)

	const (
		talkDuration = 20
		talkStart    = 1.5
		wordLength   = 0.4
		wordStep     = 0.6 // a 0.2 s pause after every word
	)

	texts := strings.Fields("сегодня мы делаем простую зарядку для спины и плеч она займёт всего десять минут " +
		"встаньте прямо поставьте ноги на ширину плеч руки опустите вдоль тела и начнём вместе")
	words := make([]transcription.Word, 0, len(texts))
	spans := make([]roughcut.Interval, 0, len(texts))

	for i, text := range texts {
		start := talkStart + wordStep*float64(i)
		words = append(words, transcription.Word{Word: text, Start: start, End: start + wordLength})
		spans = append(spans, roughcut.Interval{Start: start, End: start + wordLength})
	}

	source := filepath.Join(t.TempDir(), "talk.mkv")
	makeToneClip(t, source, talkDuration, spans)

	last, sentence := words[len(words)-1], strings.Join(texts, " ")+"."
	files := newLocalStore(map[string]string{testSourceKey: source})
	p, burner := workoutPipeline(t, files, &fakeTranscriber{result: &transcription.Result{
		Language: testLanguage, Duration: talkDuration, Text: sentence, Words: words,
		Segments: []transcription.Segment{{Start: words[0].Start, End: last.End, Text: sentence}},
	}})

	result, err := p.Run(t.Context(), testJob())
	require.NoError(t, err)

	assert.Equal(t, editjob.DoneStatus, result.Status, "a talking head is rendered at once")
	assert.Less(t, result.Duration, float64(talkDuration)-2, "the silence at the start and the end is cut")
	assert.Greater(t, result.Duration, defaultSpeechShare*talkDuration)
	require.NotNil(t, burner.last())

	plan := storedPlan(t, files)
	assert.Empty(t, plan.Mode, "the talking-head plan")
	assert.GreaterOrEqual(t, plan.SpeechShare, defaultSpeechShare)

	checkReel(t, storedReel(t, files), 30, result.Duration)
}
