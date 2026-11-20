package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"
	"go.uber.org/fx"
)

// Subtitles configures the subtitle renderer (the Montage agent, cmd/subtitles).
type Subtitles struct {
	// Directory with the subtitle fonts (TTF/OTF); relative to the working directory. The media image sets /app/fonts.
	FontsDir string `env:"SUBTITLES_FONTS_DIR" envDefault:"assets/fonts"`
	// YAML style file (brand book); empty means the built-in style (assets/subtitles/style.yaml shows it).
	StyleFile string `env:"SUBTITLES_STYLE_FILE"`
}

// SubtitlesCommand is the config of the standalone subtitles command: ffmpeg and the subtitle settings only.
type SubtitlesCommand struct {
	fx.Out

	Logging   Logging
	FFmpeg    *FFmpeg    `env:",init"`
	Subtitles *Subtitles `env:",init"`
}

func ParseSubtitlesCommand() (SubtitlesCommand, error) {
	var cfg SubtitlesCommand

	if err := env.Parse(&cfg); err != nil {
		return SubtitlesCommand{}, fmt.Errorf("failed to parse subtitles config: %w", err)
	}

	return cfg, nil
}
