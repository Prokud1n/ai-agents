package ffmpeg

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The flags of the frames read back by a check.
const (
	videoFilterFlag = "-vf"
	rawVideoFormat  = "rawvideo"
)

var testZoom = &Zoom{Factor: 1.09, MinSegment: 1.2}

func TestZoomedSegments(t *testing.T) {
	t.Parallel()

	rate := Rational{Num: 30, Den: 1}

	tests := []struct {
		name     string
		input    Cut
		expected []bool
		zooms    int
	}{
		{
			name:     "no zoom",
			input:    Cut{Segments: []Segment{{Start: 0, End: 2}, {Start: 3, End: 5}}},
			expected: []bool{false, false},
		},
		{
			name: "the framing alternates at every joint",
			input: Cut{
				Segments: []Segment{{Start: 0, End: 2}, {Start: 3, End: 5}, {Start: 6, End: 8}, {Start: 9, End: 11}},
				Options:  RenderOptions{FrameRate: rate}, Zoom: testZoom,
			},
			expected: []bool{false, true, false, true},
			zooms:    2,
		},
		{
			// 0.8 s and 1 s are short: they keep the framing before them, the next long piece changes it.
			name: "short pieces do not flicker",
			input: Cut{
				Segments: []Segment{{Start: 0, End: 2}, {Start: 3, End: 3.8}, {Start: 4, End: 6}, {Start: 7, End: 8}, {Start: 9, End: 11}},
				Options:  RenderOptions{FrameRate: rate}, Zoom: testZoom,
			},
			expected: []bool{false, false, true, true, false},
			zooms:    1,
		},
		{
			// 1.19 s is 36 frames at 30 fps: 1.2 s, long enough; the length counts in whole frames.
			name: "the length of a piece in frames of the reel",
			input: Cut{
				Segments: []Segment{{Start: 0, End: 2}, {Start: 3, End: 4.19}},
				Options:  RenderOptions{FrameRate: rate}, Zoom: testZoom,
			},
			expected: []bool{false, true},
			zooms:    1,
		},
		{
			name:     "one piece stays as it is",
			input:    Cut{Segments: []Segment{{Start: 0, End: 20}}, Zoom: testZoom},
			expected: []bool{false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, tt.input.ZoomedSegments())
			assert.Equal(t, tt.zooms, tt.input.Zooms())
		})
	}
}

func TestZoomWindow(t *testing.T) {
	t.Parallel()

	w, h, x, y := zoomWindow(1080, 1920, 1.09)

	assert.Equal(t, []int{990, 1760, 44, 64}, []int{w, h, x, y},
		"9:16 like the frame, even, the middle across and 40 % of the height fixed")
}

// TestBurnGraphZoom: the zoom works on the 1080×1920 frame after the fit and the tone mapping, under the shade and the
// subtitles; spans of neighbouring zoomed pieces are one, their edges half a frame before the frame.
func TestBurnGraphZoom(t *testing.T) {
	t.Parallel()

	segments := []Segment{{Start: 0, End: 2}, {Start: 3, End: 5}, {Start: 6, End: 6.5}, {Start: 7, End: 9}}
	zoom := ",format=yuv420p,split=2[zn][zs];[zs]crop=990:1760:44:64,scale=1080:1920[zz];" +
		"[zn][zz]overlay=x=0:y=0:enable='gte(t,1.983333)*lt(t,4.483333)'"
	base := BurnSubtitlesData{
		SubtitlesName: testSubtitlesName, FontsName: testFontsName, Width: 1080, Height: 1920, Dir: testJobDir,
	}

	tests := []struct {
		name        string
		fit         FitMode
		toneMap     bool
		shade       *Shade
		zoom        *Zoom
		expected    string
		expectedErr error
	}{
		{
			name: "crop: zoomed after the fit, then the subtitles",
			fit:  CropFitMode, zoom: testZoom,
			expected: "[cut]scale=1080:1920:force_original_aspect_ratio=increase:force_divisible_by=2:out_color_matrix=bt709:out_range=tv," +
				"crop=1080:1920" + zoom + ",setsar=1,ass=",
		},
		{
			// The black of the bars stays black: only the picture grows.
			name: "pad: the whole frame is zoomed, the bars stay black",
			fit:  PadFitMode, zoom: testZoom,
			expected: "pad=1080:1920:(ow-iw)/2:(oh-ih)/2:color=black" + zoom + ",setsar=1,ass=",
		},
		{
			name: "HDR with the shade: tone mapped, zoomed, then shaded",
			fit:  CropFitMode, toneMap: true, zoom: testZoom, shade: &Shade{Height: 900, Plateau: 600, Alpha: 153},
			expected: toneMapFilter + zoom + ",setsar=1[base];",
		},
		{name: "no zoom: as before", fit: CropFitMode, expected: "crop=1080:1920,setsar=1,ass="},
		{name: "a zoom out", fit: CropFitMode, zoom: &Zoom{Factor: 0.9}, expectedErr: ErrInvalidZoom},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data := base
			data.Fit, data.ToneMap, data.Shade = tt.fit, tt.toneMap, tt.shade
			data.Cut = &Cut{
				Segments: segments, Options: RenderOptions{FrameRate: Rational{Num: 30, Den: 1}}, Zoom: tt.zoom,
			}

			graph, err := burnGraph(&data)
			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr)

				return
			}

			require.NoError(t, err)
			assert.Contains(t, graph, tt.expected)
			assert.LessOrEqual(t, strings.Count(graph, "overlay=x=0:y=0:enable="), 1)
		})
	}
}

// TestBurnGraphZoomSpans: neighbouring zoomed pieces make one span; without a frame rate the edges are in seconds.
func TestBurnGraphZoomSpans(t *testing.T) {
	t.Parallel()

	cut := &Cut{
		// 2 s, 2 s (zoomed), 0.5 s (zoomed, short), 2 s, 2 s (zoomed)
		Segments: []Segment{{Start: 0, End: 2}, {Start: 3, End: 5}, {Start: 6, End: 6.5}, {Start: 7, End: 9}, {Start: 10, End: 12}},
		Zoom:     testZoom,
	}

	graph, err := zoomGraph(cut, 1080, 1920)
	require.NoError(t, err)
	assert.Contains(t, graph, "enable='gte(t,1.999000)*lt(t,4.499000)+gte(t,6.499000)*lt(t,8.499000)'")

	none, err := zoomGraph(&Cut{Segments: []Segment{{Start: 0, End: 3}}, Zoom: testZoom}, 1080, 1920)
	require.NoError(t, err)
	assert.Empty(t, none, "one piece: nothing to zoom, no extra filters")
}

// TestRunnerZoom: a static picture cut into three 1.5 s pieces — the second one is zoomed in: its frame is the frame of
// the first one cropped to the window and scaled back, the third one is as it was — to the frame at both joints. The
// reel keeps all its frames.
func TestRunnerZoom(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	const (
		small      = "108:192" // frames are compared at a tenth of the size
		frameSize  = 108 * 192
		plainFrame = 20    // the middle of the first piece of 45 frames
		same       = 2.0   // mean difference of the same picture after the encode
		different  = 8.0   // of a picture 9 % closer
		reelFrames = "135" // 3 × 1.5 s at 30 fps
	)

	ctx := t.Context()
	r := newTestRunner(2 * time.Minute)
	dir := t.TempDir()
	src := filepath.Join(dir, "bars.mp4")

	_, err := r.run(ctx, r.ffmpegPath, concat(commonArgs(),
		lavfiInput("smptehdbars=size=540x960:rate=30:duration=6"),
		lavfiInput("sine=frequency=440:sample_rate=48000:duration=6"),
		[]string{videoCodecFlag, videoCodec, audioCodecFlag, renderACodec, shortestFlag, src}), nil)
	require.NoError(t, err)

	reel := burnTestReel(ctx, t, r, dir, src, &Cut{
		Segments: []Segment{{Start: 0, End: 1.5}, {Start: 2, End: 3.5}, {Start: 4, End: 5.5}},
		Options:  RenderOptions{Fade: 0.01, FrameRate: Rational{Num: 30, Den: 1}},
		Zoom:     testZoom,
	})

	in, err := localFile(reel)
	require.NoError(t, err)

	count, err := r.run(ctx, r.ffprobePath, probeEntriesArgs(in, "stream=nb_frames", "csv=p=0"), nil)
	require.NoError(t, err)
	assert.Contains(t, strings.Fields(count), reelFrames, "the zoom does not change the frames of the reel")

	pick := func(filter string) []byte {
		out, err := r.runLimited(ctx, r.ffmpegPath, concat(commonArgs(), inputArgs(in),
			[]string{videoFilterFlag, filter + ",scale=" + small + ",format=gray", "-fps_mode", "passthrough", formatFlag, rawVideoFormat, "-"}),
			1<<20)
		require.NoError(t, err)

		return []byte(out)
	}

	// The middle of the first and the last piece, and the frames around both joints (45 and 90).
	checked := []struct {
		frame  int
		zoomed bool
	}{
		{frame: 20}, {frame: 44}, {frame: 45, zoomed: true}, {frame: 46, zoomed: true},
		{frame: 65, zoomed: true}, {frame: 89, zoomed: true}, {frame: 90}, {frame: 91}, {frame: 110},
	}

	terms := make([]string, 0, len(checked))
	for _, c := range checked {
		terms = append(terms, "eq(n\\,"+strconv.Itoa(c.frame)+")")
	}

	// select passes the frames in the order of the reel: checked is in that order.
	selected := pick("select='" + strings.Join(terms, "+") + "'")
	require.Len(t, selected, len(checked)*frameSize)

	frames := make(map[int][]byte, len(checked))
	for i, c := range checked {
		frames[c.frame] = selected[i*frameSize : (i+1)*frameSize]
	}

	w, h, x, y := zoomWindow(1080, 1920, testZoom.Factor)
	expected := pick("select='eq(n\\," + strconv.Itoa(plainFrame) + ")',crop=" + strconv.Itoa(w) + ":" + strconv.Itoa(h) +
		":" + strconv.Itoa(x) + ":" + strconv.Itoa(y) + ",scale=1080:1920")
	require.Len(t, expected, frameSize)

	plain := frames[plainFrame]
	assert.Greater(t, meanDiff(plain, expected), different, "the zoomed framing differs from the plain one")

	for _, c := range checked {
		reference := plain
		if c.zoomed {
			reference = expected
		}

		assert.Less(t, meanDiff(reference, frames[c.frame]), same, "frame %d zoomed: %t", c.frame, c.zoomed)
		t.Logf("zoom: frame %d (zoomed %t): %.2f from the plain, %.2f from the zoomed", c.frame, c.zoomed,
			meanDiff(plain, frames[c.frame]), meanDiff(expected, frames[c.frame]))
	}
}

func meanDiff(a, b []byte) float64 {
	sum := 0

	for i := range a {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}

		sum += d
	}

	return float64(sum) / float64(len(a))
}
