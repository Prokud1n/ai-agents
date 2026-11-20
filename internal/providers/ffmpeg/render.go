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

	if _, err := r.run(ctx, r.ffmpegPath, renderSegmentsArgs(in, out, graph, major), nil); err != nil {
		return fmt.Errorf("failed to render segments: %w", err)
	}

	return nil
}

// majorVersion reads the first line of "ffmpeg -version": the head of the output is kept, because a static
// build prints kilobytes of configuration after it.
func (r *Runner) majorVersion(ctx context.Context) (int, error) {
	stdout, err := r.execute(ctx, r.ffmpegPath, []string{"-version"}, nil, newHeadBuffer(versionHeadSize))
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

// renderSegmentsArgs reads the filter graph from a local file; graph is our own temporary file.
func renderSegmentsArgs(src, dst, graph string, ffmpegMajor int) []string {
	args := append(commonArgs(), inputArgs(src)...)

	if ffmpegMajor >= fileOptionMajor {
		args = append(args, "-/filter_complex", graph)
	} else {
		args = append(args, "-filter_complex_script", graph)
	}

	args = append(args,
		"-map", "[v]", "-map", "[a]",
		"-c:v", videoCodec, "-preset", videoPreset, "-crf", videoCRF, "-pix_fmt", videoPixFmt,
		audioCodecFlag, renderACodec, "-b:a", renderABitrate,
		"-movflags", "+faststart",
		"-f", renderFormat,
	)

	return append(args, dst)
}

// segmentsFilter builds a filter graph that decodes each stream once, splits it into one branch per
// segment, trims every branch, resets its timestamps, fades its audio edges and concatenates the pieces
// (30 fps, fade 0.01, audio starting at 0.1 s):
//
//	[0:v:0]setpts=PTS-0.100000/TB,fps=fps=30/1:start_time=0:round=down,settb=1/30,split=1[s0];
//	[0:a:0]asetpts=PTS-0.100000/TB,asplit=1[t0];
//	[s0]trim=start_pts=30:end_pts=60,setpts=PTS-STARTPTS[v0];
//	[t0]atrim=start=1.000000:end=2.000000,asetpts=PTS-STARTPTS,
//	afade=t=in:st=0:d=0.010000,afade=t=out:st=0.990000:d=0.010000[a0];
//	[v0][a0]concat=n=1:v=1:a=1[v][a]
//
// split/asplit matter: referencing [0:v:0] once per segment makes ffmpeg queue decoded frames for every
// branch, gigabytes for a 1080p clip. With a frame rate the picture is made constant (fps; round=down shows
// each source frame from the first output frame at or after it, never earlier) and counted in
// frames (settb=1/fps): the video is trimmed by frame numbers, so a segment on the frame grid holds exactly
// as many frames as its audio lasts, with no rounding of timestamps in seconds.
// Only numbers come from the caller, so nothing in the graph can be injected.
func segmentsFilter(segments []Segment, opts RenderOptions) string {
	var b strings.Builder

	n := strconv.Itoa(len(segments))

	b.WriteString("[0:v:0]" + shiftExpr("setpts", opts.Offset))

	if opts.FrameRate.Float() > 0 {
		inverse := Rational{Num: opts.FrameRate.Den, Den: opts.FrameRate.Num}
		b.WriteString(",fps=fps=" + opts.FrameRate.String() + ":start_time=0:round=down,settb=" + inverse.String())
	}

	b.WriteString(",split=" + n + labels("s", len(segments)) + ";")
	b.WriteString("[0:a:0]" + shiftExpr("asetpts", opts.Offset) + ",asplit=" + n + labels("t", len(segments)) + ";")

	for i, s := range segments {
		k := strconv.Itoa(i)
		d := min(opts.Fade, (s.End-s.Start)/2)

		b.WriteString("[s" + k + "]" + videoTrim(s, opts.FrameRate) + ",setpts=PTS-STARTPTS[v" + k + "];")
		b.WriteString("[t" + k + "]atrim=start=" + formatPrecise(s.Start) + ":end=" + formatPrecise(s.End))
		b.WriteString(",asetpts=PTS-STARTPTS")

		if d > 0 {
			b.WriteString(",afade=t=in:st=0:d=" + formatPrecise(d))
			b.WriteString(",afade=t=out:st=" + formatPrecise(s.End-s.Start-d) + ":d=" + formatPrecise(d))
		}

		b.WriteString("[a" + k + "];")
	}

	for i := range segments {
		k := strconv.Itoa(i)
		b.WriteString("[v" + k + "][a" + k + "]")
	}

	b.WriteString("concat=n=" + n + ":v=1:a=1[v][a]")

	return b.String()
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

func labels(prefix string, n int) string {
	var b strings.Builder
	for i := range n {
		b.WriteString("[" + prefix + strconv.Itoa(i) + "]")
	}

	return b.String()
}

func formatPrecise(value float64) string {
	return strconv.FormatFloat(value, 'f', filterPrecision, float64Bits)
}
