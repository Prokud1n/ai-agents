package ffmpeg

import (
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSegmentsFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []Segment
		opts     RenderOptions
		expected string
	}{
		{
			name:  "one segment, constant 30 fps, audio starting later",
			input: []Segment{{Start: 1, End: 2.5}},
			opts:  RenderOptions{Fade: 0.01, FrameRate: Rational{Num: 30, Den: 1}, Offset: 0.1},
			expected: "[0:v:0]setpts=PTS-0.104167/TB,fps=fps=30/1:start_time=0:round=up,settb=1/30,split=1[s0];" +
				"[0:a:0]asetpts=PTS-0.100000/TB,asplit=1[t0];" +
				"[s0]trim=start_pts=30:end_pts=75,setpts=PTS-STARTPTS[v0];" +
				"[t0]atrim=start=1.000000:end=2.500000,asetpts=PTS-STARTPTS," +
				"afade=t=in:st=0:d=0.010000,afade=t=out:st=1.490000:d=0.010000[a0];" +
				"[v0][a0]concat=n=1:v=1:a=1[v][a]",
		},
		{
			name:  "two segments from one decoded stream, a short fade, unknown frame rate",
			input: []Segment{{Start: 0, End: 0.01}, {Start: 3, End: 4}},
			opts:  RenderOptions{Fade: 0.01, Offset: -0.02},
			expected: "[0:v:0]setpts=PTS+0.020000/TB,split=2[s0][s1];[0:a:0]asetpts=PTS+0.020000/TB,asplit=2[t0][t1];" +
				"[s0]trim=start=0.000000:end=0.010000,setpts=PTS-STARTPTS[v0];" +
				"[t0]atrim=start=0.000000:end=0.010000,asetpts=PTS-STARTPTS," +
				"afade=t=in:st=0:d=0.005000,afade=t=out:st=0.005000:d=0.005000[a0];" +
				"[s1]trim=start=3.000000:end=4.000000,setpts=PTS-STARTPTS[v1];" +
				"[t1]atrim=start=3.000000:end=4.000000,asetpts=PTS-STARTPTS," +
				"afade=t=in:st=0:d=0.010000,afade=t=out:st=0.990000:d=0.010000[a1];" +
				"[v0][a0][v1][a1]concat=n=2:v=1:a=1[v][a]",
		},
		{
			name:  "no fade, no offset, ntsc rate kept as a rational",
			input: []Segment{{Start: 1.001, End: 2.002}},
			opts:  RenderOptions{FrameRate: Rational{Num: 30000, Den: 1001}},
			expected: "[0:v:0]setpts=PTS-0.004171/TB,fps=fps=30000/1001:start_time=0:round=up,settb=1001/30000,split=1[s0];" +
				"[0:a:0]asetpts=PTS,asplit=1[t0];" +
				"[s0]trim=start_pts=30:end_pts=60,setpts=PTS-STARTPTS[v0];" +
				"[t0]atrim=start=1.001000:end=2.002000,asetpts=PTS-STARTPTS[a0];" +
				"[v0][a0]concat=n=1:v=1:a=1[v][a]",
		},
		{
			name:  "muted: the audio track stays, silent",
			input: []Segment{{Start: 1, End: 2}},
			opts:  RenderOptions{Mute: true},
			expected: "[0:v:0]setpts=PTS,split=1[s0];[0:a:0]asetpts=PTS,volume=0,asplit=1[t0];" +
				"[s0]trim=start=1.000000:end=2.000000,setpts=PTS-STARTPTS[v0];" +
				"[t0]atrim=start=1.000000:end=2.000000,asetpts=PTS-STARTPTS[a0];" +
				"[v0][a0]concat=n=1:v=1:a=1[v][a]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, segmentsFilter(tt.input, tt.opts))
		})
	}
}

func TestRenderSegmentsArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    int
		expected []string
	}{
		{name: "ffmpeg 8 reads the option from a file", input: 8, expected: []string{"-/filter_complex", "/tmp/g.txt"}},
		{name: "ffmpeg 6 has the script option", input: 6, expected: []string{"-filter_complex_script", "/tmp/g.txt"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			args := renderSegmentsArgs(testVideo, "file:/out/cut.part", "/tmp/g.txt", tt.input)

			assert.Equal(t, concat(commonArgs(), inputArgs(testVideo)), args[:8])
			assert.Equal(t, tt.expected, args[8:10])
			assert.Equal(t, "file:/out/cut.part", args[len(args)-1])
			assert.Contains(t, strings.Join(args, " "), "-f mp4 file:/out/cut.part")
			assert.Contains(t, strings.Join(args, " "), "-c:v libx264")
		})
	}
}

func TestParseMajorVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{name: "alpine", input: "ffmpeg version 8.1.3 Copyright (c) 2000-2026 the FFmpeg developers\nbuilt with gcc", expected: 8},
		{name: "ubuntu", input: "ffmpeg version 6.1.1-3ubuntu5 Copyright (c) 2000-2023", expected: 6},
		{name: "release tag", input: "ffmpeg version n7.0 Copyright", expected: 7},
		{name: "git build", input: "ffmpeg version N-112345-gabcdef Copyright", expected: developmentMajor},
		{name: "unreadable", input: "something else", expected: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, parseMajorVersion(tt.input))
		})
	}
}

func TestParseProbe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		expected  MediaInfo
		expectErr bool
	}{
		{
			name: "phone video",
			input: `{"streams":[{"codec_type":"video","r_frame_rate":"30/1","avg_frame_rate":"30000/1001","start_time":"0.000000"},
				{"codec_type":"audio","r_frame_rate":"0/0","avg_frame_rate":"0/0","start_time":"0.078000"},
				{"codec_type":"data","start_time":"N/A"}],"format":{"duration":"61.500000","start_time":"0.020000"}}`,
			expected: MediaInfo{
				Duration: 61.5, HasVideo: true, HasAudio: true, AudioStart: 0.078, FormatStart: 0.02,
				AvgFrameRate: Rational{Num: 30000, Den: 1001}, RealFrameRate: Rational{Num: 30, Den: 1},
			},
		},
		{
			name:  "no audio",
			input: `{"streams":[{"codec_type":"video","r_frame_rate":"25/1","avg_frame_rate":"25/1"}],"format":{"duration":"2"}}`,
			expected: MediaInfo{
				Duration: 2, HasVideo: true, AvgFrameRate: Rational{Num: 25, Den: 1}, RealFrameRate: Rational{Num: 25, Den: 1},
			},
		},
		{
			name: "iPhone HDR (HLG)",
			input: `{"streams":[{"codec_type":"video","r_frame_rate":"30/1","avg_frame_rate":"30/1",` +
				`"color_transfer":"arib-std-b67","color_primaries":"bt2020"}],"format":{"duration":"3"}}`,
			expected: MediaInfo{
				Duration: 3, HasVideo: true, AvgFrameRate: Rational{Num: 30, Den: 1}, RealFrameRate: Rational{Num: 30, Den: 1},
				ColorTransfer: "arib-std-b67", ColorPrimaries: "bt2020",
			},
		},
		{name: "no duration", input: `{"streams":[],"format":{}}`, expectErr: true},
		{name: "not json", input: "oops", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseProbe(tt.input)
			if tt.expectErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestMediaInfoHDR(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "HLG", input: "arib-std-b67", expected: true},
		{name: "PQ", input: "smpte2084", expected: true},
		{name: "SDR", input: "bt709"},
		{name: "untagged", input: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			info := MediaInfo{ColorTransfer: tt.input}
			assert.Equal(t, tt.expected, info.HDR())
		})
	}
}

func TestParseRational(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		expected  Rational
		expectErr bool
	}{
		{name: "ntsc", input: "30000/1001\n", expected: Rational{Num: 30000, Den: 1001}},
		{name: "integer rational", input: "30/1", expected: Rational{Num: 30, Den: 1}},
		{name: "plain number", input: "25", expected: Rational{Num: 25, Den: 1}},
		{name: "unknown", input: "0/0", expected: Rational{}},
		{name: "no video stream", input: "", expected: Rational{}},
		{name: "garbage", input: "abc/1", expectErr: true},
		{name: "fraction", input: "29.97", expectErr: true},
		{name: "negative", input: "-30/1", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseRational(tt.input)
			if tt.expectErr {
				require.ErrorIs(t, err, errBadFrameRate)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestRationalFloat(t *testing.T) {
	t.Parallel()

	assert.InDelta(t, 29.97003, Rational{Num: 30000, Den: 1001}.Float(), 1e-5)
	assert.InDelta(t, 0.0, Rational{}.Float(), 0)
	assert.Equal(t, "30000/1001", Rational{Num: 30000, Den: 1001}.String())
}

func TestHeadBuffer(t *testing.T) {
	t.Parallel()

	b := newHeadBuffer(8)
	for _, part := range []string{"ffmpeg ", "version 8.1", " and kilobytes of configuration"} {
		n, err := b.Write([]byte(part))
		require.NoError(t, err)
		assert.Len(t, part, n)
	}

	assert.Equal(t, "ffmpeg v", b.String())
}

// probeStreams returns "codec,width,height" lines of the streams of a file.
func probeStreams(t *testing.T, r *Runner, path string) string {
	t.Helper()

	in, err := localFile(path)
	require.NoError(t, err)

	out, err := r.run(t.Context(), r.ffprobePath, probeEntriesArgs(in, "stream=codec_name,width,height", "csv=p=0"), nil)
	require.NoError(t, err)

	return strings.TrimSpace(out)
}

// A vertical clip with a tone in [1, 2) and [4, 5) s: the render keeps two fragments around the tones.
func TestRunnerRenderSegments(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	ctx := t.Context()
	r := newTestRunner(time.Minute)
	dir := t.TempDir()

	src := filepath.Join(dir, "vertical.mp4")
	_, err := r.run(ctx, r.ffmpegPath, concat(
		commonArgs(),
		lavfiInput("aevalsrc=if(between(t\\,1\\,2)+between(t\\,4\\,5)\\,0.5*sin(2*PI*440*t)\\,0):s=48000:d=6"),
		lavfiInput("testsrc=duration=6:size=180x320:rate=30"),
		[]string{"-codec:v", videoCodec, "-codec:a", renderACodec, shortestFlag, src},
	), nil)
	require.NoError(t, err)

	dst := filepath.Join(dir, "cut.part") // no extension: the format is set explicitly
	info, err := r.Probe(ctx, src)
	require.NoError(t, err)
	assert.Equal(t, Rational{Num: 30, Den: 1}, info.AvgFrameRate)
	assert.True(t, info.HasVideo)
	assert.True(t, info.HasAudio)

	segments := []Segment{{Start: 0.9, End: 2.1}, {Start: 3.9, End: 5.1}}
	opts := RenderOptions{Fade: 0.01, FrameRate: info.AvgFrameRate, Offset: info.AudioStart - info.FormatStart}
	require.NoError(t, r.RenderSegments(ctx, src, dst, segments, opts))

	duration, err := r.Duration(ctx, dst)
	require.NoError(t, err)
	assert.InDelta(t, 2.4, duration, 0.05)

	silences, err := r.DetectSilences(ctx, dst, SilenceOptions{NoiseDB: -30, MinDuration: 400 * time.Millisecond})
	require.NoError(t, err)
	assert.Empty(t, silences)

	assert.Equal(t, "h264,180,320\naac", probeStreams(t, r, dst))

	t.Logf("ffmpeg: rendered %d segments, %.3f s, streams %s", len(segments), duration,
		strconv.Quote(probeStreams(t, r, dst)))
}

// TestRunnerRenderHalvesFrameRate: a 60 fps source whose frame n has the brightness 3n, cut at 30 fps into two
// fragments: frame k of the reel (frame j of a fragment starting at frame s) is the source frame 2(s+j) exactly, the
// frame of the sound's time, not the one after it. Matroska keeps the timestamps in milliseconds (16/60 s is stored as
// 0.267 s, a little late), as a container can, and PCM audio starts with the picture.
func TestRunnerRenderHalvesFrameRate(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	const (
		brightnessStep = 3
		sampleSize     = 16 // a 4×4 gray crop of the middle of the frame
	)

	ctx := t.Context()
	r := newTestRunner(time.Minute)
	dir := t.TempDir()
	src := filepath.Join(dir, "numbered.mkv")

	_, err := r.run(ctx, r.ffmpegPath, concat(
		commonArgs(),
		lavfiInput("sine=frequency=440:sample_rate=48000:duration=1.2"),
		lavfiInput("color=c=black:s=64x64:r=60:d=1.2,format=gray,geq=lum='N*3'"),
		[]string{"-c:v", "ffv1", audioCodecFlag, "pcm_s16le", shortestFlag, src},
	), nil)
	require.NoError(t, err)

	info, err := r.Probe(ctx, src)
	require.NoError(t, err)
	require.Equal(t, Rational{Num: 60, Den: 1}, info.RealFrameRate)

	dst := filepath.Join(dir, "reel.mp4")
	// 30 fps frames 6–17 and 24–33.
	segments := []Segment{{Start: 0.2, End: 0.6}, {Start: 0.8, End: 34.0 / 30}}
	opts := RenderOptions{FrameRate: Rational{Num: 30, Den: 1}, Offset: info.AudioStart - info.FormatStart}
	require.NoError(t, r.RenderSegments(ctx, src, dst, segments, opts))

	in, err := localFile(dst)
	require.NoError(t, err)

	frames, err := r.run(ctx, r.ffmpegPath, concat(commonArgs(), inputArgs(in),
		[]string{"-vf", "crop=4:4,format=gray", formatFlag, "rawvideo", "-"}), nil)
	require.NoError(t, err)

	expected := make([]int, 0, len(frames)/sampleSize)
	for _, rng := range [][2]int{{6, 18}, {24, 34}} {
		for k := rng[0]; k < rng[1]; k++ {
			expected = append(expected, 2*k)
		}
	}

	got := make([]int, 0, len(frames)/sampleSize)
	for i := 0; i+sampleSize <= len(frames); i += sampleSize {
		got = append(got, int(math.Round(float64(frames[i])/brightnessStep)))
	}

	assert.Equal(t, expected, got, "reel frame k is the source frame 2k of its time")
	t.Logf("60 → 30 fps: source frames of the reel %v", got)
}

func TestRunnerRenderSegmentsErrors(t *testing.T) {
	t.Parallel()

	r := newTestRunner(time.Minute)

	err := r.RenderSegments(t.Context(), "in.mp4", "out.mp4", nil, RenderOptions{})

	require.ErrorIs(t, err, errNoSegments)
}
