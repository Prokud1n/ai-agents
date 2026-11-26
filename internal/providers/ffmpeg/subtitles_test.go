package ffmpeg

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testOutput        = "file:/out/reel.mp4"
	testSubtitlesName = "subtitles.ass"
	testFontsName     = "fonts"
	testJobDir        = "/tmp/job"
)

func TestBurnSubtitlesArgs(t *testing.T) {
	t.Parallel()

	base := BurnSubtitlesData{SubtitlesName: testSubtitlesName, FontsName: testFontsName, Width: 1080, Height: 1920, Fit: PadFitMode}
	encode := []string{
		videoCodecFlag, "libx264", "-preset", "veryfast", "-crf", "20", "-maxrate", "10M", "-bufsize", "20M",
		"-profile:v", "high", "-pix_fmt", "yuv420p",
		"-colorspace", bt709, "-color_primaries", bt709, "-color_trc", bt709, "-color_range", "tv",
		audioCodecFlag, "aac", "-b:a", outAudioBits, "-ar", "48000", "-ac", "2", "-movflags", "+faststart", formatFlag, "mp4",
	}
	head := []string{
		"-hide_banner", "-nostdin", "-nostats", "-y", "-loglevel", "info", "-protocol_whitelist", "file", "-i", testVideo,
		"-filter_complex",
	}
	maps := []string{mapFlag, "[v]", mapFlag, "0:a:0?"}

	tests := []struct {
		name        string
		input       func(d BurnSubtitlesData) BurnSubtitlesData
		expected    []string
		expectedErr error
	}{
		{
			name:  "pad keeps the whole frame",
			input: func(d BurnSubtitlesData) BurnSubtitlesData { return d },
			expected: concat(head, []string{
				"[0:v]scale=1080:1920:force_original_aspect_ratio=decrease:force_divisible_by=2:out_color_matrix=bt709:out_range=tv," +
					"pad=1080:1920:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,ass=filename=subtitles.ass:fontsdir=fonts," + colorTagsFilter + "[v]",
			}, maps, encode, []string{testOutput}),
		},
		{
			name: "crop fills the frame",
			input: func(d BurnSubtitlesData) BurnSubtitlesData {
				d.Fit = CropFitMode

				return d
			},
			expected: concat(head, []string{
				"[0:v]scale=1080:1920:force_original_aspect_ratio=increase:force_divisible_by=2:out_color_matrix=bt709:out_range=tv," +
					"crop=1080:1920,setsar=1,ass=filename=subtitles.ass:fontsdir=fonts," + colorTagsFilter + "[v]",
			}, maps, encode, []string{testOutput}),
		},
		{
			name: "top shade under the subtitles",
			input: func(d BurnSubtitlesData) BurnSubtitlesData {
				d.Shade = &Shade{Height: 900, Plateau: 600, Alpha: 153}

				return d
			},
			expected: concat(head, []string{
				"[0:v]scale=1080:1920:force_original_aspect_ratio=decrease:force_divisible_by=2:out_color_matrix=bt709:out_range=tv," +
					"pad=1080:1920:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1[base];" +
					"color=c=black:s=1080x900:r=1:d=1,format=rgba,geq=r=0:g=0:b=0:a='153*if(lt(Y,600),1,(1+cos(PI*(Y-600)/300))/2)'[shade];" +
					"[base][shade]overlay=x=0:y=0,ass=filename=subtitles.ass:fontsdir=fonts," + colorTagsFilter + "[v]",
			}, maps, encode, []string{testOutput}),
		},
		{
			name: "bottom shade",
			input: func(d BurnSubtitlesData) BurnSubtitlesData {
				d.Shade = &Shade{Height: 600, Plateau: 400, Alpha: 100, Bottom: true}

				return d
			},
			expected: concat(head, []string{
				"[0:v]scale=1080:1920:force_original_aspect_ratio=decrease:force_divisible_by=2:out_color_matrix=bt709:out_range=tv," +
					"pad=1080:1920:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1[base];" +
					"color=c=black:s=1080x600:r=1:d=1,format=rgba," +
					"geq=r=0:g=0:b=0:a='100*if(lt((600-1-Y),400),1,(1+cos(PI*((600-1-Y)-400)/200))/2)'[shade];" +
					"[base][shade]overlay=x=0:y=1320,ass=filename=subtitles.ass:fontsdir=fonts," + colorTagsFilter + "[v]",
			}, maps, encode, []string{testOutput}),
		},
		{
			// The 4K HDR frame is scaled as it is (no matrix conversion before the tone mapping), then tone mapped at
			// 1080×1920.
			name: "an HDR picture is scaled first, then tone mapped",
			input: func(d BurnSubtitlesData) BurnSubtitlesData {
				d.ToneMap = true

				return d
			},
			expected: concat(head, []string{
				"[0:v]scale=1080:1920:force_original_aspect_ratio=decrease:force_divisible_by=2," +
					"pad=1080:1920:(ow-iw)/2:(oh-ih)/2:color=black," +
					"zscale=t=linear:npl=100,format=gbrpf32le,zscale=p=bt709,tonemap=hable:desat=0," +
					"zscale=t=bt709:m=bt709:r=tv,format=yuv420p," +
					"setsar=1,ass=filename=subtitles.ass:fontsdir=fonts," + colorTagsFilter + "[v]",
			}, maps, encode, []string{testOutput}),
		},
		{
			name: "a cropped HDR picture with the shade: the shade and the subtitles are on the tone mapped frame",
			input: func(d BurnSubtitlesData) BurnSubtitlesData {
				d.ToneMap, d.Fit = true, CropFitMode
				d.Shade = &Shade{Height: 900, Plateau: 600, Alpha: 153}

				return d
			},
			expected: concat(head, []string{
				"[0:v]scale=1080:1920:force_original_aspect_ratio=increase:force_divisible_by=2,crop=1080:1920," +
					toneMapFilter + ",setsar=1[base];" +
					"color=c=black:s=1080x900:r=1:d=1,format=rgba,geq=r=0:g=0:b=0:a='153*if(lt(Y,600),1,(1+cos(PI*(Y-600)/300))/2)'[shade];" +
					"[base][shade]overlay=x=0:y=0,ass=filename=subtitles.ass:fontsdir=fonts," + colorTagsFilter + "[v]",
			}, maps, encode, []string{testOutput}),
		},
		{
			name: "shade taller than the frame",
			input: func(d BurnSubtitlesData) BurnSubtitlesData {
				d.Shade = &Shade{Height: 2000, Plateau: 100, Alpha: 100}

				return d
			},
			expectedErr: ErrInvalidShade,
		},
		{
			name: "plateau as high as the shade",
			input: func(d BurnSubtitlesData) BurnSubtitlesData {
				d.Shade = &Shade{Height: 500, Plateau: 500, Alpha: 100}

				return d
			},
			expectedErr: ErrInvalidShade,
		},
		{
			name: "path instead of a name is refused",
			input: func(d BurnSubtitlesData) BurnSubtitlesData {
				d.SubtitlesName = "../x.ass"

				return d
			},
			expectedErr: ErrUnsafeName,
		},
		{
			name: "filter syntax in a name is refused",
			input: func(d BurnSubtitlesData) BurnSubtitlesData {
				d.FontsName = "f:fontsdir=/etc,drawtext"

				return d
			},
			expectedErr: ErrUnsafeName,
		},
		{
			name: "odd size",
			input: func(d BurnSubtitlesData) BurnSubtitlesData {
				d.Width = 1081

				return d
			},
			expectedErr: ErrInvalidSize,
		},
		{
			name: "unknown fit",
			input: func(d BurnSubtitlesData) BurnSubtitlesData {
				d.Fit = "stretch"

				return d
			},
			expectedErr: ErrInvalidFitMode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data := tt.input(base)

			got, _, err := burnSubtitlesArgs(testVideo, testOutput, &data, unknownMajor)
			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

// TestBurnSubtitlesArgsWithCut: the rough cut goes into the same graph; the graph is read from a file in Dir,
// the audio is the joined one.
func TestBurnSubtitlesArgsWithCut(t *testing.T) {
	t.Parallel()

	data := BurnSubtitlesData{
		SubtitlesName: testSubtitlesName, FontsName: testFontsName, Width: 1080, Height: 1920, Fit: PadFitMode, Dir: testJobDir,
		Cut: &Cut{
			Segments: []Segment{{Start: 1, End: 2}, {Start: 3, End: 4}},
			Options:  RenderOptions{Fade: 0.01, FrameRate: Rational{Num: 30, Den: 1}},
		},
	}

	tests := []struct {
		name     string
		major    int
		expected []string
	}{
		{name: "ffmpeg 8", major: 8, expected: []string{"-/filter_complex", "graph.txt", mapFlag, "[" + videoLabel + "]", mapFlag, "[a]"}},
		{name: "ffmpeg 6", major: 6, expected: []string{"-filter_complex_script", "graph.txt", mapFlag, "[" + videoLabel + "]", mapFlag, "[a]"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			args, graph, err := burnSubtitlesArgs(testVideo, testOutput, &data, tt.major)
			require.NoError(t, err)

			assert.Equal(t, tt.expected, args[10:16])
			assert.Equal(t, testOutput, args[len(args)-1])

			cut := segmentsGraph(data.Cut.Segments, data.Cut.Options, "cut", "a")
			assert.Equal(t, cut+";[cut]scale=1080:1920:force_original_aspect_ratio=decrease:force_divisible_by=2:out_color_matrix=bt709:out_range=tv,"+
				"pad=1080:1920:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,ass=filename=subtitles.ass:fontsdir=fonts,"+colorTagsFilter+"[v]", graph)
			assert.True(t, strings.HasSuffix(cut, "concat=n=2:v=1:a=1[cut][a]"))
		})
	}

	empty := data
	empty.Cut = &Cut{}
	_, _, err := burnSubtitlesArgs(testVideo, testOutput, &empty, 8)
	require.ErrorIs(t, err, errNoSegments)

	noDir := data
	noDir.Dir = ""
	_, _, err = burnSubtitlesArgs(testVideo, testOutput, &noDir, 8)
	require.ErrorIs(t, err, ErrNoWorkDir)
}

func TestParseFontSelections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected []FontSelection
	}{
		{name: "no lines", input: "", expected: []FontSelection{}},
		{
			name: "font from fontsdir and a system fallback",
			input: "[Parsed_ass_0 @ 0x55] fontselect: (Manrope, 700, 0) -> Manrope-Bold, 0, Manrope-Bold\n" +
				"[Parsed_ass_0 @ 0x55] fontselect: (Fira Sans Extra Condensed, 400, 1) -> " +
				"/usr/share/fonts/opentype/inter/Inter-Italic.otf, 0, Inter-Italic\n" +
				"[Parsed_ass_0 @ 0x55] fontselect: failed to find any fallback with glyph 0x1F600 for font: (Manrope, 700, 0)\n",
			expected: []FontSelection{
				{Family: "Manrope", Weight: 700, Font: "Manrope-Bold", PostScriptName: "Manrope-Bold"},
				{
					Family: "Fira Sans Extra Condensed", Weight: 400, Italic: 1,
					Font: "/usr/share/fonts/opentype/inter/Inter-Italic.otf", PostScriptName: "Inter-Italic",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, parseFontSelections(tt.input))
		})
	}
}

// TestBurnGraphOrder: in one pass the source is brought to the draft's frame rate first (fps, before split), cut by
// frames of that rate, joined, scaled to 1080×1920 and only then tone mapped, shaded and drawn on.
func TestBurnGraphOrder(t *testing.T) {
	t.Parallel()

	data := BurnSubtitlesData{
		SubtitlesName: testSubtitlesName, FontsName: testFontsName, Width: 1080, Height: 1920, Fit: CropFitMode, Dir: testJobDir,
		ToneMap: true, Shade: &Shade{Height: 900, Plateau: 600, Alpha: 153},
		Cut: &Cut{
			Segments: []Segment{{Start: 1, End: 2}, {Start: 3, End: 4.5}},
			Options:  RenderOptions{Fade: 0.01, FrameRate: Rational{Num: 30, Den: 1}},
		},
	}

	args, graph, err := burnSubtitlesArgs(testVideo, testOutput, &data, 8)
	require.NoError(t, err)

	order := []string{
		"fps=fps=30/1:start_time=0:round=up,settb=1/30", "split=2", "trim=start_pts=30:end_pts=60",
		"trim=start_pts=90:end_pts=135", "concat=n=2", "[cut]scale=1080:1920", "crop=1080:1920",
		"zscale=t=linear", "format=yuv420p", "setsar=1[base]", "overlay=", "ass=filename=", "setparams=",
	}

	last := -1

	for _, filter := range order {
		at := strings.Index(graph, filter)
		require.GreaterOrEqual(t, at, 0, "%s is in the graph", filter)
		assert.Greater(t, at, last, "%s comes after the filters before it", filter)
		last = at
	}

	assert.Equal(t, 1, strings.Count(graph, "zscale=t=linear"), "the picture is tone mapped once, after the cut")
	assert.Contains(t, strings.Join(args, " "), "-preset veryfast -crf 20")
	assert.NotContains(t, args, "-threads", "ffmpeg picks the threads for the decoder, the filters and x264")
}
