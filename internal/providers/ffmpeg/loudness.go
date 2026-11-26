package ffmpeg

import (
	"context"
	"errors"
	"fmt"
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
	// maxCorrection (dB) of the gain by the measurement: a larger miss is not the limiter, the measurement is wrong.
	maxCorrection = 12.0
	// minLoudnessSlope: a correction is at most the miss times 1/minLoudnessSlope.
	minLoudnessSlope = 0.25
	// maxFilePeak (dBTP) of the sound of the rendered file. The limiter holds the true peak at the target (-2 dBTP,
	// oversampled), but the AAC encoder can overshoot it by several dB on bright bursts (sibilants): the sound alone is
	// then encoded again, lower by the overshoot and peakMargin (the video is copied; seconds), at most peakRetries
	// times.
	maxFilePeak = -1.0
	peakMargin  = 0.5
	peakRetries = 2
	// maxPeakCut (dB): the sound is never lowered more than this after the encode, the loudness would miss the target.
	maxPeakCut    = 3.0
	peakSuffix    = ".peak.mp4"
	measuredLabel = "m"
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
	// PeakCut (dB) is how much the sound of the rendered file was lowered after the encode: the AAC encoder overshot
	// maxFilePeak (capFilePeak); 0 if it did not.
	PeakCut float64
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
	dir, err := os.MkdirTemp("", filterDirPattern)
	if err != nil {
		return nil, fmt.Errorf("failed to create filter dir: %w", err)
	}

	defer func() { _ = os.RemoveAll(dir) }() // best effort: a few bytes in the temp dir

	graph := filepath.Join(dir, filterFileName)
	if err := os.WriteFile(graph, []byte(soundGraph(segments, opts)), filterFileMode); err != nil {
		return nil, fmt.Errorf("failed to write filter graph: %w", err)
	}

	major, err := r.majorVersion(ctx)
	if err != nil {
		return nil, err
	}

	args := append(commonArgs(), inputArgs(in)...)
	args = append(args, filterFileArgs(graph, major)...)
	args = append(args, mapFlag, "["+measuredLabel+"]", formatFlag, "null", "-")

	log := newTailBuffer(measureLogTail)
	if _, err := r.run(ctx, r.ffmpegPath, args, log); err != nil {
		return nil, fmt.Errorf("failed to measure the sound of the cut: %w", err)
	}

	measured := parseLoudness(log.String())
	if measured == nil {
		return nil, fmt.Errorf("failed to measure the sound of the cut: %w", errNoSummary)
	}

	return measured, nil
}

// soundGraph is the sound of segmentsGraph from one input (the whole source), processed and measured:
//
//	[0:a:0]asetpts=PTS,asplit=2[t0][t1];[t0]atrim=…[a0];[t1]atrim=…[a1];[a0][a1]concat=n=2:v=0:a=1[j];
//	[j]<soundChain>,ebur128=peak=true:framelog=verbose[m]
func soundGraph(segments []Segment, opts RenderOptions) string {
	var b strings.Builder

	whole := inputGroup{first: 0, last: len(segments) - 1, shift: opts.Offset}
	writeAudioBranches(&b, whole, segments, opts)

	for i := range segments {
		b.WriteString("[a" + strconv.Itoa(i) + "]")
	}

	b.WriteString("concat=n=" + strconv.Itoa(len(segments)) + ":v=0:a=1[" + soundLabel + "];[" + soundLabel + "]")
	b.WriteString(soundChain(opts) + "," + measureFilter + "[" + measuredLabel + "]")

	return b.String()
}

// capFilePeak lowers the sound of the rendered file (a local path) when its true peak after the encode is above
// maxFilePeak, by at most maxPeakCut in all, and updates the report (PeakCut, Output). The file is replaced only by a
// complete one.
func (r *Runner) capFilePeak(ctx context.Context, path string, report *SoundReport) error {
	for range peakRetries {
		if report.Output == nil || report.Output.TruePeak <= maxFilePeak {
			return nil
		}

		cut := min(report.Output.TruePeak-maxFilePeak+peakMargin, maxPeakCut-report.PeakCut)
		if cut <= 0 {
			return nil
		}

		in, tmp, err := localFiles(path, path+peakSuffix)
		if err != nil {
			return err
		}

		args := append(commonArgs(), inputArgs(in)...)
		args = append(args, mapFlag, "0:v:0", mapFlag, "0:a:0", videoCodecFlag, "copy",
			audioFilterFlag, "volume=-"+formatLevel(cut)+"dB", audioCodecFlag, renderACodec, "-b:a", outAudioBits,
			sampleRateFlag, outAudioRate, channelsFlag, outChannels, "-movflags", "+faststart", formatFlag, renderFormat, tmp)

		if _, err := r.run(ctx, r.ffmpegPath, args, nil); err != nil {
			return fmt.Errorf("failed to lower the peaks of the sound: %w", err)
		}

		if err := os.Rename(path+peakSuffix, path); err != nil {
			return fmt.Errorf("failed to replace the render: %w", err)
		}

		report.PeakCut += cut
		report.Output = r.measureFile(ctx, in)
	}

	return nil
}
