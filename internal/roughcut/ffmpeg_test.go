package roughcut

import (
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/transcription"
)

func testFFmpegAvailable() bool {
	_, ffmpegErr := exec.LookPath("ffmpeg")
	_, ffprobeErr := exec.LookPath("ffprobe")

	return ffmpegErr == nil && ffprobeErr == nil
}

// generateSpeechVideo writes a vertical test clip as long as the transcript, where a 440 Hz tone sounds
// exactly during every word and the rest is silence.
func generateSpeechVideo(t *testing.T, path string, src *transcription.Result) {
	t.Helper()

	format := func(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

	spans := make([]string, 0, len(src.Words))
	for _, w := range src.Words {
		spans = append(spans, "between(t"+lavfiComma+format(w.Start)+lavfiComma+format(w.End)+")")
	}

	duration := format(src.Duration)
	audio := "aevalsrc=if(" + strings.Join(spans, "+") + "\\,0.5*sin(2*PI*440*t)\\,0):s=48000:d=" + duration
	video := "testsrc=duration=" + duration + ":size=360x640:rate=30"

	cmd := exec.CommandContext(t.Context(), "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", audio, "-f", "lavfi", "-i", video,
		"-c:v", "mpeg4", "-c:a", "aac", "-shortest", path)

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

// lavfiComma separates function arguments inside a filter option.
const lavfiComma = `\,`

// syncOffset delays the audio of the sync clip, so its start_time differs from the video's.
const (
	syncOffset        = "0.1"
	syncOffsetSeconds = 0.1
	syncFPS           = 30
)

// generateSyncVideo writes a variable frame rate clip (every 5th frame of 30 fps dropped, the audio
// delayed by syncOffset) where each word of spans sounds as a tone and shows as a bright picture,
// at the same moments. Times of spans are seconds of the generated audio.
func generateSyncVideo(t *testing.T, path string, spans [][2]float64, duration float64) {
	t.Helper()

	format := func(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

	audioExpr := make([]string, 0, len(spans))
	videoExpr := make([]string, 0, len(spans))

	for _, s := range spans {
		audioExpr = append(audioExpr, "between(t"+lavfiComma+format(s[0])+lavfiComma+format(s[1])+")")
		// Frame numbers, not times: a start like 29/30 s written with 3 decimals (0.967) would light the next frame
		// and put the picture of the word a frame after its sound.
		first := int(math.Round((s[0] + syncOffsetSeconds) * syncFPS))
		last := int(math.Round((s[1]+syncOffsetSeconds)*syncFPS)) - 1
		videoExpr = append(videoExpr, "between(N"+lavfiComma+strconv.Itoa(first)+lavfiComma+strconv.Itoa(last)+")")
	}

	d := format(duration)
	video := "nullsrc=s=180x320:r=" + strconv.Itoa(syncFPS) + ":d=" + d + ",geq=lum=if(" + strings.Join(videoExpr, "+") +
		"\\,235\\,16):cb=128:cr=128,select=not(eq(mod(n\\,5)\\,2))"
	audio := "aevalsrc=if(" + strings.Join(audioExpr, "+") + "\\,0.5*sin(2*PI*440*t)\\,0):s=48000:d=" + d

	cmd := exec.CommandContext(t.Context(), "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", video, "-itsoffset", syncOffset, "-f", "lavfi", "-i", audio,
		"-map", "0:v", "-map", "1:a", "-c:v", "libx264", "-c:a", "aac", "-fps_mode", "vfr", path)

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

var (
	silenceEndRe = regexp.MustCompile(`silence_end: ([0-9.]+)`)
	blackEndRe   = regexp.MustCompile(`black_end:([0-9.]+)`)
)

// onsets returns when tones start (end of silences) and when the picture turns bright (end of black)
// in a rendered clip.
func onsets(t *testing.T, path string) (audio, video []float64) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "ffmpeg", "-hide_banner", "-nostdin", "-i", path,
		"-vf", "blackdetect=d=0.02:pix_th=0.5", "-af", "silencedetect=noise=-30dB:d=0.02", "-f", "null", "-")

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	parse := func(re *regexp.Regexp) []float64 {
		var values []float64

		for _, m := range re.FindAllStringSubmatch(string(out), -1) {
			v, err := strconv.ParseFloat(m[1], 64)
			require.NoError(t, err)

			values = append(values, v)
		}

		return values
	}

	return parse(silenceEndRe), parse(blackEndRe)
}

// remuxWithOffset copies a clip with all its timestamps moved by offset seconds (format start_time).
func remuxWithOffset(t *testing.T, src, dst, offset string) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-i", src, "-c", "copy", "-output_ts_offset", offset, dst)

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}
