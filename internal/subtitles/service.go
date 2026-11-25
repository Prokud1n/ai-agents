package subtitles

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
)

const (
	tempDirPattern = "subtitles-*"
	// Plain names inside the temp dir: ffmpeg runs there, so no user path reaches the filter graph.
	assName   = "subtitles.ass"
	fontsName = "fonts"

	partialSuffix = ".part.mp4"

	fileMode   = 0o600
	outputMode = 0o644
	dirMode    = 0o700
)

var (
	ErrNoTranscript = errors.New("transcript is required")
	ErrEmptyPath    = errors.New("video and output paths are required")
	ErrNoFonts      = errors.New("no TTF/OTF fonts in the fonts directory")

	fontExtensions = map[string]bool{".ttf": true, ".otf": true, ".ttc": true}
)

type videoRenderer interface {
	BurnSubtitles(ctx context.Context, data *ffmpeg.BurnSubtitlesData) (*ffmpeg.BurnReport, error)
}

// Service makes Reels subtitles in the brand style from a transcript: an ASS file for libass,
// or a 1080×1920 video with the subtitles burned in.
type Service struct {
	renderer videoRenderer
	fontsDir string
	style    Style
	log      *slog.Logger
}

func NewService(renderer videoRenderer, cfg *config.Subtitles, log *slog.Logger) (*Service, error) {
	style := DefaultStyle()

	if cfg.StyleFile != "" {
		loaded, err := LoadStyle(cfg.StyleFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load SUBTITLES_STYLE_FILE: %w", err)
		}

		style = loaded
	}

	return &Service{renderer: renderer, fontsDir: cfg.FontsDir, style: style, log: log}, nil
}

// Style returns the service style (a copy).
func (s *Service) Style() Style {
	style := s.style
	style.Accent.Words = append([]string(nil), s.style.Accent.Words...)

	return style
}

// CreateSubtitles renders the ASS document without touching the video.
func (s *Service) CreateSubtitles(data *CreateSubtitlesData) (*Subtitles, error) {
	style, err := s.resolveStyle(data)
	if err != nil {
		return nil, err
	}

	ass, cues := buildASS(data.Transcript, &style, data.Accents, data.Karaoke, data.Titles, data.Hook)

	return &Subtitles{ASS: []byte(ass), Cues: cues}, nil
}

// BurnSubtitles renders the subtitles onto the video with libass and the fonts from SUBTITLES_FONTS_DIR.
func (s *Service) BurnSubtitles(ctx context.Context, data *BurnSubtitlesData) (*BurnResult, error) {
	if data.VideoPath == "" || data.OutputPath == "" {
		return nil, fmt.Errorf("failed to burn subtitles: %w", ErrEmptyPath)
	}

	style, err := s.resolveStyle(&data.CreateSubtitlesData)
	if err != nil {
		return nil, err
	}

	ass, cues := buildASS(data.Transcript, &style, data.Accents, data.Karaoke, data.Titles, data.Hook)

	dir, err := os.MkdirTemp("", tempDirPattern)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			s.log.ErrorContext(ctx, "subtitles: failed to remove temp dir", "error", err, "dir", dir)
		}
	}()

	if err := os.WriteFile(filepath.Join(dir, assName), []byte(ass), fileMode); err != nil {
		return nil, fmt.Errorf("failed to write subtitles: %w", err)
	}

	if err := copyFonts(s.fontsDir, filepath.Join(dir, fontsName)); err != nil {
		return nil, err
	}

	fit := data.Fit
	if fit == "" {
		fit = ffmpeg.PadFitMode
	}

	report, err := s.render(ctx, data.OutputPath, &ffmpeg.BurnSubtitlesData{
		Input: data.VideoPath, Dir: dir,
		SubtitlesName: assName, FontsName: fontsName,
		Width: FrameWidth, Height: FrameHeight, Fit: fit, Shade: shade(&style), Cut: data.Cut,
		ToneMap: data.ToneMap,
	})
	if err != nil {
		return nil, err
	}

	result := &BurnResult{
		Cues: cues, FontFallbacks: fontFallbacks(&style, report.Fonts, cues > 0), MissingGlyphs: report.MissingGlyphs,
		Fonts: report.Fonts,
	}
	s.logResult(ctx, result)

	return result, nil
}

// render writes the video to a temporary file next to the output and renames it into place only after
// ffmpeg succeeded, so a failed or interrupted render never leaves a broken file at the output path.
func (s *Service) render(ctx context.Context, output string, data *ffmpeg.BurnSubtitlesData) (*ffmpeg.BurnReport, error) {
	tmp, err := os.CreateTemp(filepath.Dir(output), "."+filepath.Base(output)+".*"+partialSuffix)
	if err != nil {
		return nil, fmt.Errorf("failed to create output file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("failed to create output file: %w", err)
	}

	renamed := false

	defer func() {
		if renamed {
			return
		}

		if err := os.Remove(tmp.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.log.ErrorContext(ctx, "subtitles: failed to remove partial output", "error", err, "path", tmp.Name())
		}
	}()

	data.Output = tmp.Name()

	report, err := s.renderer.BurnSubtitles(ctx, data)
	if err != nil {
		return nil, fmt.Errorf("failed to render video: %w", err)
	}

	// The video is read by other processes (the bot, the user): readable like a file ffmpeg would create.
	if err := os.Chmod(tmp.Name(), outputMode); err != nil {
		return nil, fmt.Errorf("failed to set output permissions: %w", err)
	}

	if err := os.Rename(tmp.Name(), output); err != nil {
		return nil, fmt.Errorf("failed to move the rendered video to %s: %w", output, err)
	}

	renamed = true

	return report, nil
}

// shade is the vignette of the style at the subtitles' edge; none in the middle position or when disabled.
func shade(style *Style) *ffmpeg.Shade {
	v := style.Vignette
	if !v.Enabled || style.Layout.Position == MiddlePosition {
		return nil
	}

	plateau, bottom := style.vignetteSpan()

	return &ffmpeg.Shade{
		Height:  plateau + v.Fade,
		Plateau: plateau,
		Alpha:   int(math.Round(v.Opacity * opaque)),
		Bottom:  bottom,
	}
}

func (s *Service) resolveStyle(data *CreateSubtitlesData) (Style, error) {
	if data.Transcript == nil {
		return Style{}, fmt.Errorf("failed to create subtitles: %w", ErrNoTranscript)
	}

	if data.Style == nil {
		return s.Style(), nil
	}

	style := *data.Style
	if err := style.Validate(); err != nil {
		return Style{}, fmt.Errorf("failed to create subtitles: %w", err)
	}

	return style, nil
}

func (s *Service) logResult(ctx context.Context, r *BurnResult) {
	for _, f := range r.FontFallbacks {
		s.log.WarnContext(ctx, "subtitles: brand font not found, libass used another one", "font", f, "fonts_dir", s.fontsDir)
	}

	for _, g := range r.MissingGlyphs {
		s.log.WarnContext(ctx, "subtitles: character missing in the fonts", "libass", g)
	}

	s.log.InfoContext(ctx, "subtitles: video rendered", "cues", r.Cues, "font_fallbacks", len(r.FontFallbacks))
}

// copyFonts copies the font files to dst, the fontsdir of the render. Only regular files are copied, through
// os.Root, so neither a link in the fonts directory nor a crafted name can reach a file elsewhere.
func copyFonts(src, dst string) error {
	if err := os.Mkdir(dst, dirMode); err != nil {
		return fmt.Errorf("failed to create fonts dir: %w", err)
	}

	from, err := os.OpenRoot(src)
	if err != nil {
		return fmt.Errorf("failed to open fonts dir (SUBTITLES_FONTS_DIR): %w", err)
	}
	defer from.Close()

	to, err := os.OpenRoot(dst)
	if err != nil {
		return fmt.Errorf("failed to open fonts dir: %w", err)
	}
	defer to.Close()

	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("failed to read fonts dir (SUBTITLES_FONTS_DIR): %w", err)
	}

	copied := 0

	for _, e := range entries {
		if !e.Type().IsRegular() || !fontExtensions[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}

		if err := copyFile(from, to, e.Name()); err != nil {
			return err
		}

		copied++
	}

	if copied == 0 {
		return fmt.Errorf("failed to prepare fonts from %s: %w", src, ErrNoFonts)
	}

	return nil
}

func copyFile(from, to *os.Root, name string) error {
	in, err := from.Open(name)
	if err != nil {
		return fmt.Errorf("failed to read font: %w", err)
	}
	defer in.Close()

	out, err := to.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		return fmt.Errorf("failed to copy font: %w", err)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()

		return fmt.Errorf("failed to copy font: %w", err)
	}

	if err := out.Close(); err != nil {
		return fmt.Errorf("failed to copy font: %w", err)
	}

	return nil
}

// fontFallbacks lists style families that libass replaced with another font. A family is matched by the
// selected font's PostScript name or file: "Fira Sans Extra Condensed" -> "FiraSansExtraCondensed-Bold".
// With subtitles on screen libass must report the main font; if it reported nothing, that is a fallback too.
func fontFallbacks(style *Style, fonts []ffmpeg.FontSelection, shown bool) []string {
	fallbacks := []string{}
	reported := map[string]bool{}

	for _, f := range fonts {
		family := squash(f.Family)
		if family != squash(style.Text.Font) && family != squash(style.Accent.Font) && family != squash(style.Title.Font) &&
			family != squash(style.Hook.Font) {
			continue
		}

		reported[family] = true

		found := strings.Contains(squash(f.PostScriptName), family) || strings.Contains(squash(filepath.Base(f.Font)), family)
		if line := f.Family + " -> " + f.Font; !found && !slices.Contains(fallbacks, line) {
			fallbacks = append(fallbacks, line)
		}
	}

	if shown && !reported[squash(style.Text.Font)] {
		fallbacks = append(fallbacks, style.Text.Font+" -> (libass reported no font)")
	}

	return fallbacks
}

// squash lowercases a font name and drops spaces, dashes and underscores.
func squash(name string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' || r == '_' {
			return -1
		}

		return r
	}, strings.ToLower(name))
}
