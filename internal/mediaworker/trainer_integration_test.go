package mediaworker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/editor"
	"github.com/Prokud1n/ai-agents/internal/editplan"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/redistest"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/s3test"
	"github.com/Prokud1n/ai-agents/internal/tasks"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const (
	// longPhrases of the long synthetic talking head: two words each, 2 s apart (the pauses of 0.3 s stay), so the
	// cleaned cut is about 72 s of a 74 s source — longer than the default 60 s with its margin (66 s).
	longPhrases  = 36
	longStep     = 2.0
	longDuration = 74
	// longKept: the model keeps the first 20 phrases (about 40 s), within the 30–60 s of the default target.
	longKept = 20
	// trainerWait bounds one version of the long reel (the render of about 40 s at 1080×1920).
	trainerWait  = 5 * time.Minute
	trainerHook  = "развеиваю страхи об онлайн-тренировках"
	trainerLeft  = "tg: @ana_prokudina"
	trainerRight = "2x"
	// sectionShownFor: a frame this long after a heading starts has it (it stays 2.5–4 s); sectionGoneAfter: one this
	// long after has none (the next heading is 6 phrases later).
	sectionShownFor  = 1.0
	sectionGoneAfter = 5.0
	// sectionBandFrom..sectionBandTo: the rows of the headings in the trainer's style.
	sectionBandFrom = 372
	sectionBandTo   = 450
)

// The model's decisions: the selection with the hook and a heading on each of three points (the words 4, 16 and 28
// start phrases 2, 8 and 14), and the revision that removes the headings.
var (
	trainerSelection = `{"decision": "edit", "reason": "заголовок, три страха и финал", "hook_title": "` + trainerHook + `",
		"screen_notes": [
			{"kind": "section", "text": "страх 1", "word": 4},
			{"kind": "section", "text": "страх 2", "word": 16},
			{"kind": "section", "text": "страх 3", "word": 28}
		],
		"remove_words": [{"from": ` + strconv.Itoa(2*longKept) + `, "to": ` + strconv.Itoa(2*longPhrases-1) + `}]}`
	trainerRemoveSections = `{"decision": "edit", "reason": "убери разделы", "screen_notes": []}`
)

// longTalkTranscript is the transcript of the long synthetic talking head: a segment per phrase, all words different.
func longTalkTranscript() *transcription.Result {
	result := &transcription.Result{Language: testLanguage, Duration: longDuration}

	for p := range longPhrases {
		start := talkStart + float64(p)*longStep
		first := transcription.Word{Word: "мысль" + strconv.Itoa(p), Start: start, End: start + talkWordLen}
		second := transcription.Word{
			Word: "довод" + strconv.Itoa(p), Start: first.End + talkWordGap, End: first.End + talkWordGap + talkWordLen,
		}

		result.Words = append(result.Words, first, second)
		result.Segments = append(result.Segments, transcription.Segment{
			Start: first.Start, End: second.End, Text: " " + first.Word + " " + second.Word + ".",
		})
	}

	return result
}

// longTalkTranscriber is the speech recognition of the long talking head that counts its calls.
type longTalkTranscriber struct {
	calls atomic.Int32
}

func (c *longTalkTranscriber) Transcribe(context.Context, transcription.TranscribeData) (*transcription.Result, error) {
	c.calls.Add(1)

	return longTalkTranscript(), nil
}

// trainerModel is the editor's model behind the real decider: the selection of the first version, the revision that
// removes the headings.
type trainerModel struct {
	mu      sync.Mutex
	prompts []string
}

func (m *trainerModel) Run(ctx context.Context, data *agents.RunAgentData) (*agents.Result, error) {
	m.mu.Lock()
	m.prompts = append(m.prompts, data.Input)
	m.mu.Unlock()

	input := trainerSelection
	if strings.Contains(data.Input, "<trainer_revision>") {
		input = trainerRemoveSections
	}

	answer, err := editplan.NewSubmitTool().Execute(ctx, json.RawMessage(input))
	if err != nil {
		return nil, err
	}

	return &agents.Result{Text: answer}, nil
}

func (m *trainerModel) Prompts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]string(nil), m.prompts...)
}

// startTrainerEnv is the editor of the app with the defaults of config.Editor (the target of 60 s, the hook on) and
// the top line, and the media worker in the default (the trainer's) subtitle style, through a real Redis and S3.
func startTrainerEnv(t *testing.T, model modelRunner) (*editEnv, *longTalkTranscriber) {
	t.Helper()

	requireIntegrationFFmpeg(t)

	log := slog.New(slog.DiscardHandler)
	client := redistest.Connect(t)

	files, err := media.NewStore(s3test.Connect(t),
		&config.Media{KeyPrefix: testKeyPrefix, ResultPrefix: testResultPrefix, UploadTimeout: time.Minute}, log)
	require.NoError(t, err)

	var cfg config.Editor
	require.NoError(t, env.ParseWithOptions(&cfg, env.Options{Environment: map[string]string{
		"REELS_TOPBAR_LEFT": trainerLeft, "REELS_TOPBAR_RIGHT": trainerRight,
	}}))

	cfg.Stream, cfg.JobTimeout, cfg.PollInterval = redistest.Prefix(t, client)+"editor:jobs", trainerWait, time.Second

	board, err := editjob.NewBoard(client, &cfg, log)
	require.NoError(t, err)

	words := longTalkTranscript().Words
	spans := make([]roughcut.Interval, 0, len(words))

	for _, w := range words {
		spans = append(spans, roughcut.Interval{Start: w.Start, End: w.End})
	}

	clip := filepath.Join(t.TempDir(), "long.mkv")
	makeToneClip(t, clip, longDuration, spans)

	f, err := os.Open(clip)
	require.NoError(t, err)

	t.Cleanup(func() { _ = f.Close() })

	source, err := files.Save(t.Context(), &media.File{
		ChatID: clipChat, MessageID: clipMsgID, FileUniqueID: "trainer", MIMEType: matroskaType,
	}, f)
	require.NoError(t, err)

	speech := &longTalkTranscriber{}
	envEdit := &editEnv{
		files: files, source: source,
		executor: editor.NewExecutor(board, files, editplan.NewDecider(model, log), noTech{}, &cfg, log),
	}

	worker := NewWorker(board, realPipelineStyled(t, files, speech, ""), &config.MediaWorker{WorkDir: t.TempDir()}, log)
	require.NoError(t, worker.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, worker.Stop(context.Background())) })

	return envEdit, speech
}

// runLong runs the task like the task workers do, within trainerWait.
func runLong(t *testing.T, e *editEnv, data *tasks.ExecuteData) tasks.Output {
	t.Helper()

	deadline := time.Now().Add(trainerWait)

	for {
		out, err := e.executor.Execute(t.Context(), data)

		var deferred *tasks.DeferredError
		if !errors.As(err, &deferred) {
			require.NoError(t, err)

			return out
		}

		require.True(t, time.Now().Before(deadline), "the edit did not finish in %s", trainerWait)
		time.Sleep(editPoll)
	}
}

// TestIntegrationTrainerStyle: with the defaults of the app a long talking head (68 s after the cleaning, no caption)
// stops for the editor's selection under the default target of 60 s; the model (through the real decider and its
// checks) keeps the main points with the hook and a section heading on each, the worker renders them in the trainer's
// style (the default) within 30–60 s. The revision «убери разделы» makes the same cut without the headings: the
// reels differ in the band of the headings only while one is shown.
func TestIntegrationTrainerStyle(t *testing.T) {
	t.Parallel()

	model := &trainerModel{}
	e, speech := startTrainerEnv(t, model)
	first := e.data(46, "")

	v1 := runLong(t, e, first)
	assert.Equal(t, int32(1), speech.calls.Load())
	require.Len(t, model.Prompts(), 1, "the long talking head is given to the model for the selection")
	assert.Contains(t, model.Prompts()[0], "Сделай рилс 30–60 с: оставь главное по структуре")
	assert.Contains(t, v1.Text, "сократил под рилс")
	assert.Contains(t, v1.Text, "Заголовок: «"+trainerHook+"».")
	assert.Contains(t, v1.Text, "На экране: 3 раздела.")

	duration := e.reelDuration(t, v1.MediaKey)
	assert.InDelta(t, longKept*longStep, duration, 2.5, "twenty phrases of thirty-six")
	assert.LessOrEqual(t, duration, time.Minute.Seconds()*(1+talkTolerance))

	files1, err := editjob.Files(e.files, e.source, 1)
	require.NoError(t, err)

	var plan roughcut.Plan
	require.NoError(t, json.Unmarshal(e.read(t, files1.Plan), &plan))
	require.NotNil(t, plan.Edit)

	sections := roughcut.RemapScreenNotes(plan.Edit.ScreenNotes, plan.Keep)
	require.Len(t, sections, 3)
	assert.Equal(t, roughcut.SectionNote, sections[0].Kind)

	revision := *first
	revision.Version, revision.Revision = 2, "убери разделы"

	v2 := runLong(t, e, &revision)
	assert.Equal(t, int32(1), speech.calls.Load(), "the revision is not transcribed again")
	assert.Contains(t, v2.Text, "Убрал пункты и цифры с экрана.")
	assert.InDelta(t, duration, e.reelDuration(t, v2.MediaKey), 0.05, "the cut is the same")

	// The zoom moves closer at the start of each section (T3.25): the sections of the real rough cut reach Zoom.Must,
	// the log of v1 says so. The selection here is one fragment (the first 20 phrases with their short pauses), so the
	// reel has no joint to change the framing at; the rule of the joints is checked by ffmpeg.TestZoomedSegments.
	log1 := string(e.read(t, files1.Decisions))
	assert.Contains(t, log1, "разделов: 3", "the sections of the real rough cut reach the zoom")
	assert.Regexp(t, `масштаб меняется на \d+ из \d+ стыков`, log1)

	files2, err := editjob.Files(e.files, e.source, 2)
	require.NoError(t, err)

	var plan2 roughcut.Plan
	require.NoError(t, json.Unmarshal(e.read(t, files2.Plan), &plan2))

	// Without the headings the revision is zoomed at other joints: the reels are compared where both have the same
	// framing, so they differ by the heading only.
	withZoom, plainZoom := trainerZoom(&plan, sections), trainerZoom(&plan2, nil)
	start := sections[0].Start
	// The heading stays at least 2.5 s; if the framings differ all that time (v1 moves closer on the section), the
	// heading is compared as before: the difference only grows with the zoom.
	shown := sameFraming(withZoom, plainZoom, start+sectionShownFor/2, start+sectionShownFor*2.4)
	if math.IsNaN(shown) {
		shown = start + sectionShownFor
	}

	gone := sameFraming(withZoom, plainZoom, start+sectionGoneAfter, start+sectionGoneAfter*2)
	t.Logf("trainer style: section at %.2f s, compared at %.2f s (shown) and %.2f s (gone)", start, shown, gone)

	withSections, plain := e.read(t, v1.MediaKey), e.read(t, v2.MediaKey)

	require.False(t, math.IsNaN(gone), "a moment after the heading with the same framing in both reels")
	assert.Greater(t, meanDiff(reelFrame(t, withSections, shown), reelFrame(t, plain, shown), sectionBandFrom,
		sectionBandTo), hookDiff, "the heading is drawn in the reel with it only")
	assert.Less(t, meanDiff(reelFrame(t, withSections, gone), reelFrame(t, plain, gone), sectionBandFrom,
		sectionBandTo), sameDiff, "after the heading the reels are the same")
}

// zoomJointMargin: a compared frame is this far from a joint of the reel, seconds (the lengths here are not on frames).
const zoomJointMargin = 0.2

// reelZoom is the framing of a reel: its segments (seconds of the reel) and which of them are closer.
type reelZoom struct {
	ends   []float64
	zoomed []bool
}

// trainerZoom is the framing the worker gives the plan with its defaults (MEDIA_WORKER_ZOOM, the sections of the edit).
func trainerZoom(plan *roughcut.Plan, sections []roughcut.ScreenNote) reelZoom {
	cut := &ffmpeg.Cut{
		Segments: (&roughcut.Result{Plan: *plan}).Segments(),
		Zoom: &ffmpeg.Zoom{
			Factor: 1 + testZoom/percent, MinSegment: minZoomSegment, Every: zoomEvery, Must: sectionStarts(sections),
		},
	}

	ends, at := make([]float64, 0, len(cut.Segments)), 0.0
	for _, s := range cut.Segments {
		at += s.End - s.Start
		ends = append(ends, at)
	}

	return reelZoom{ends: ends, zoomed: cut.ZoomedSegments()}
}

// at is the framing at the second of the reel and whether a joint is within zoomJointMargin of it.
func (z reelZoom) at(second float64) (zoomed, nearJoint bool) {
	for i, end := range z.ends {
		if second < end {
			start := 0.0
			if i > 0 {
				start = z.ends[i-1]
			}

			return z.zoomed[i], second-start < zoomJointMargin || end-second < zoomJointMargin
		}
	}

	return false, true
}

// sameFraming is the first moment within [from, to] (by 0.1 s) where both reels have the same framing, away from the
// joints; the test fails without one.
func sameFraming(a, b reelZoom, from, to float64) float64 {
	const step = 0.1

	for second := from; second <= to; second += step {
		za, nearA := a.at(second)
		zb, nearB := b.at(second)

		if za == zb && !nearA && !nearB {
			return second
		}
	}

	return math.NaN()
}
