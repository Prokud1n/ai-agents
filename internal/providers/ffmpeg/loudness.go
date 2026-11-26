package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The loudness of a draft is fitted before the render (T3.19b): the gain by the measurement of the source misses the
// target, because the limiter after it cuts the peaks (phone speech peaks 13–20 dB above its loudness) and the cut
// leaves out pauses and noise. A pass over the sound of the cut alone — the same segments, fades and chain, no
// picture: seconds — measures what the chain gives, and the gain is corrected by the miss; the limiter takes a part of
// a correction too, so it is repeated while the miss is above loudnessTolerance.
const (
	// loudnessTolerance (LU): a miss this small is not corrected.
	loudnessTolerance = 0.1
	// maxLoudnessPasses of the sound alone before the render.
	maxLoudnessPasses = 3
	// maxCorrection (dB): the correction of the gain by the passes is held within ±maxCorrection. The limiter and the
	// cut move the loudness by a few dB (1–4 dB on the trainer's videos); a correction that reaches the bound means
	// the sound of the cut is not like the measured source (almost silent, music, a broken measurement): the render
	// goes on with the bound and the report says so (SoundReport.CorrectionCapped), the result may miss the target.
	maxCorrection = 12.0
	// minLoudnessSlope: a correction is at most the miss times 1/minLoudnessSlope.
	minLoudnessSlope = 0.25
	// maxFilePeak (dBTP) of the sound of the rendered file. The limiter holds the true peak at the target (-2 dBTP,
	// oversampled), but the AAC encoder can overshoot it by several dB on bright bursts (sibilants): the sound alone is
	// then made again with the ceiling of the limiter lower by the overshoot and peakMargin (the same gain; the video is
	// copied; seconds), at most peakRetries times (capFilePeak).
	maxFilePeak = -1.0
	peakMargin  = 0.5
	peakRetries = 2
	// maxPeakCut (dB): the ceiling of the limiter is never lowered more than this after the encode: the limiter would
	// press the speech itself and the loudness would miss the target.
	maxPeakCut    = 3.0
	peakSuffix    = ".peak.mp4"
	measuredLabel = "m"
	peakLabel     = "p"
)

var errNoSummary = errors.New("ffmpeg reported no loudness summary")

// SoundReport is what the render did to the loudness of the sound.
type SoundReport struct {
	// Gain is the gain applied, dB, of which Correction came from the passes over the sound of the cut (Passes).
	Gain       float64
	Correction float64
	Passes     int
	// FitError: the passes over the sound of the cut failed, the render went on with the gain by the measurement of the
	// source (Correction 0); nil if they did not fail.
	FitError error
	// CorrectionCapped: the correction reached ±maxCorrection, the loudness may miss the target.
	CorrectionCapped bool
	// PeakCut (dB) is how much lower the ceiling of the limiter was made after the encode, because the AAC encoder
	// overshot maxFilePeak (capFilePeak); 0 if it did not.
	PeakCut float64
	// PeakError: lowering the peaks after the encode failed; the file is the render as it was (its true peak is in
	// Output). Nil if it did not fail.
	PeakError error
	// Output is the loudness of the rendered file (after the AAC encode: its true peak is what the network gets),
	// measured by one more pass over its sound; nil if it could not be measured.
	Output *Loudness
}

// measureFile measures the loudness of the first audio stream of a local file; nil if it fails (only a report).
func (r *Runner) measureFile(ctx context.Context, path string) *Loudness {
	args := append(commonArgs(), inputArgs(path)...)
	args = append(args, mapFlag, "0:a:0", audioFilterFlag, measureFilter, formatFlag, "null", "-")

	log := newTailBuffer(measureLogTail)
	if _, err := r.run(ctx, r.ffmpegPath, args, log); err != nil {
		return nil
	}

	return parseLoudness(log.String())
}

// fitLoudness returns the options with the gain corrected so that the processed sound of the cut is at the target,
// and the number of passes it took. Without a measured target (loudnorm adjusts itself) or with a muted sound the
// options are returned as they are, with no pass.
func (r *Runner) fitLoudness(ctx context.Context, in string, segments []Segment, opts RenderOptions) (RenderOptions, int, error) {
	if opts.Mute || opts.Loudness == nil || opts.Loudness.Measured == nil {
		return opts, 0, nil
	}

	out := opts
	target := *opts.Loudness
	passes, slope := 0, 1.0
	lastLevel, lastCorrection := 0.0, 0.0

	for passes < maxLoudnessPasses {
		attempt := target
		out.Loudness = &attempt

		measured, err := r.measureCut(ctx, in, segments, out)
		if err != nil {
			return opts, passes, err
		}

		passes++

		miss := target.Integrated - measured.Integrated
		if measured.Integrated <= minLevel || math.Abs(miss) <= loudnessTolerance {
			break
		}

		// The limiter takes a part of every dB of gain: the next correction is the miss divided by how much the last
		// one moved the loudness (the secant), within minLoudnessSlope..1.
		if step := attempt.Correction - lastCorrection; passes > 1 && step != 0 {
			slope = min(max((measured.Integrated-lastLevel)/step, minLoudnessSlope), 1)
		}

		lastLevel, lastCorrection = measured.Integrated, attempt.Correction
		target.Correction = min(max(target.Correction+miss/slope, -maxCorrection), maxCorrection)
	}

	out.Loudness = &target

	return out, passes, nil
}

// measureCut measures the loudness of the processed sound of the cut: the audio of the source alone, decoded whole
// (cheap next to the picture), cut, joined and processed as the render does it.
func (r *Runner) measureCut(ctx context.Context, in string, segments []Segment, opts RenderOptions) (*Loudness, error) {
	log := newTailBuffer(measureLogTail)

	err := r.runSoundGraph(ctx, soundGraph(segments, opts), inputArgs(in),
		[]string{mapFlag, "[" + measuredLabel + "]", formatFlag, "null", "-"}, log)
	if err != nil {
		return nil, fmt.Errorf("failed to measure the sound of the cut: %w", err)
	}

	measured := parseLoudness(log.String())
	if measured == nil {
		return nil, fmt.Errorf("failed to measure the sound of the cut: %w", errNoSummary)
	}

	return measured, nil
}

// runSoundGraph runs ffmpeg with the inputs, the filter graph (through a file in a temp dir: hundreds of segments do
// not fit an argument) and the output arguments.
func (r *Runner) runSoundGraph(ctx context.Context, graph string, inputs, output []string, log io.Writer) error {
	dir, err := os.MkdirTemp("", filterDirPattern)
	if err != nil {
		return fmt.Errorf("failed to create filter dir: %w", err)
	}

	defer func() { _ = os.RemoveAll(dir) }() // best effort: a few bytes in the temp dir

	path := filepath.Join(dir, filterFileName)
	if err := os.WriteFile(path, []byte(graph), filterFileMode); err != nil {
		return fmt.Errorf("failed to write filter graph: %w", err)
	}

	major, err := r.majorVersion(ctx)
	if err != nil {
		return err
	}

	args := append(commonArgs(), inputs...)
	args = append(args, filterFileArgs(path, major)...)
	args = append(args, output...)

	if _, err := r.run(ctx, r.ffmpegPath, args, log); err != nil {
		return fmt.Errorf("failed to run the sound graph: %w", err)
	}

	return nil
}

// soundGraph is the sound of segmentsGraph from one input (the whole source), processed and measured:
//
//	[0:a:0]asetpts=PTS,asplit=2[t0][t1];[t0]atrim=…[a0];[t1]atrim=…[a1];[a0][a1]concat=n=2:v=0:a=1[j];
//	[j]<soundChain>,ebur128=peak=true:framelog=verbose[m]
func soundGraph(segments []Segment, opts RenderOptions) string {
	return cutSoundGraph(segments, opts, measureFilter, measuredLabel)
}

// cutSoundGraph is the sound of segmentsGraph from one input, processed by soundChain and then by tail (none if
// empty), into [label].
func cutSoundGraph(segments []Segment, opts RenderOptions, tail, label string) string {
	var b strings.Builder

	whole := inputGroup{first: 0, last: len(segments) - 1, shift: opts.Offset}
	writeAudioBranches(&b, whole, segments, opts)

	for i := range segments {
		b.WriteString("[a" + strconv.Itoa(i) + "]")
	}

	filters := make([]string, 0, 2)
	for _, f := range []string{soundChain(opts), tail} {
		if f != "" {
			filters = append(filters, f)
		}
	}

	if len(filters) == 0 {
		filters = append(filters, "anull")
	}

	b.WriteString("concat=n=" + strconv.Itoa(len(segments)) + ":v=0:a=1[" + soundLabel + "];[" + soundLabel + "]")
	b.WriteString(strings.Join(filters, ",") + "[" + label + "]")

	return b.String()
}

// capFilePeak brings the true peak of the rendered file (path) under maxFilePeak when the AAC encoder pushed it over:
// the sound of the cut is made again from the source (src, a local URL) with the ceiling of the limiter lower by the
// overshoot and peakMargin (by at most maxPeakCut in all) and the gain fitted again (fitLoudness, from the gain of the
// render: a lower ceiling presses the speech too, 1.5 dB of it took 0.9 LU of a dense speech-like sound), encoded and
// joined with the picture of the file (copied, +faststart). So the loudness stays at the target (a volume change of
// the whole sound took it under -14.5 LUFS). A failed fitting pass leaves the gain of the render. The report is
// updated (PeakCut, the gain, Output); the file is replaced only by a complete one, the temporary file is removed in
// any case.
func (r *Runner) capFilePeak(ctx context.Context, src, path string, cut *Cut, report *SoundReport) error {
	if cut == nil || cut.Options.Loudness == nil {
		return nil
	}

	out, tmp, err := localFiles(path, path+peakSuffix)
	if err != nil {
		return err
	}

	defer func() { _ = os.Remove(path + peakSuffix) }() // best effort: gone after a successful replace

	correction := cut.Options.Loudness.Correction

	for range peakRetries {
		if report.Output == nil || report.Output.TruePeak <= maxFilePeak {
			return nil
		}

		lower := min(report.Output.TruePeak-maxFilePeak+peakMargin, maxPeakCut-report.PeakCut)
		if lower <= 0 {
			return nil
		}

		opts, err := r.refitPeak(ctx, src, cut, correction, report.PeakCut+lower, report)
		if err != nil {
			return err
		}

		correction = opts.Loudness.Correction

		if err := r.resound(ctx, src, out, tmp, cut.Segments, opts); err != nil {
			return err
		}

		if err := os.Rename(path+peakSuffix, path); err != nil {
			return fmt.Errorf("failed to replace the render: %w", err)
		}

		report.PeakCut += lower
		report.Output = r.measureFile(ctx, out)
	}

	return nil
}

// refitPeak is the options of the cut with the ceiling of the limiter lower by below and the gain fitted again from
// correction; the report gets the new gain. Only a cancelled context is an error: a failed pass keeps correction.
func (r *Runner) refitPeak(
	ctx context.Context,
	src string,
	cut *Cut,
	correction, below float64,
	report *SoundReport,
) (RenderOptions, error) {
	opts := cut.Options
	target := *opts.Loudness
	target.TruePeak -= below
	target.Correction = correction
	opts.Loudness = &target

	fitted, passes, err := r.fitLoudness(ctx, src, cut.Segments, opts)
	if err != nil && ctx.Err() != nil {
		return RenderOptions{}, fmt.Errorf("failed to fit the loudness under the lower ceiling: %w", err)
	}

	if err == nil {
		opts = fitted
		report.Passes += passes
	}

	report.Gain, report.Correction = opts.Loudness.Gain(), opts.Loudness.Correction
	report.CorrectionCapped = correctionCapped(opts.Loudness.Correction)

	return opts, nil
}

// correctionCapped: the correction reached its bound (fitLoudness clamps it to ±maxCorrection).
func correctionCapped(correction float64) bool {
	return math.Abs(correction) >= maxCorrection
}

// resound makes the sound of the cut from the source (src) with opts and joins it with the picture of the rendered
// file (out, copied) into tmp, +faststart.
func (r *Runner) resound(ctx context.Context, src, out, tmp string, segments []Segment, opts RenderOptions) error {
	inputs := append(inputArgs(src), inputArgs(out)...)

	err := r.runSoundGraph(ctx, cutSoundGraph(segments, opts, "", peakLabel), inputs,
		[]string{mapFlag, "1:v:0", mapFlag, "[" + peakLabel + "]", videoCodecFlag, "copy", audioCodecFlag, renderACodec,
			"-b:a", outAudioBits, sampleRateFlag, outAudioRate, channelsFlag, outChannels, "-movflags", "+faststart",
			formatFlag, renderFormat, tmp}, nil)
	if err != nil {
		return fmt.Errorf("failed to lower the peaks of the sound: %w", err)
	}

	return nil
}
