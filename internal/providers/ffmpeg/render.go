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
	"unicode"
)

// Rendered video: H.264 + AAC in MP4, the picture size and orientation of the source are kept (no scaling;
// a phone's rotation tag is applied by the decoder, so a vertical clip stays 9:16).
const (
	videoCodec     = "libx264"
	videoPreset    = "veryfast"
	videoCRF       = "20" // visually lossless for a draft that is cut again later
	videoPixFmt    = "yuv420p"
	renderACodec   = "aac"
	renderABitrate = "192k"
	renderFormat   = "mp4"

	filterPrecision = 6 // microseconds: frame edges like 1/30 s must not be rounded to milliseconds
	// frameTolerance (of an output frame) is how much earlier the video is moved before fps with round=up: a frame
	// stored a little after its time (millisecond or microsecond timestamps: 16/60 s is 0.267 s in Matroska) still
	// lands on its own output frame instead of the next one, and is shown at most 1/8 frame early.
	frameTolerance = 0.125

	filterDirPattern = "ffmpeg-filter-*"
	filterFileName   = "graph.txt"
	filterFileMode   = 0o600

	// ffmpeg 7 reads an option value from a file with "-/option file"; older versions only know
	// -filter_complex_script, which ffmpeg 7 deprecated.
	fileOptionMajor   = 7
	versionPrefix     = "ffmpeg version "
	frameRateDivider  = "/"
	unknownMajor      = 0
	versionHeadSize   = 1 << 10
	decimalBase       = 10
	int64Bits         = 64
	developmentMajor  = 1 << 10 // git builds ("N-12345-g…") are newer than any release
	developmentPrefix = "N-"
)

var (
	errNoSegments   = errors.New("no segments to render")
	errBadFrameRate = errors.New("bad frame rate")
)

// Segment is a fragment of the source kept in the render, in seconds of the audio timeline (see Offset).
type Segment struct {
	Start float64
	End   float64
}

type RenderOptions struct {
	// Fade is the audio fade in and out of every segment, in seconds, against clicks at the joints.
	Fade float64
	// FrameRate, if known, converts the video to this constant frame rate before cutting: frames then
	// lie on the grid k/FrameRate of the audio timeline, a variable frame rate phone video included.
	// Segment edges are rounded to that grid: on it each segment holds exactly as many frames as its audio
	// lasts, and the picture does not drift from the sound over many joints.
	FrameRate Rational
	// Offset is where the first audio sample is in the filter graph: MediaInfo.AudioStart minus
	// MediaInfo.FormatStart (ffmpeg has already subtracted the latter). Segment times count from the first
	// audio sample, as the transcript does, so both streams are shifted by it before cutting.
	Offset float64
	// Mute silences the sound of the source: the audio track stays (silent), so the export has the same streams.
	// A muted sound is neither denoised nor normalized.
	Mute bool
	// Denoise, if set, lightly reduces the noise of the joined sound (speech): the rumble and the stationary hiss.
	Denoise *Denoise
	// Loudness, if set, brings the joined sound to it after the noise reduction; nil keeps the level of the source.
	Loudness *LoudnessTarget
	// Source is the size of the decoded frame of the source: it limits the number of inputs of a cut (inputLimit).
	// Nil: unknown, the limit is maxCutInputs.
	Source *SourceFrame
}

// SourceFrame is the frame of the source as its decoder holds it.
type SourceFrame struct {
	Width  int
	Height int
	// BytesPerSample: 1 for 8-bit video, 2 for 10- and 12-bit.
	BytesPerSample int
}

// RenderSegments re-encodes the given fragments of src one after another into dst (MP4 regardless of its
// extension). Cuts are frame- and sample-accurate: the source is decoded and trimmed, never stream-copied
// from keyframes. The filter graph goes through a temporary file, so hundreds of segments do not hit
// the limit of one command-line argument.
func (r *Runner) RenderSegments(ctx context.Context, src, dst string, segments []Segment, opts RenderOptions) error {
	if len(segments) == 0 {
		return fmt.Errorf("failed to render: %w", errNoSegments)
	}

	in, out, err := localFiles(src, dst)
	if err != nil {
		return err
	}

	if opts, _, err = r.fitLoudness(ctx, in, segments, opts); err != nil {
		return err
	}

	dir, err := os.MkdirTemp("", filterDirPattern)
	if err != nil {
		return fmt.Errorf("failed to create filter dir: %w", err)
	}

	defer func() { _ = os.RemoveAll(dir) }() // best effort: a few bytes in the temp dir

	graph := filepath.Join(dir, filterFileName)
	if err := os.WriteFile(graph, []byte(segmentsFilter(segments, opts)), filterFileMode); err != nil {
		return fmt.Errorf("failed to write filter graph: %w", err)
	}

	major, err := r.majorVersion(ctx)
	if err != nil {
		return err
	}

	inputs := cutInputArgs(in, groupSegments(segments, opts))
	if _, err := r.run(ctx, r.ffmpegPath, renderSegmentsArgs(inputs, out, graph, major), nil); err != nil {
		return fmt.Errorf("failed to render segments: %w", err)
	}

	return nil
}

// majorVersion reads the first line of "ffmpeg -version": the head of the output is kept, because a static
// build prints kilobytes of configuration after it.
func (r *Runner) majorVersion(ctx context.Context) (int, error) {
	stdout, err := r.execute(ctx, "", r.ffmpegPath, []string{"-version"}, nil, newHeadBuffer(versionHeadSize))
	if err != nil {
		return 0, fmt.Errorf("failed to read ffmpeg version: %w", err)
	}

	return parseMajorVersion(stdout), nil
}

// parseMajorVersion reads "ffmpeg version 8.1.3 …", "ffmpeg version n7.0 …" or "6.1.1-3ubuntu5".
// A git build ("N-…") counts as the newest; an unreadable version as 0 (the old syntax).
func parseMajorVersion(out string) int {
	line, _, _ := strings.Cut(out, "\n")

	_, version, ok := strings.Cut(line, versionPrefix)
	if !ok {
		return unknownMajor
	}

	if strings.HasPrefix(version, developmentPrefix) {
		return developmentMajor
	}

	version = strings.TrimPrefix(version, "n")
	end := strings.IndexFunc(version, func(r rune) bool { return !unicode.IsDigit(r) })

	if end < 0 {
		end = len(version)
	}

	major, err := strconv.Atoi(version[:end])
	if err != nil {
		return unknownMajor
	}

	return major
}

// parseRational reads ffprobe's "30000/1001" or "25"; "0/0" (unknown) and an empty value are the zero Rational.
func parseRational(s string) (Rational, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Rational{}, nil
	}

	num, den, ok := strings.Cut(s, frameRateDivider)
	if !ok {
		den = "1"
	}

	n, numErr := strconv.ParseInt(num, decimalBase, int64Bits)
	d, denErr := strconv.ParseInt(den, decimalBase, int64Bits)

	if numErr != nil || denErr != nil || n < 0 || d < 0 {
		return Rational{}, fmt.Errorf("failed to parse %q: %w", s, errBadFrameRate)
	}

	if n == 0 || d == 0 {
		return Rational{}, nil
	}

	return Rational{Num: n, Den: d}, nil
}

// renderSegmentsArgs reads the filter graph from a local file; graph is our own temporary file. inputs are the
// inputs of the cut (cutInputArgs).
func renderSegmentsArgs(inputs []string, dst, graph string, ffmpegMajor int) []string {
	args := append(commonArgs(), inputs...)

	args = append(args, filterFileArgs(graph, ffmpegMajor)...)
	args = append(args,
		mapFlag, "[v]", mapFlag, "[a]",
		videoCodecFlag, videoCodec, "-preset", videoPreset, "-crf", videoCRF, "-pix_fmt", videoPixFmt,
		audioCodecFlag, renderACodec, "-b:a", renderABitrate,
		"-movflags", "+faststart",
		formatFlag, renderFormat,
	)

	return append(args, dst)
}

// filterFileArgs read the filter graph from a file, with the option of the ffmpeg version.
func filterFileArgs(graph string, ffmpegMajor int) []string {
	if ffmpegMajor >= fileOptionMajor {
		return []string{"-/filter_complex", graph}
	}

	return []string{"-filter_complex_script", graph}
}

// segmentsFilter builds a filter graph that decodes each stream of every input once (an input per group of
// neighbouring segments, groupSegments; one input for segments near one another), splits it into one branch per
// segment, trims every branch, resets its timestamps, fades its audio edges and concatenates the pieces
// (one input, 30 fps, fade 0.01, audio starting at 0.1 s):
//
//	[0:v:0]setpts=PTS-0.104167/TB,fps=fps=30/1:start_time=0:round=up,settb=1/30,split=1[s0];
//	[0:a:0]asetpts=PTS-0.100000/TB,asplit=1[t0];
//	[s0]trim=start_pts=30:end_pts=60,setpts=PTS-STARTPTS[v0];
//	[t0]atrim=start=1.000000:end=2.000000,asetpts=PTS-STARTPTS,
//	afade=t=in:st=0:d=0.010000,afade=t=out:st=0.990000:d=0.010000[a0];
//	[v0][a0]concat=n=1:v=1:a=1[v][a]
//
// split/asplit matter: referencing [0:v:0] once per segment makes ffmpeg queue decoded frames for every
// branch, gigabytes for a 1080p clip. With a frame rate the picture is made constant (fps) and counted in
// frames (settb=1/fps). round=up puts a source frame on the first output frame at or after it, so output frame k
// shows the source frame of time k/fps, never a later one: 60 → 30 fps keeps the source frames 0, 2, 4…
// (round=down kept 1, 3, 5…, the picture 1/60 s ahead of the sound); the video is moved frameTolerance of a frame
// earlier for timestamps rounded up by the container (the sound is not). The video is trimmed by frame numbers,
// so a segment on the frame grid holds exactly
// as many frames as its audio lasts, with no rounding of timestamps in seconds.
// Only numbers come from the caller, so nothing in the graph can be injected.
func segmentsFilter(segments []Segment, opts RenderOptions) string {
	return segmentsGraph(segments, opts, "v", "a")
}

// segmentsGraph is segmentsFilter with the given labels of the concatenated video and audio. Each group of
// segments (groupSegments) is read from its own input; one group is the graph of the whole source.
func segmentsGraph(segments []Segment, opts RenderOptions, videoOut, audioOut string) string {
	var b strings.Builder

	for input, g := range groupSegments(segments, opts) {
		writeGroup(&b, input, g, segments, opts)
	}

	writeConcat(&b, len(segments), opts, videoOut, audioOut)

	return b.String()
}

// writeGroup decodes the streams of one input once, splits them into a branch per segment of the group and trims,
// resets and fades every branch. Segment times are moved by the group's base, its input's timestamps by its shift.
func writeGroup(b *strings.Builder, input int, g inputGroup, segments []Segment, opts RenderOptions) {
	n := g.last - g.first + 1
	count := strconv.Itoa(n)

	if rate := opts.FrameRate.Float(); rate > 0 {
		inverse := Rational{Num: opts.FrameRate.Den, Den: opts.FrameRate.Num}
		b.WriteString(streamLabel(input, "v") + shiftExpr("setpts", g.shift+frameTolerance/rate))
		b.WriteString(",fps=fps=" + opts.FrameRate.String() + ":start_time=0:round=up,settb=" + inverse.String())
	} else {
		b.WriteString(streamLabel(input, "v") + shiftExpr("setpts", g.shift))
	}

	b.WriteString(",split=" + count + rangeLabels("s", g.first, n) + ";")
	writeAudioHead(b, input, g, opts)

	for i := g.first; i <= g.last; i++ {
		s := Segment{Start: segments[i].Start - g.base, End: segments[i].End - g.base}
		k := strconv.Itoa(i)

		b.WriteString("[s" + k + "]" + videoTrim(s, opts.FrameRate) + ",setpts=PTS-STARTPTS[v" + k + "];")
		writeAudioSegment(b, i, s, opts)
	}
}

// writeAudioBranches is the sound of a group of the first input alone: its audio split and every branch trimmed and
// faded.
func writeAudioBranches(b *strings.Builder, g inputGroup, segments []Segment, opts RenderOptions) {
	writeAudioHead(b, 0, g, opts)

	for i := g.first; i <= g.last; i++ {
		writeAudioSegment(b, i, Segment{Start: segments[i].Start - g.base, End: segments[i].End - g.base}, opts)
	}
}

// writeAudioHead decodes the audio of an input once, moves it by the group's shift and splits it into the branches
// [t<first>]…[t<last>].
func writeAudioHead(b *strings.Builder, input int, g inputGroup, opts RenderOptions) {
	n := g.last - g.first + 1

	b.WriteString(streamLabel(input, "a") + shiftExpr("asetpts", g.shift))

	if opts.Mute {
		b.WriteString(",volume=0")
	}

	b.WriteString(",asplit=" + strconv.Itoa(n) + rangeLabels("t", g.first, n) + ";")
}

// writeAudioSegment trims the branch [t<i>] to s (times of the group's graph), resets it and fades its edges: [a<i>].
func writeAudioSegment(b *strings.Builder, i int, s Segment, opts RenderOptions) {
	k := strconv.Itoa(i)
	d := min(opts.Fade, (s.End-s.Start)/2)

	b.WriteString("[t" + k + "]atrim=start=" + formatPrecise(s.Start) + ":end=" + formatPrecise(s.End))
	b.WriteString(",asetpts=PTS-STARTPTS")

	if d > 0 {
		b.WriteString(",afade=t=in:st=0:d=" + formatPrecise(d))
		b.WriteString(",afade=t=out:st=" + formatPrecise(s.End-s.Start-d) + ":d=" + formatPrecise(d))
	}

	b.WriteString("[a" + k + "];")
}

// writeConcat joins the pieces and processes the joined sound (soundChain), so it is denoised and normalized as one
// whole, not piece by piece.
func writeConcat(b *strings.Builder, n int, opts RenderOptions, videoOut, audioOut string) {
	for i := range n {
		k := strconv.Itoa(i)
		b.WriteString("[v" + k + "][a" + k + "]")
	}

	sound := soundChain(opts)
	joined := audioOut

	if sound != "" {
		joined = soundLabel
	}

	b.WriteString("concat=n=" + strconv.Itoa(n) + ":v=1:a=1[" + videoOut + "][" + joined + "]")

	if sound != "" {
		b.WriteString(";[" + joined + "]" + sound + "[" + audioOut + "]")
	}
}

// videoTrim cuts a segment by frame numbers when the frame rate is known, by seconds otherwise.
func videoTrim(s Segment, frameRate Rational) string {
	rate := frameRate.Float()
	if rate <= 0 {
		return "trim=start=" + formatPrecise(s.Start) + ":end=" + formatPrecise(s.End)
	}

	start := int64(math.Round(s.Start * rate))
	end := int64(math.Round(s.End * rate))

	return "trim=start_pts=" + strconv.FormatInt(start, decimalBase) + ":end_pts=" + strconv.FormatInt(end, decimalBase)
}

// shiftExpr moves timestamps so the first audio sample is at 0: "setpts=PTS-0.100000/TB".
func shiftExpr(filter string, offset float64) string {
	if offset == 0 {
		return filter + "=PTS"
	}

	if offset < 0 {
		return filter + "=PTS+" + formatPrecise(-offset) + "/TB"
	}

	return filter + "=PTS-" + formatPrecise(offset) + "/TB"
}

// rangeLabels are n labels of the prefix numbered from first: "[s3][s4]".
func rangeLabels(prefix string, first, n int) string {
	var b strings.Builder
	for i := range n {
		b.WriteString("[" + prefix + strconv.Itoa(first+i) + "]")
	}

	return b.String()
}

func formatPrecise(value float64) string {
	return strconv.FormatFloat(value, 'f', filterPrecision, float64Bits)
}
