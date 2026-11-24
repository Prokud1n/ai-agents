package subtitles

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
)

const (
	repoFontsDir  = "../../assets/fonts"
	mainFont      = "Manrope Medium"
	accentFont    = "Fira Sans Extra Condensed ExtraBold"
	manropeMedium = "Manrope-Medium"
	inputVideo    = "in.mp4"
	outputVideo   = "out.mp4"
)

// fakeRenderer records the render request and what was in its directory at that moment.
type fakeRenderer struct {
	got    ffmpeg.BurnSubtitlesData
	ass    string
	fonts  []string
	report *ffmpeg.BurnReport
	fail   error
}

func (f *fakeRenderer) BurnSubtitles(_ context.Context, data *ffmpeg.BurnSubtitlesData) (*ffmpeg.BurnReport, error) {
	f.got = *data

	if f.fail != nil {
		// ffmpeg leaves a partial file behind when it fails
		if err := os.WriteFile(data.Output, []byte("partial"), 0o600); err != nil {
			return nil, err
		}

		return nil, f.fail
	}

	if err := os.WriteFile(data.Output, []byte("video"), 0o600); err != nil {
		return nil, err
	}

	ass, err := os.ReadFile(filepath.Join(data.Dir, data.SubtitlesName))
	if err != nil {
		return nil, err
	}

	f.ass = string(ass)

	entries, err := os.ReadDir(filepath.Join(data.Dir, data.FontsName))
	if err != nil {
		return nil, err
	}

	for _, e := range entries {
		f.fonts = append(f.fonts, e.Name())
	}

	return f.report, nil
}

func newTestService(t *testing.T, renderer videoRenderer, cfg *config.Subtitles) *Service {
	t.Helper()

	svc, err := NewService(renderer, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)

	return svc
}

func TestBurnSubtitlesPreparesRender(t *testing.T) {
	t.Parallel()

	fontsDir := t.TempDir()
	for _, name := range []string{"Manrope-Bold.ttf", "Fira.OTF", "OFL.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(fontsDir, name), []byte("font"), 0o600))
	}

	require.NoError(t, os.Mkdir(filepath.Join(fontsDir, "nested.ttf"), 0o700))
	require.NoError(t, os.Symlink("/etc/hostname", filepath.Join(fontsDir, "link.ttf")))

	renderer := &fakeRenderer{report: &ffmpeg.BurnReport{Fonts: []ffmpeg.FontSelection{
		{Family: mainFont, Weight: 700, Font: manropeMedium, PostScriptName: manropeMedium},
	}}}
	svc := newTestService(t, renderer, &config.Subtitles{FontsDir: fontsDir})
	outDir := t.TempDir()
	output := filepath.Join(outDir, outputVideo)

	result, err := svc.BurnSubtitles(t.Context(), &BurnSubtitlesData{
		CreateSubtitlesData: CreateSubtitlesData{Transcript: speech(wPrivetCapExcl)},
		VideoPath:           inputVideo,
		OutputPath:          output,
	})

	require.NoError(t, err)
	assert.Equal(t, &BurnResult{Cues: 1, FontFallbacks: []string{}}, result)

	assert.Equal(t, inputVideo, renderer.got.Input)
	assert.Equal(t, outDir, filepath.Dir(renderer.got.Output), "rendered next to the output")
	assert.True(t, strings.HasSuffix(renderer.got.Output, partialSuffix))
	// the even part ends under the tallest subtitle: margin 260 + 3 lines of 64 px × 1.25 = 500 px
	assert.Equal(t, &ffmpeg.Shade{Height: 820, Plateau: 500, Alpha: 153}, renderer.got.Shade)
	assert.Equal(t, assName, renderer.got.SubtitlesName)
	assert.Equal(t, fontsName, renderer.got.FontsName)
	assert.Equal(t, FrameWidth, renderer.got.Width)
	assert.Equal(t, FrameHeight, renderer.got.Height)
	assert.Equal(t, ffmpeg.PadFitMode, renderer.got.Fit)
	assert.Nil(t, renderer.got.Cut, "no cut: the whole video")
	assert.Contains(t, renderer.ass, wPrivetCapExcl)
	assert.ElementsMatch(t, []string{"Manrope-Bold.ttf", "Fira.OTF"}, renderer.fonts, "only regular font files are copied")

	_, err = os.Stat(renderer.got.Dir)
	require.ErrorIs(t, err, os.ErrNotExist, "temp dir is removed")

	content, err := os.ReadFile(output)
	require.NoError(t, err)
	assert.Equal(t, "video", string(content))

	entries, err := os.ReadDir(outDir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no partial file is left")
}

// TestBurnSubtitlesPassesCut: the rough cut goes to the renderer as it is, for one pass.
func TestBurnSubtitlesPassesCut(t *testing.T) {
	t.Parallel()

	fontsDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fontsDir, "Manrope-Bold.ttf"), []byte("font"), 0o600))

	renderer := &fakeRenderer{report: &ffmpeg.BurnReport{}}
	cut := &ffmpeg.Cut{Segments: []ffmpeg.Segment{{Start: 0.5, End: 1.5}}, Options: ffmpeg.RenderOptions{Fade: 0.01}}

	_, err := newTestService(t, renderer, &config.Subtitles{FontsDir: fontsDir}).BurnSubtitles(t.Context(), &BurnSubtitlesData{
		CreateSubtitlesData: CreateSubtitlesData{Transcript: speech(wPrivetCapExcl)},
		VideoPath:           inputVideo,
		OutputPath:          filepath.Join(t.TempDir(), outputVideo),
		Cut:                 cut,
	})
	require.NoError(t, err)
	assert.Same(t, cut, renderer.got.Cut)
}

func TestBurnSubtitlesFailedRenderLeavesNoOutput(t *testing.T) {
	t.Parallel()

	errRender := errors.New("ffmpeg failed")
	renderer := &fakeRenderer{fail: errRender}
	svc := newTestService(t, renderer, &config.Subtitles{FontsDir: repoFontsDir})
	outDir := t.TempDir()

	_, err := svc.BurnSubtitles(t.Context(), &BurnSubtitlesData{
		CreateSubtitlesData: CreateSubtitlesData{Transcript: speech(wPrivetCapExcl)},
		VideoPath:           inputVideo,
		OutputPath:          filepath.Join(outDir, outputVideo),
	})

	require.ErrorIs(t, err, errRender)

	entries, err := os.ReadDir(outDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "neither the output nor the partial file is left")
}

func TestShade(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		modify   func(s *Style)
		expected *ffmpeg.Shade
	}{
		{name: "top", modify: func(*Style) {}, expected: &ffmpeg.Shade{Height: 820, Plateau: 500, Alpha: 153}},
		{
			name:     "bottom",
			modify:   func(s *Style) { s.Layout.Position = BottomPosition },
			expected: &ffmpeg.Shade{Height: 1000, Plateau: 680, Alpha: 153, Bottom: true},
		},
		{name: "middle has none", modify: func(s *Style) { s.Layout.Position = MiddlePosition }},
		{name: "disabled", modify: func(s *Style) { s.Vignette.Enabled = false }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			style := DefaultStyle()
			tt.modify(&style)

			assert.Equal(t, tt.expected, shade(&style))
		})
	}
}

func TestBurnSubtitlesErrors(t *testing.T) {
	t.Parallel()

	emptyFonts := t.TempDir()
	invalid := DefaultStyle()
	invalid.Layout.MarginTop = 0

	tests := []struct {
		name        string
		fontsDir    string
		input       BurnSubtitlesData
		expectedErr error
	}{
		{
			name:  "no transcript",
			input: BurnSubtitlesData{VideoPath: inputVideo, OutputPath: outputVideo}, fontsDir: repoFontsDir,
			expectedErr: ErrNoTranscript,
		},
		{
			name:     "no output",
			input:    BurnSubtitlesData{CreateSubtitlesData: CreateSubtitlesData{Transcript: speech(wordOne)}, VideoPath: inputVideo},
			fontsDir: repoFontsDir, expectedErr: ErrEmptyPath,
		},
		{
			name: "invalid style",
			input: BurnSubtitlesData{
				CreateSubtitlesData: CreateSubtitlesData{Transcript: speech(wordOne), Style: &invalid},
				VideoPath:           inputVideo, OutputPath: outputVideo,
			},
			fontsDir: repoFontsDir, expectedErr: ErrInvalidStyle,
		},
		{
			name: "no fonts",
			input: BurnSubtitlesData{
				CreateSubtitlesData: CreateSubtitlesData{Transcript: speech(wordOne)}, VideoPath: inputVideo, OutputPath: outputVideo,
			},
			fontsDir: emptyFonts, expectedErr: ErrNoFonts,
		},
		{
			name: "missing fonts dir",
			input: BurnSubtitlesData{
				CreateSubtitlesData: CreateSubtitlesData{Transcript: speech(wordOne)}, VideoPath: inputVideo, OutputPath: outputVideo,
			},
			fontsDir: filepath.Join(emptyFonts, "missing"), expectedErr: os.ErrNotExist,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newTestService(t, &fakeRenderer{}, &config.Subtitles{FontsDir: tt.fontsDir})

			_, err := svc.BurnSubtitles(t.Context(), &tt.input)

			require.ErrorIs(t, err, tt.expectedErr)
		})
	}
}

func TestNewServiceStyleFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	valid := filepath.Join(dir, "brand.yaml")
	broken := filepath.Join(dir, "broken.yaml")

	require.NoError(t, os.WriteFile(valid, []byte("layout:\n  position: bottom\n"), 0o600))
	require.NoError(t, os.WriteFile(broken, []byte("layout:\n  margin_top: 10\n"), 0o600))

	tests := []struct {
		name        string
		styleFile   string
		expected    Position
		expectedErr error
	}{
		{name: "built-in style", expected: TopPosition},
		{name: "brand book file", styleFile: valid, expected: BottomPosition},
		{name: "invalid file fails the start", styleFile: broken, expectedErr: ErrInvalidStyle},
		{name: "missing file fails the start", styleFile: filepath.Join(dir, "none.yaml"), expectedErr: os.ErrNotExist},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, err := NewService(&fakeRenderer{}, &config.Subtitles{StyleFile: tt.styleFile}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, svc.Style().Layout.Position)
		})
	}
}

func TestFontFallbacks(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()

	tests := []struct {
		name     string
		input    []ffmpeg.FontSelection
		shown    bool
		expected []string
	}{
		{
			name: "brand fonts found",
			input: []ffmpeg.FontSelection{
				{Family: mainFont, Weight: 700, Font: manropeMedium, PostScriptName: manropeMedium},
				{Family: accentFont, Weight: 400, Font: "FiraSansExtraCondensed-ExtraBold", PostScriptName: "FiraSansExtraCondensed-ExtraBold"},
			},
			shown:    true,
			expected: []string{},
		},
		{
			name: "system font instead of the brand one, reported once",
			input: []ffmpeg.FontSelection{
				{Family: mainFont, Weight: 700, Font: "/usr/share/fonts/Inter-Bold.otf", PostScriptName: "Inter-Bold"},
				{Family: mainFont, Weight: 700, Font: "/usr/share/fonts/Inter-Bold.otf", PostScriptName: "Inter-Bold"},
				{Family: "Arial", Weight: 400, Font: "/usr/share/fonts/DejaVuSans.ttf", PostScriptName: "DejaVuSans"},
			},
			shown:    true,
			expected: []string{"Manrope Medium -> /usr/share/fonts/Inter-Bold.otf"},
		},
		{
			name:     "brand font installed in the system is fine",
			input:    []ffmpeg.FontSelection{{Family: mainFont, Weight: 400, Font: "/usr/share/fonts/manrope/Manrope-Medium.ttf"}},
			shown:    true,
			expected: []string{},
		},
		{
			name:     "libass reported nothing while subtitles were shown",
			shown:    true,
			expected: []string{"Manrope Medium -> (libass reported no font)"},
		},
		{name: "nothing shown, nothing reported", expected: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, fontFallbacks(&style, tt.input, tt.shown))
		})
	}
}
