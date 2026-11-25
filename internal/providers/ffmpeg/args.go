package ffmpeg

import (
	"fmt"
	"path/filepath"
	"strconv"
	"time"
)

// Audio prepared for speech recognition: mono 16 kHz is what Whisper works with internally,
// and 32 kbit/s MP3 is ~14 MB per hour, so an hour fits into one 25 MB upload.
const (
	AudioExtension = ".mp3"

	audioCodec      = "libmp3lame"
	audioBitrate    = "32k"
	audioSampleRate = "16000"
	audioChannels   = "1"

	inputFlag       = "-i"
	audioCodecFlag  = "-c:a"
	audioFilterFlag = "-af"
	channelsFlag    = "-ac"
	noVideoFlag     = "-vn"
	sampleRateFlag  = "-ar"
	plainOutput     = "default=noprint_wrappers=1:nokey=1" // ffprobe prints bare values
	// Inputs come from users (Telegram files): only local files may be opened, never URLs,
	// concat: lists or other protocols that a crafted name or playlist could point to.
	fileProtocol      = "file"
	protocolWhitelist = "-protocol_whitelist"

	timePrecision = 3  // milliseconds are enough for ffmpeg time arguments
	float64Bits   = 64 // bit size for strconv
)

// SilenceOptions define a pause for silencedetect: quieter than NoiseDB for at least MinDuration.
type SilenceOptions struct {
	NoiseDB     float64
	MinDuration time.Duration
}

// localFile turns a path into an explicit local file URL: "-x.mp4", "http://h/x" or "concat:a|b"
// become "file:/abs/dir/-x.mp4", ... and are never read as an option or another protocol.
func localFile(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve path %q: %w", path, err)
	}

	return fileProtocol + ":" + abs, nil
}

// commonArgs make ffmpeg non-interactive, overwrite the output and keep stderr free of the banner and progress.
func commonArgs() []string {
	return []string{"-hide_banner", "-nostdin", "-nostats", "-y"}
}

// inputArgs take a local file URL from localFile.
func inputArgs(input string) []string {
	return []string{protocolWhitelist, fileProtocol, inputFlag, input}
}

func encodeArgs() []string {
	return []string{
		noVideoFlag,
		channelsFlag, audioChannels,
		sampleRateFlag, audioSampleRate,
		audioCodecFlag, audioCodec,
		"-b:a", audioBitrate,
	}
}

func extractAudioArgs(src, dst string) []string {
	args := append(commonArgs(), inputArgs(src)...)
	// "0:a:0" takes the first audio stream and fails with a clear message if there is none.
	args = append(args, mapFlag, "0:a:0", audioFilterFlag, measureFilter)
	args = append(args, encodeArgs()...)

	return append(args, dst)
}

func cutAudioArgs(src, dst string, start, duration float64) []string {
	args := commonArgs()
	// -ss before -i seeks the input; with re-encoding the cut is still sample-accurate.
	args = append(args, "-ss", formatDecimal(start), "-t", formatDecimal(duration))
	args = append(args, inputArgs(src)...)
	args = append(args, encodeArgs()...)

	return append(args, dst)
}

func durationArgs(input string) []string {
	return probeEntriesArgs(input, "format=duration", plainOutput)
}

// probeEntriesArgs make ffprobe print only the given entries of a local file in the given format.
func probeEntriesArgs(input, entries, format string) []string {
	args := []string{"-v", "error", "-show_entries", entries, "-of", format}

	return append(args, inputArgs(input)...)
}

func silenceDetectArgs(input string, opts SilenceOptions) []string {
	filter := "silencedetect=noise=" + formatDecimal(opts.NoiseDB) + "dB:d=" + formatDecimal(opts.MinDuration.Seconds())

	args := append(commonArgs(), inputArgs(input)...)

	return append(args, audioFilterFlag, filter, "-f", "null", "-")
}

// formatDecimal formats seconds or decibels with three decimals.
func formatDecimal(value float64) string {
	return strconv.FormatFloat(value, 'f', timePrecision, float64Bits)
}
