package ffmpeg

import (
	"math"
	"slices"
	"strconv"
)

// A cut decodes only what it keeps (T3.19b): neighbouring segments are grouped, and every group is a separate input of
// the source seeked to just before it (-ss before -i: the demuxer jumps to the keyframe before it, so the decoder
// skips the rest of the file) and read only to just after it (-t). An edit of a one-minute reel from a 4-minute 4K
// source decodes about a minute of it instead of four. Inside a group the graph is the one of a single input: the
// video is made constant on the frame grid and trimmed by frame numbers, the audio by samples, so the cut is as exact.
const (
	// groupGap: segments closer than this are decoded together. A seek decodes from the keyframe before it (a phone
	// puts one about every 1–2 s) and opens a decoder, so a shorter gap is cheaper to decode through.
	groupGap = 3.0
	// Every input keeps its decoder and its frames open until the end of the render: the review measured 1.4 GB of
	// RSS for 8 inputs of 4K 8-bit H.264 and 2.2 GB for 8 inputs of 4K 10-bit HEVC. So the number of inputs comes from
	// a memory budget: decodeMemoryBudget / (width × height × bytes per sample × 1.5 (4:2:0) × decodeFrames),
	// rounded, within minCutInputs..maxCutInputs — 7 inputs for 4K 8-bit, 4 for 4K 10-bit, 8 for 1080p. More groups are
	// merged where the gaps are the shortest. Without the frame size (not probed) the limit is maxCutInputs.
	maxCutInputs       = 8
	minCutInputs       = 2
	decodeMemoryBudget = 1331 << 20 // about 1.3 GiB of decoders, under the 3 GB of the media worker's container
	// decodeFrames: frames a decoder with two threads holds (its references, the frame threads, the queue to the graph).
	decodeFrames = 16
	chromaFactor = 1.5 // 4:2:0: the two chroma planes add half a luma plane
	// groupThreads of the decoder of each input when there are several: one of them decodes at a time, and two
	// threads keep its frames few (a decoder with as many threads as CPUs holds a frame per thread).
	groupThreads = "2"
	threadsFlag  = "-threads"
	// seekPreroll: a group's input starts this much before its first segment, so the frame shown at the first output
	// frame is decoded and the fps filter sees the frames before it, as in a whole decoded source.
	seekPreroll = 0.5
	// seekTail: the input is read this much past the end of the group's last segment.
	seekTail = 0.5
	// minSeek: a group starting within the first seconds of the source is decoded from the start, not seeked.
	minSeek = 2.0
	// gridEpsilon keeps a time on the frame grid (k/rate computed in floating point) from being floored a frame down.
	gridEpsilon = 1e-6
	// groupArgs: the most arguments a group adds before its input (-threads, -ss and -t with their values).
	groupArgs = 6
)

// inputGroup is a run of neighbouring segments decoded from one input.
type inputGroup struct {
	first, last int // indices of its segments
	// base is the time of the audio timeline that is 0 in the group's graph, on the frame grid: the segments of the
	// group are cut at their times minus base.
	base float64
	// seek is the -ss of the input (seconds of the file from its start, as ffmpeg counts it); 0: no seek.
	seek float64
	// length is the -t of the input; 0: to the end of the file.
	length float64
	// shift replaces RenderOptions.Offset in the group's graph: it moves the input's timestamps so that base is at 0.
	shift float64
}

// groupSegments plans the inputs of a cut. One group without a seek is the whole source decoded once, as before.
func groupSegments(segments []Segment, opts RenderOptions) []inputGroup {
	if len(segments) == 0 {
		return nil
	}

	groups := []inputGroup{{first: 0, last: 0}}

	for i := 1; i < len(segments); i++ {
		if segments[i].Start-segments[i-1].End < groupGap {
			groups[len(groups)-1].last = i

			continue
		}

		groups = append(groups, inputGroup{first: i, last: i})
	}

	groups = mergeGroups(groups, segments, inputLimit(opts.Source))

	out := make([]inputGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, placeGroup(g, segments, opts, len(groups) > 1))
	}

	return out
}

// inputLimit is the number of inputs whose decoders fit decodeMemoryBudget (see maxCutInputs).
func inputLimit(frame SourceFrame) int {
	bytes := float64(frame.Width) * float64(frame.Height) * float64(frame.BytesPerSample)
	if bytes <= 0 {
		return maxCutInputs
	}

	limit := math.Round(decodeMemoryBudget / (bytes * chromaFactor * decodeFrames))

	return int(min(max(limit, minCutInputs), maxCutInputs))
}

// mergeGroups joins the groups separated by the shortest gaps until at most limit are left.
func mergeGroups(groups []inputGroup, segments []Segment, limit int) []inputGroup {
	out := append([]inputGroup(nil), groups...)

	for len(out) > limit {
		shortest := 1

		for i := 2; i < len(out); i++ {
			if groupGapBefore(out[i], segments) < groupGapBefore(out[shortest], segments) {
				shortest = i
			}
		}

		merged := inputGroup{first: out[shortest-1].first, last: out[shortest].last}
		out = slices.Concat(out[:shortest-1], []inputGroup{merged}, out[shortest+1:])
	}

	return out
}

// groupGapBefore is the gap between a group and the segment before it.
func groupGapBefore(g inputGroup, segments []Segment) float64 {
	return segments[g.first].Start - segments[g.first-1].End
}

// placeGroup sets where the input of a group starts and ends. A seek starts seekPreroll (in whole frames) before the
// first segment; a group whose seek would land within the first minSeek seconds of the source is decoded from the start
// (any group, a single one too: a single group further in is seeked). bounded: the input is read only to just after
// the group (several inputs: each stops where its group ends; a seeked single group too).
func placeGroup(g inputGroup, segments []Segment, opts RenderOptions, bounded bool) inputGroup {
	out := g
	start, end := segments[g.first].Start, segments[g.last].End

	if rate := opts.FrameRate.Float(); rate > 0 {
		out.base = (math.Floor(start*rate+gridEpsilon) - math.Ceil(seekPreroll*rate)) / rate
	} else {
		out.base = start - seekPreroll
	}

	// Seeked, the input's timestamps start at base (seek = base + Offset): no shift is left.
	out.seek = out.base + opts.Offset
	if out.seek < minSeek {
		out.base, out.seek, out.shift = 0, 0, opts.Offset
	}

	if bounded || out.seek > 0 {
		out.length = end + opts.Offset - out.seek + seekTail
	}

	return out
}

// cutInputArgs are the inputs of a cut, one per group, in the order of the groups.
func cutInputArgs(src string, groups []inputGroup) []string {
	args := make([]string, 0, len(groups)*(len(inputArgs(src))+groupArgs))

	for _, g := range groups {
		if len(groups) > 1 {
			args = append(args, threadsFlag, groupThreads)
		}

		if g.seek > 0 {
			args = append(args, seekFlag, formatPrecise(g.seek))
		}

		if g.length > 0 {
			args = append(args, durationFlag, formatPrecise(g.length))
		}

		args = append(args, inputArgs(src)...)
	}

	return args
}

// streamLabel is "[2:v:0]": the first stream of a kind of the input of a group.
func streamLabel(input int, kind string) string {
	return "[" + strconv.Itoa(input) + ":" + kind + ":0]"
}
