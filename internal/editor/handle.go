package editor

import (
	"log/slog"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/config"
)

// trainerStyleFiles are the file names of the trainer's subtitle style (the default, also when SUBTITLES_STYLE_FILE is
// empty): it draws neither the Instagram handle nor the end card.
var trainerStyleFiles = []string{"style.yaml", "subtitles-style.yaml"}

// WarnHiddenHandle warns at the start of the app that REELS_HANDLE is set but the subtitle style of the media worker
// (SUBTITLES_STYLE_FILE: empty or the trainer's file) draws no handle: the owner sees why the drafts have none. A style
// file of another name is not checked (it is in the media image, not in the app's).
func WarnHiddenHandle(cfg *config.Editor, log *slog.Logger) {
	if strings.TrimSpace(cfg.ReelsHandle) == "" {
		return
	}

	style := strings.TrimSpace(cfg.SubtitlesStyleFile)
	if style != "" && !slices.Contains(trainerStyleFiles, filepath.Base(style)) {
		return
	}

	log.Warn("editor: REELS_HANDLE is set, but the trainer's subtitle style draws no handle and no end card; "+
		"set SUBTITLES_STYLE_FILE=/app/subtitles-style-words.yaml for them", "subtitles_style_file", style)
}
