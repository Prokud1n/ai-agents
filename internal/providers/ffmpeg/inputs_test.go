package ffmpeg

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pcmCodec keeps the sound of a generated source exact.
const pcmCodec = "pcm_s16le"

func TestGroupSegments(t *testing.T) {
	t.Parallel()

	rate30 := RenderOptions{FrameRate: Rational{Num: 30, Den: 1}, Offset: 0.1}

	tests := []struct {
		name     string
		input    []Segment
		opts     RenderOptions
		expected []inputGroup
	}{
		{
			name:     "one segment near the start: the whole source, read to the end",
			input:    []Segment{{Start: 1, End: 2}},
			opts:     rate30,
			expected: []inputGroup{{first: 0, last: 0, shift: 0.1}},
		},
		{
			name:  "one segment far into the source: seeked half a second (whole frames) before it",
			input: []Segment{{Start: 60.02, End: 62}},
			opts:  rate30,
			// The first frame at or before 60.02 is 1800 (60 s), 15 frames earlier is 59.5 s.
			expected: []inputGroup{{first: 0, last: 0, base: 59.5, seek: 59.6, length: 3}},
		},
		{
			name:  "close segments share an input, far ones get their own",
			input: []Segment{{Start: 0.5, End: 1}, {Start: 2, End: 3}, {Start: 10, End: 11}, {Start: 12, End: 13}},
			opts:  RenderOptions{FrameRate: Rational{Num: 25, Den: 1}},
			expected: []inputGroup{
				{first: 0, last: 1, length: 3.5},
				{first: 2, last: 3, base: 9.48, seek: 9.48, length: 4.02},
			},
		},
		{
			name:  "no frame rate: half a second before the segment",
			input: []Segment{{Start: 0, End: 1}, {Start: 8, End: 9}},
			opts:  RenderOptions{Offset: -0.02},
			expected: []inputGroup{
				{first: 0, last: 0, shift: -0.02, length: 1.48},
				{first: 1, last: 1, base: 7.5, seek: 7.48, length: 2},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := groupSegments(tt.input, tt.opts)
			require.Len(t, got, len(tt.expected))

			for i, g := range got {
				want := tt.expected[i]
				assert.Equal(t, [2]int{want.first, want.last}, [2]int{g.first, g.last}, "group %d", i)
				assert.InDelta(t, want.base, g.base, 1e-9, "base of group %d", i)
				assert.InDelta(t, want.seek, g.seek, 1e-9, "seek of group %d", i)
				assert.InDelta(t, want.length, g.length, 1e-9, "length of group %d", i)
				assert.InDelta(t, want.shift, g.shift, 1e-9, "shift of group %d", i)
			}
		})
	}
}

// TestGroupSegmentsLimit: many far segments are merged where the gaps are the shortest, down to maxCutInputs inputs.
func TestGroupSegmentsLimit(t *testing.T) {
	t.Parallel()

	// Segments 10 s apart (gaps of 9 s), but 5 and 9 start 2 s earlier: the gaps before them are the shortest (7 s).
	segments := make([]Segment, 0, 12)
	for i := range 12 {
		start := 10 * float64(i)
		if i == 5 || i == 9 {
			start -= 2
		}

		segments = append(segments, Segment{Start: start, End: start + 1})
	}

	got := groupSegments(segments, RenderOptions{})
	require.Len(t, got, maxCutInputs)

	covered := 0
	for i, g := range got {
		if i > 0 {
			assert.Equal(t, got[i-1].last+1, g.first, "the groups follow one another")
		}

		covered += g.last - g.first + 1
	}

	assert.Equal(t, len(segments), covered)
	assert.Equal(t, [2]int{4, 5}, [2]int{got[2].first, got[2].last}, "the shortest gap is decoded through")
}

func TestCutInputArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []inputGroup
		expected []string
	}{
		{
			name:     "the whole source: as before",
			input:    []inputGroup{{}},
			expected: inputArgs(testVideo),
		},
		{
			name:  "groups: each input seeked and bounded, its decoder with two threads",
			input: []inputGroup{{length: 3.5}, {seek: 9.48, length: 4.02}},
			expected: concat(
				[]string{threadsFlag, groupThreads, durationFlag, "3.500000"}, inputArgs(testVideo),
				[]string{threadsFlag, groupThreads, seekFlag, "9.480000", durationFlag, "4.020000"}, inputArgs(testVideo),
			),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, cutInputArgs(testVideo, tt.input))
		})
	}
}

// TestSegmentsFilterGroups: every group reads its own input, with its segments moved by its base.
func TestSegmentsFilterGroups(t *testing.T) {
	t.Parallel()

	got := segmentsFilter([]Segment{{Start: 1, End: 2}, {Start: 30, End: 31}},
		RenderOptions{Fade: 0.01, FrameRate: Rational{Num: 30, Den: 1}, Offset: 0.1})

	assert.Equal(t, "[0:v:0]setpts=PTS-0.104167/TB,fps=fps=30/1:start_time=0:round=up,settb=1/30,split=1[s0];"+
		"[0:a:0]asetpts=PTS-0.100000/TB,asplit=1[t0];"+
		"[s0]trim=start_pts=30:end_pts=60,setpts=PTS-STARTPTS[v0];"+
		"[t0]atrim=start=1.000000:end=2.000000,asetpts=PTS-STARTPTS,"+
		"afade=t=in:st=0:d=0.010000,afade=t=out:st=0.990000:d=0.010000[a0];"+
		"[1:v:0]setpts=PTS-0.004167/TB,fps=fps=30/1:start_time=0:round=up,settb=1/30,split=1[s1];"+
		"[1:a:0]asetpts=PTS,asplit=1[t1];"+
		"[s1]trim=start_pts=15:end_pts=45,setpts=PTS-STARTPTS[v1];"+
		"[t1]atrim=start=0.500000:end=1.500000,asetpts=PTS-STARTPTS,"+
		"afade=t=in:st=0:d=0.010000,afade=t=out:st=0.990000:d=0.010000[a1];"+
		"[v0][a0][v1][a1]concat=n=2:v=1:a=1[v][a]", got)
}

// TestRunnerRenderSeeksGroups: a 60 fps source of 20 s (H.264 with a keyframe every half second and B-frames, in
// Matroska with millisecond timestamps) whose frame n shows its number, cut at 30 fps into segments far apart, so each
// group is seeked: frame k of every segment is the source frame of its time exactly, as with the whole source decoded.
func TestRunnerRenderSeeksGroups(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	const (
		lowStep    = 4 // brightness of N mod 64 in the left square
		highStep   = 8 // of N / 64 in the right one
		modulus    = 64
		sampleSize = 16 // a 4×4 gray crop
		rate       = 30
	)

	ctx := t.Context()
	r := newTestRunner(2 * time.Minute)
	dir := t.TempDir()
	src := filepath.Join(dir, "numbered.mkv")

	_, err := r.run(ctx, r.ffmpegPath, concat(
		commonArgs(),
		lavfiInput("sine=frequency=440:sample_rate=48000:duration=20"),
		lavfiInput("color=c=black:s=64x32:r=60:d=20,format=gray,"+
			"geq=lum='if(lt(X\\,32)\\,mod(N\\,64)*4\\,floor(N/64)*8)'"),
		[]string{videoCodecFlag, videoCodec, "-qp", "0", "-g", "30", "-pix_fmt", "yuv420p", audioCodecFlag, pcmCodec,
			shortestFlag, src},
	), nil)
	require.NoError(t, err)

	info, err := r.Probe(ctx, src)
	require.NoError(t, err)

	segments := []Segment{{Start: 0.2, End: 0.6}, {Start: 6.1, End: 6.5}, {Start: 12, End: 12.4}, {Start: 19.0, End: 19.5}}
	opts := RenderOptions{FrameRate: Rational{Num: rate, Den: 1}, Offset: info.AudioStart - info.FormatStart}
	require.Len(t, groupSegments(segments, opts), len(segments), "every segment is its own seeked input")

	dst := filepath.Join(dir, "reel.mp4")
	require.NoError(t, r.RenderSegments(ctx, src, dst, segments, opts))

	in, err := localFile(dst)
	require.NoError(t, err)

	frames, err := r.runLimited(ctx, r.ffmpegPath, concat(commonArgs(), inputArgs(in),
		[]string{"-vf", "format=gray,split=2[l][r];[l]crop=4:4:12:12[a];[r]crop=4:4:44:12[b];[a][b]vstack",
			formatFlag, rawVideoFormat, "-"}), 1<<20)
	require.NoError(t, err)

	var expected, got []int

	for _, s := range segments {
		for k := int(math.Round(s.Start * rate)); k < int(math.Round(s.End*rate)); k++ {
			expected = append(expected, 2*k)
		}
	}

	for i := 0; i+2*sampleSize <= len(frames); i += 2 * sampleSize {
		low := int(math.Round(float64(frames[i]) / lowStep))
		high := int(math.Round(float64(frames[i+sampleSize]) / highStep))
		got = append(got, high*modulus+low)
	}

	assert.Equal(t, expected, got, "reel frame k is the source frame 2k of its time")

	duration, err := r.Duration(ctx, dst)
	require.NoError(t, err)
	assert.InDelta(t, 1.7, duration, 0.05, "the sound is as long as the picture")
	t.Logf("seeked groups: source frames of the reel %v", got)
}

// TestRunnerJointsWithoutClicks: a continuous tone cut in the middle of its waves at every joint (the worst case): with
// the fades at the joints the sound changes there no faster than within the tone, so there is no click.
func TestRunnerJointsWithoutClicks(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	const (
		sampleRate = 48000
		near       = 0.003 // s around a joint
		farFrom    = 0.03  // s: the tone itself, away from the joint
		farTo      = 0.06
		margin     = 1.2 // AAC rings a little
	)

	ctx := t.Context()
	r := newTestRunner(2 * time.Minute)
	dir := t.TempDir()
	src := filepath.Join(dir, "tone.mp4")

	_, err := r.run(ctx, r.ffmpegPath, concat(commonArgs(),
		lavfiInput("sine=frequency=440:sample_rate=48000:duration=10"),
		lavfiInput("testsrc=duration=10:size=180x320:rate=30"),
		[]string{videoCodecFlag, videoCodec, audioCodecFlag, renderACodec, shortestFlag, src}), nil)
	require.NoError(t, err)

	// Joints at 1.0 and 2.0 s of the reel, between pieces that end and start at different phases of the tone.
	segments := []Segment{{Start: 0.5, End: 1.5}, {Start: 2.0 + 1.0/3, End: 3.0 + 1.0/3}, {Start: 7.1, End: 8.1}}
	dst := filepath.Join(dir, "reel.mp4")
	require.NoError(t, r.RenderSegments(ctx, src, dst, segments, RenderOptions{
		Fade: 0.01, FrameRate: Rational{Num: 30, Den: 1},
	}))

	in, err := localFile(dst)
	require.NoError(t, err)

	pcm, err := r.runLimited(ctx, r.ffmpegPath, concat(commonArgs(), inputArgs(in),
		[]string{noVideoFlag, channelsFlag, "1", sampleRateFlag, "48000", formatFlag, "s16le", "-"}), 1<<20)
	require.NoError(t, err)

	steps := make([]float64, 0, len(pcm)/2)
	previous := 0.0

	for i := 0; i+1 < len(pcm); i += 2 {
		u := int(pcm[i]) | int(pcm[i+1])<<8
		if u >= 1<<15 {
			u -= 1 << 16
		}

		v := float64(u)
		steps = append(steps, math.Abs(v-previous))
		previous = v
	}

	largest := func(from, to float64) float64 {
		peak := 0.0
		for i := max(int(from*sampleRate), 1); i < min(int(to*sampleRate), len(steps)); i++ {
			peak = max(peak, steps[i])
		}

		return peak
	}

	for _, joint := range []float64{1, 2} {
		atJoint := largest(joint-near, joint+near)
		tone := max(largest(joint-farTo, joint-farFrom), largest(joint+farFrom, joint+farTo))

		require.Positive(t, tone)
		assert.LessOrEqual(t, atJoint, tone*margin, "the joint at %.0f s clicks", joint)
		t.Logf("joint at %.0f s: the largest step %.0f, in the tone around it %.0f", joint, atJoint, tone)
	}
}
