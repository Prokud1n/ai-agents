package ffmpeg

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testOutput = "file:/out/reel.mp4"

func TestBurnSubtitlesArgs(t *testing.T) {
	t.Parallel()

	base := BurnSubtitlesData{SubtitlesName: "subtitles.ass", FontsName: "fonts", Width: 1080, Height: 1920, Fit: PadFitMode}
	encode := []string{
		videoCodecFlag, "libx264", "-preset", "medium", "-crf", "20", "-profile:v", "high", "-pix_fmt", "yuv420p",
		audioCodecFlag, "aac", "-b:a", "128k", "-ar", "48000", "-movflags", "+faststart", formatFlag, "mp4",
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
				"[0:v]scale=1080:1920:force_original_aspect_ratio=decrease:force_divisible_by=2," +
					"pad=1080:1920:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,ass=filename=subtitles.ass:fontsdir=fonts[v]",
			}, maps, encode, []string{testOutput}),
		},
		{
			name: "crop fills the frame",
			input: func(d BurnSubtitlesData) BurnSubtitlesData {
				d.Fit = CropFitMode

				return d
			},
			expected: concat(head, []string{
				"[0:v]scale=1080:1920:force_original_aspect_ratio=increase:force_divisible_by=2," +
					"crop=1080:1920,setsar=1,ass=filename=subtitles.ass:fontsdir=fonts[v]",
			}, maps, encode, []string{testOutput}),
		},
		{
			name: "top shade under the subtitles",
			input: func(d BurnSubtitlesData) BurnSubtitlesData {
				d.Shade = &Shade{Height: 900, Plateau: 600, Alpha: 153}

				return d
			},
			expected: concat(head, []string{
				"[0:v]scale=1080:1920:force_original_aspect_ratio=decrease:force_divisible_by=2," +
					"pad=1080:1920:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1[base];" +
					"color=c=black:s=1080x900:r=1:d=1,format=rgba,geq=r=0:g=0:b=0:a='153*if(lt(Y,600),1,(1+cos(PI*(Y-600)/300))/2)'[shade];" +
					"[base][shade]overlay=x=0:y=0,ass=filename=subtitles.ass:fontsdir=fonts[v]",
			}, maps, encode, []string{testOutput}),
		},
		{
			name: "bottom shade",
			input: func(d BurnSubtitlesData) BurnSubtitlesData {
				d.Shade = &Shade{Height: 600, Plateau: 400, Alpha: 100, Bottom: true}

				return d
			},
			expected: concat(head, []string{
				"[0:v]scale=1080:1920:force_original_aspect_ratio=decrease:force_divisible_by=2," +
					"pad=1080:1920:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1[base];" +
					"color=c=black:s=1080x600:r=1:d=1,format=rgba," +
					"geq=r=0:g=0:b=0:a='100*if(lt((600-1-Y),400),1,(1+cos(PI*((600-1-Y)-400)/200))/2)'[shade];" +
					"[base][shade]overlay=x=0:y=1320,ass=filename=subtitles.ass:fontsdir=fonts[v]",
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

			got, err := burnSubtitlesArgs(testVideo, testOutput, &data)
			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
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
