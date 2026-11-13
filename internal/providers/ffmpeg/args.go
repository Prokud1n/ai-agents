package ffmpeg

import (
	"strconv"
)

// Audio prepared for speech recognition: mono 16 kHz is what Whisper works with internally,
// and 32 kbit/s MP3 is ~14 MB per hour, so an hour fits into one 25 MB upload.
const (
	AudioExtension = ".mp3"

	audioCodec      = "libmp3lame"
	audioBitrate    = "32k"
	audioSampleRate = "16000"
	audioChannels   = "1"

	// silencedetect: a pause is at least silenceMinDuration below silenceNoiseLevel.
	silenceNoiseLevel  = "-30dB"
	silenceMinDuration = "0.5"

	timePrecision = 3  // milliseconds are enough for ffmpeg time arguments
	float64Bits   = 64 // bit size for strconv
)

// commonArgs make ffmpeg non-interactive, overwrite the output and keep stderr free of the banner and progress.
func commonArgs() []string {
	return []string{"-hide_banner", "-nostdin", "-nostats", "-y"}
}

func encodeArgs() []string {
	return []string{
		"-vn",
		"-ac", audioChannels,
		"-ar", audioSampleRate,
		"-c:a", audioCodec,
		"-b:a", audioBitrate,
	}
}

func inputArgs(path string) []string {
	return []string{"-i", path}
}

func extractAudioArgs(src, dst string) []string {
	args := append(commonArgs(), inputArgs(src)...)
	// "0:a:0" takes the first audio stream and fails with a clear message if there is none.
	args = append(args, "-map", "0:a:0")
	args = append(args, encodeArgs()...)

	return append(args, dst)
}

func cutAudioArgs(src, dst string, start, duration float64) []string {
	args := commonArgs()
	// -ss before -i seeks the input; with re-encoding the cut is still sample-accurate.
	args = append(args, "-ss", formatSeconds(start), "-t", formatSeconds(duration))
	args = append(args, inputArgs(src)...)
	args = append(args, encodeArgs()...)

	return append(args, dst)
}

func durationArgs(path string) []string {
	return []string{
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	}
}

func silenceDetectArgs(path string) []string {
	args := append(commonArgs(), inputArgs(path)...)

	return append(args,
		"-af", "silencedetect=noise="+silenceNoiseLevel+":d="+silenceMinDuration,
		"-f", "null", "-",
	)
}

func formatSeconds(seconds float64) string {
	return strconv.FormatFloat(seconds, 'f', timePrecision, float64Bits)
}
