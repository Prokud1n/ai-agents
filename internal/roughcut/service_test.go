package roughcut

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

var errRender = errors.New("render failed")

const testFrameRate = 25

var testRate = ffmpeg.Rational{Num: testFrameRate, Den: 1}

type fakeMedia struct {
	duration  float64
	info      *ffmpeg.MediaInfo // overrides the default probe result
	opts      ffmpeg.RenderOptions
	renderErr error
	rendered  []ffmpeg.Segment
	dst       string
	levels    *ffmpeg.Levels // the sound level of the source; nil: the margins
}

func (f *fakeMedia) Probe(context.Context, string) (ffmpeg.MediaInfo, error) {
	if f.info != nil {
		return *f.info, nil
	}

	return ffmpeg.MediaInfo{Duration: f.duration, HasVideo: true, HasAudio: true, AvgFrameRate: testRate}, nil
}

func (f *fakeMedia) AudioLevels(context.Context, string) (*ffmpeg.Levels, error) {
	return f.levels, nil
}

func (f *fakeMedia) RenderSegments(
	_ context.Context,
	_, dst string,
	segments []ffmpeg.Segment,
	opts ffmpeg.RenderOptions,
) error {
	f.rendered, f.dst, f.opts = segments, dst, opts
	if f.renderErr != nil {
		return f.renderErr
	}

	return writeFile(dst, []byte("video"))
}

func testConfig() *config.RoughCut {
	return &config.RoughCut{MaxPause: 400 * time.Millisecond, RetakeSimilarity: 0.7, Fillers: []string{testFiller, unitFiller}}
}

func newTestService(t *testing.T, media mediaProcessor) *Service {
	t.Helper()

	svc, err := NewService(media, testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)

	return svc
}

func readPlan(t *testing.T, path string) Plan {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var p Plan
	require.NoError(t, json.Unmarshal(data, &p))

	return p
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}

	return names
}

func TestServiceCut(t *testing.T) {
	t.Parallel()

	src := speech("давайте при | давайте приступим || э к разминке")

	tests := []struct {
		name          string
		planOnly      bool
		expectedFiles []string
	}{
		{name: "plan only", planOnly: true, expectedFiles: []string{LogFile, PlanFile, TranscriptFile}},
		{name: "with render", expectedFiles: []string{LogFile, PlanFile, VideoFile, TranscriptFile}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			media := &fakeMedia{duration: src.Duration}
			dir := filepath.Join(t.TempDir(), "out")

			result, err := newTestService(t, media).Cut(t.Context(), &CutData{
				Source: "/videos/clip.mp4", Transcript: src, OutDir: dir, PlanOnly: tt.planOnly,
			})
			require.NoError(t, err)

			assert.Equal(t, tt.expectedFiles, listDir(t, dir))
			assert.Equal(t, testClip, result.Plan.Source)
			assert.Equal(t, result.Plan, readPlan(t, result.Files.Plan))
			assert.Equal(t, "давайте приступим к разминке", joinWords(result.Transcript.Words))
			assert.Len(t, result.Plan.Takes, 1)

			assert.Len(t, result.Segments(), len(result.Plan.Keep), "the plan can be rendered by the caller")
			assert.Positive(t, result.Render.FrameRate.Float())
			assert.InDelta(t, junctionFade, result.Render.Fade, 1e-9)

			if tt.planOnly {
				assert.Empty(t, media.rendered)
				assert.Empty(t, result.Files.Video)

				return
			}

			assert.Equal(t, filepath.Join(dir, VideoFile), result.Files.Video)
			assert.Len(t, media.rendered, len(result.Plan.Keep))
		})
	}
}

// TestServiceCutSectionOnCutReading (T3.22): the spoken heading «ошибка номер два» is cut from the sound, the section
// set on it lives to the result at the start of the question, and no long pause is left at the joint.
func TestServiceCutSectionOnCutReading(t *testing.T) {
	t.Parallel()

	const (
		answerEnd     = 1.0
		questionSound = 4.95
	)

	src := words(word("ответ", 0.5, answerEnd), word("ошибка", 3.0, 3.4), word("номер", 3.45, 3.85),
		word("два", 3.9, 4.3), word("а", 4.9, 5.1), word("вдруг", 5.15, 5.6), word("забуду", 5.65, 6.2))
	src.Duration, src.Language = 7, testLanguage

	media := &fakeMedia{duration: 7, levels: soundOf(7, [3]float64{0.5, answerEnd, testSpeech},
		[3]float64{3.05, 4.3, testSpeech}, [3]float64{questionSound, 6.2, testSpeech})}
	edit := &Edit{
		Remove:      []WordRange{{From: 1, To: 3}},
		ScreenNotes: []ScreenNote{{Kind: SectionNote, Text: sectionTwo, Start: 3.0}},
	}

	result, err := newTestService(t, media).Cut(t.Context(), &CutData{
		Source: "/videos/clip.mp4", Transcript: src, OutDir: filepath.Join(t.TempDir(), "out"), PlanOnly: true,
		Edit: edit,
	})
	require.NoError(t, err)
	require.True(t, result.Plan.SoundCuts)
	require.Len(t, result.Plan.Keep, 2)

	keep := result.Plan.Keep
	require.Len(t, result.ScreenNotes, 1, "the section is not lost with its reading")
	assert.InDelta(t, keep[0].End-keep[0].Start, result.ScreenNotes[0].Start, 1e-3, "where the question starts")
	assert.Equal(t, "ответ а вдруг забуду", joinWords(result.Transcript.Words))

	silence := (keep[0].End - answerEnd) + (questionSound - keep[1].Start)
	assert.LessOrEqual(t, silence, result.Plan.Settings.MaxPause+1e-3, "no long pause at the joint")
}

// TestServiceCutNoteOnQuietStart (T3.22): Whisper starts a kept word 0.65 s before its sound, the cut by the sound
// starts its piece about 0.5 s after the word; a callout on that word (it passed the app's check) still lives to the
// result, and the word to the subtitles.
func TestServiceCutNoteOnQuietStart(t *testing.T) {
	t.Parallel()

	src := words(word("ответ", 0.5, 1.0), word("лишнее", 2.0, 2.4), word("десять", 3.0, 3.9),
		word("минут", 3.95, 4.4))
	src.Duration, src.Language = 6, testLanguage

	media := &fakeMedia{duration: 6, levels: soundOf(6, [3]float64{0.5, 1.0, testSpeech},
		[3]float64{2.0, 2.4, testSpeech}, [3]float64{3.65, 4.4, testSpeech})}
	edit := &Edit{
		Remove:      []WordRange{{From: 1, To: 1}},
		ScreenNotes: []ScreenNote{{Kind: CalloutNote, Text: "10 минут", Start: 3.0}},
	}

	result, err := newTestService(t, media).Cut(t.Context(), &CutData{
		Source: "/videos/clip.mp4", Transcript: src, OutDir: filepath.Join(t.TempDir(), "out"), PlanOnly: true,
		Edit: edit,
	})
	require.NoError(t, err)
	require.Len(t, result.Plan.Keep, 2)

	keep := result.Plan.Keep
	shift := keep[1].Start - 3.0
	require.Greater(t, shift, 0.4, "the piece starts well after Whisper's start of the word")
	require.LessOrEqual(t, shift, noteShiftReach)

	require.Len(t, result.ScreenNotes, 1, "the callout is not lost")
	assert.InDelta(t, keep[0].End-keep[0].Start, result.ScreenNotes[0].Start, 1e-3)
	assert.Equal(t, "ответ десять минут", joinWords(result.Transcript.Words))
}

func TestServiceCutSettings(t *testing.T) {
	t.Parallel()

	src := speech("раз || два")
	settings := newTestService(t, &fakeMedia{}).DefaultSettings()
	settings.MaxPause = 3

	result, err := newTestService(t, &fakeMedia{duration: src.Duration}).Cut(t.Context(), &CutData{
		Source: testClip, Transcript: src, OutDir: t.TempDir(), PlanOnly: true, Settings: &settings,
	})
	require.NoError(t, err)

	assert.Len(t, result.Plan.Keep, 1)
	assert.InDelta(t, 3.0, result.Plan.Settings.MaxPause, timeEpsilon)
}

func TestServiceCutEditedPlan(t *testing.T) {
	t.Parallel()

	src := speech("раз два три четыре")
	edited := &Plan{Version: PlanVersion, FrameRate: 1000, Keep: []Interval{{Start: 0.4, End: 1.4}, {Start: 1.8, End: 2.4}}}
	media := &fakeMedia{duration: src.Duration}

	result, err := newTestService(t, media).Cut(t.Context(), &CutData{
		Source: testClip, Transcript: src, Plan: edited, OutDir: t.TempDir(),
	})
	require.NoError(t, err)

	assert.Equal(t, []ffmpeg.Segment{{Start: 0.4, End: 1.4}, {Start: 1.8, End: 2.4}}, media.rendered)
	assert.InDelta(t, 1.6, result.Plan.Duration, timeEpsilon)
	assert.InDelta(t, junctionFade, media.opts.Fade, timeEpsilon)
	assert.Equal(t, testRate, media.opts.FrameRate, "the frame rate of the file is not trusted")
	assert.InDelta(t, testFrameRate, result.Plan.FrameRate, timeEpsilon)
	assert.Equal(t, "раз два четыре", joinWords(result.Transcript.Words))
}

func TestServiceCutErrors(t *testing.T) {
	t.Parallel()

	src := speech("раз два")

	tests := []struct {
		name     string
		input    CutData
		media    *fakeMedia
		expected error
	}{
		{name: "no source", input: CutData{Transcript: src, OutDir: "out"}, expected: ErrEmptySource},
		{name: "no transcript", input: CutData{Source: testSource, OutDir: "out"}, expected: ErrEmptyTranscript},
		{name: "no output dir", input: CutData{Source: testSource, Transcript: src}, expected: ErrEmptyOutDir},
		{
			name:     "nothing to keep",
			input:    CutData{Source: testSource, Transcript: speech("э ммм")},
			expected: ErrInvalidPlan,
		},
		{
			name:     "edited plan of another version",
			input:    CutData{Source: testSource, Transcript: src, Plan: &Plan{Version: 2, Keep: []Interval{{Start: 0, End: 1}}}},
			expected: ErrInvalidPlan,
		},
		{
			name: "edited plan with overlapping intervals",
			input: CutData{Source: testSource, Transcript: src, Plan: &Plan{
				Version: PlanVersion, Keep: []Interval{{Start: 0, End: 1}, {Start: 0.5, End: 1.5}},
			}},
			expected: ErrInvalidPlan,
		},
		{
			name: "edited plan past the end",
			input: CutData{Source: testSource, Transcript: src, Plan: &Plan{
				Version: PlanVersion, Keep: []Interval{{Start: 0, End: 100}},
			}},
			expected: ErrInvalidPlan,
		},
		{
			name:     "edited plan without intervals",
			input:    CutData{Source: testSource, Transcript: src, Plan: &Plan{Version: PlanVersion}},
			expected: ErrInvalidPlan,
		},
		{
			name:     "render fails",
			input:    CutData{Source: testSource, Transcript: src},
			media:    &fakeMedia{renderErr: errRender},
			expected: errRender,
		},
		{
			name:     "video without sound",
			input:    CutData{Source: testSource, Transcript: src},
			media:    &fakeMedia{info: &ffmpeg.MediaInfo{Duration: 5, HasVideo: true, AvgFrameRate: testRate}},
			expected: ErrNoAudio,
		},
		{
			name:     "audio only",
			input:    CutData{Source: testSource, Transcript: src},
			media:    &fakeMedia{info: &ffmpeg.MediaInfo{Duration: 5, HasAudio: true}},
			expected: ErrNoVideo,
		},
		{
			name:     "unknown frame rate",
			input:    CutData{Source: testSource, Transcript: src},
			media:    &fakeMedia{info: &ffmpeg.MediaInfo{Duration: 5, HasVideo: true, HasAudio: true}},
			expected: ErrNoFrameRate,
		},
		{
			name:     "invalid settings",
			input:    CutData{Source: testSource, Transcript: src, Settings: &Settings{MaxPause: 0.4, RetakeSimilarity: 2}},
			expected: ErrInvalidSettings,
		},
		{
			name: "edited plan with too many fragments",
			input: CutData{Source: testSource, Transcript: speech(strings.Repeat("слово | ", MaxSegments+1)), Plan: &Plan{
				Version: PlanVersion, Keep: manyIntervals(MaxSegments + 1),
			}},
			media: &fakeMedia{info: &ffmpeg.MediaInfo{
				Duration: MaxSegments + 2, HasVideo: true, HasAudio: true, AvgFrameRate: testRate,
			}},
			expected: ErrInvalidPlan,
		},
		{
			name: "edited plan shorter than a frame",
			input: CutData{Source: testSource, Transcript: src, Plan: &Plan{
				Version: PlanVersion, Keep: []Interval{{Start: 0, End: 0.01}},
			}},
			expected: ErrInvalidPlan,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			media := tt.media
			if media == nil {
				media = &fakeMedia{}
			}

			media.duration = src.Duration
			dir := t.TempDir()

			data := tt.input
			if data.OutDir == "" && !errors.Is(tt.expected, ErrEmptyOutDir) {
				data.OutDir = dir
			}

			_, err := newTestService(t, media).Cut(t.Context(), &data)

			require.ErrorIs(t, err, tt.expected)
			assert.NotContains(t, listDir(t, dir), VideoFile)

			if media.dst != "" {
				_, statErr := os.Stat(media.dst)
				assert.ErrorIs(t, statErr, os.ErrNotExist, "temp video is removed")
			}
		})
	}
}

func manyIntervals(n int) []Interval {
	keep := make([]Interval, 0, n)
	for i := range n {
		keep = append(keep, Interval{Start: float64(i), End: float64(i) + 0.5})
	}

	return keep
}

func TestNewServiceInvalidConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input *config.RoughCut
	}{
		{name: "zero pause", input: &config.RoughCut{RetakeSimilarity: 0.7}},
		{name: "similarity above one", input: &config.RoughCut{MaxPause: time.Second, RetakeSimilarity: 1.5}},
		{name: "zero similarity", input: &config.RoughCut{MaxPause: time.Second}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewService(&fakeMedia{}, tt.input, slog.New(slog.NewTextHandler(io.Discard, nil)))

			require.ErrorIs(t, err, ErrInvalidSettings)
		})
	}
}

// A video of an earlier run must not stay next to a new plan: neither in plan-only mode nor after a failed render.
func TestServiceCutRemovesOldVideo(t *testing.T) {
	t.Parallel()

	src := speech("раз два | три")

	tests := []struct {
		name     string
		planOnly bool
		media    *fakeMedia
	}{
		{name: "plan only", planOnly: true, media: &fakeMedia{}},
		{name: "render fails", media: &fakeMedia{renderErr: errRender}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			require.NoError(t, writeFile(filepath.Join(dir, VideoFile), []byte("old")))

			tt.media.duration = src.Duration
			_, _ = newTestService(t, tt.media).Cut(t.Context(), &CutData{
				Source: testSource, Transcript: src, OutDir: dir, PlanOnly: tt.planOnly,
			})

			assert.Equal(t, []string{LogFile, PlanFile, TranscriptFile}, listDir(t, dir))
		})
	}
}

// requireFFmpegEnv makes the render test fail instead of skipping when ffmpeg is missing (set in CI).
const requireFFmpegEnv = "FFMPEG_TESTS_REQUIRED"

func requireFFmpeg(t *testing.T) {
	t.Helper()

	if testFFmpegAvailable() {
		return
	}

	if os.Getenv(requireFFmpegEnv) != "" {
		t.Fatal("ffmpeg/ffprobe not found in PATH")
	}

	t.Skip("ffmpeg/ffprobe not found in PATH")
}

// TestRenderSynthetic is the end-to-end check of the acceptance criterion on a generated clip: a tone
// stands for every word of a synthetic transcript with a broken-off take, a filler and long pauses.
// The result must be as long as the plan says and have no silence longer than MaxPause.
func TestRenderSynthetic(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	src := speech("давайте при | давайте приступим || эээ к разминке || сначала шея | потом плечи || и спина")
	video := filepath.Join(t.TempDir(), "source.mp4")
	generateSpeechVideo(t, video, src)

	runner := ffmpeg.NewRunner(&config.FFmpeg{Timeout: 2 * time.Minute})
	dir := filepath.Join(t.TempDir(), "out")

	result, err := newTestService(t, runner).Cut(t.Context(), &CutData{Source: video, Transcript: src, OutDir: dir})
	require.NoError(t, err)

	assert.Equal(t, []string{LogFile, PlanFile, VideoFile, TranscriptFile}, listDir(t, dir))

	stat, err := os.Stat(result.Files.Video)
	require.NoError(t, err)
	assert.NotZero(t, stat.Mode().Perm()&0o004, "the video is readable by the app running as another user")

	assert.Equal(t, "давайте приступим к разминке сначала шея потом плечи и спина", joinWords(result.Transcript.Words))

	duration, err := runner.Duration(t.Context(), result.Files.Video)
	require.NoError(t, err)
	assert.InDelta(t, result.Plan.Duration, duration, 0.05, "picture and sound do not drift from the plan")
	assert.Less(t, duration, src.Duration-5)

	silences, err := runner.DetectSilences(t.Context(), result.Files.Video, ffmpeg.SilenceOptions{
		NoiseDB: -30, MinDuration: time.Duration(result.Plan.Settings.MaxPause * float64(time.Second)),
	})
	require.NoError(t, err)
	assert.Empty(t, silences, "pauses longer than %.1f s", result.Plan.Settings.MaxPause)

	// Every word of the remapped transcript must still sound where it says.
	for _, w := range result.Transcript.Words {
		assert.Less(t, w.End, duration+0.05, w.Word)
	}

	t.Logf("rough cut: %.2f s -> %.2f s (plan %.2f s), %d fragments\n%s",
		src.Duration, duration, result.Plan.Duration, len(result.Plan.Keep), result.Log)
}

// TestRenderVariableFrameRateSync: on a phone-like source (variable frame rate, audio starting later than
// the picture) the picture and the sound of every kept word start where the remapped transcript says,
// within a frame, after many joints. The same holds for a file whose timestamps start at 5 s
// (format start_time, as in some cameras' files): ffmpeg subtracts it itself, so it is not shifted twice.
func TestRenderVariableFrameRateSync(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	tests := []struct {
		name     string
		tsOffset string
	}{
		{name: "variable frame rate, delayed audio"},
		{name: "file timestamps start at 5 s", tsOffset: "5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checkSync(t, tt.tsOffset)
		})
	}
}

// checkSync cuts a generated sync clip and compares the onsets of sound and picture with the transcript.
func checkSync(t *testing.T, tsOffset string) {
	t.Helper()

	// Words start on frames the source has (n/30 s with n%5 != 2, shifted by the audio delay of 0.1 s),
	// so the picture can start exactly with the word.
	const (
		phrases     = 12
		firstFrame  = 15
		phraseStep  = 60 // frames
		secondWord  = 14 // frames after the first word
		sourceFPS   = 30.0
		wordLen     = 0.3
		audioDelay  = 0.1
		phraseCount = 2
	)

	names := []string{
		"альфа", "бета", "гамма", "дельта", "эпсилон", "дзета", "эта", "тета", "йота", "каппа", "лямбда", "мю",
		"ню", "кси", "омикрон", "пи", "ро", "сигма", "тау", "ипсилон", "фи", "хи", "пси", "омега",
	}

	spans := make([][2]float64, 0, phrases*phraseCount)
	for k := range phrases {
		for _, n := range []int{firstFrame + k*phraseStep, firstFrame + k*phraseStep + secondWord} {
			start := float64(n)/sourceFPS - audioDelay
			spans = append(spans, [2]float64{start, start + wordLen})
		}
	}

	duration := spans[len(spans)-1][1] + 1
	video := filepath.Join(t.TempDir(), "vfr.mp4")
	generateSyncVideo(t, video, spans, duration)

	if tsOffset != "" {
		shifted := filepath.Join(t.TempDir(), "shifted.mp4")
		remuxWithOffset(t, video, shifted, tsOffset)
		video = shifted
	}

	runner := ffmpeg.NewRunner(&config.FFmpeg{Timeout: 2 * time.Minute})
	info, err := runner.Probe(t.Context(), video)
	require.NoError(t, err)

	// The transcript counts from the first decoded audio sample, as Whisper does on the extracted audio.
	shift := audioDelay - (info.AudioStart - info.FormatStart)
	src := &transcription.Result{Language: testLanguage, Duration: duration}

	for i, s := range spans {
		src.Words = append(src.Words, transcription.Word{Word: names[i], Start: roundTime(s[0] + shift), End: roundTime(s[1] + shift)})
	}

	result, err := newTestService(t, runner).Cut(t.Context(), &CutData{
		Source: video, Transcript: src, OutDir: filepath.Join(t.TempDir(), "out"),
	})
	require.NoError(t, err)
	require.Len(t, result.Plan.Keep, phrases)

	outDuration, err := runner.Duration(t.Context(), result.Files.Video)
	require.NoError(t, err)

	audio, picture := onsets(t, result.Files.Video)
	tolerance := 1/result.Plan.FrameRate + 0.01

	for name, got := range map[string][]float64{"sound": audio, "picture": picture} {
		got = dropTail(got, outDuration)
		require.Len(t, got, len(result.Transcript.Words), name)

		for i, w := range result.Transcript.Words {
			assert.InDelta(t, w.Start, got[i], tolerance, "%s of %q", name, w.Word)
		}
	}

	t.Logf("source: avg %s fps, r %s fps, format start %.3f s, audio start %.3f s; result %.3f fps, %d fragments, %.2f s",
		info.AvgFrameRate, info.RealFrameRate, info.FormatStart, info.AudioStart, result.Plan.FrameRate,
		len(result.Plan.Keep), outDuration)
}

// dropTail removes the end-of-stream markers ffmpeg prints when a clip ends in silence or black.
func dropTail(values []float64, duration float64) []float64 {
	out := make([]float64, 0, len(values))

	for _, v := range values {
		if v < duration-0.06 {
			out = append(out, v)
		}
	}

	return out
}
