package ffmpeg

import (
	"regexp"
	"strconv"
	"strings"
)

// The sound of a draft: a light noise reduction of speech, then the loudness of social networks.
const (
	// measureFilter measures the loudness of the sound it passes on unchanged (EBU R128 with the true peak); the summary
	// is logged at the info level when the graph closes, the per-frame lines only at the verbose one.
	measureFilter = "ebur128=peak=true:framelog=verbose"
	// measureLogTail of stderr is kept to read the summary: it is the last thing ffmpeg logs, about 400 bytes.
	measureLogTail = 16 << 10
	summaryMarker  = "Summary:"

	// denoiseFilter is a light noise reduction of speech: the rumble below 80 Hz (wind, traffic, handling of the
	// phone) is cut, then afftdn lowers the stationary noise under a floor of -50 dBFS by at most 10 dB (12 by
	// default). A stronger reduction (nr 20+, a floor of -30) makes the voice sound «under water».
	denoiseFilter = "highpass=f=80,afftdn=nr=10:nf=-50"
	// soundRate is the sample rate after loudnorm, which works at 192 kHz in its dynamic mode.
	soundRate  = "48000"
	soundLabel = "j"

	// Limits of loudnorm's measured_* options: a silent sound measures -inf, which neither loudnorm nor JSON takes.
	minLevel = -99.0
	maxRange = 99.0
)

var (
	integratedRe = regexp.MustCompile(`I:\s+(-?[0-9.]+|-inf) LUFS\s+Threshold:\s+(-?[0-9.]+|-inf) LUFS`)
	rangeRe      = regexp.MustCompile(`LRA:\s+([0-9.]+) LU\b`)
	truePeakRe   = regexp.MustCompile(`Peak:\s+(-?[0-9.]+|-inf) dBFS`)
)

// Loudness is an EBU R128 measurement of a sound (the ebur128 filter).
type Loudness struct {
	// Integrated loudness, LUFS, and Threshold, the relative gate it was measured above, LUFS.
	Integrated float64 `json:"integrated"`
	Threshold  float64 `json:"threshold"`
	// Range is the loudness range, LU.
	Range float64 `json:"range"`
	// TruePeak, dBTP.
	TruePeak float64 `json:"true_peak"`
}

// Loudnorm is the loudness the joined sound of a render is brought to (the loudnorm filter).
type Loudnorm struct {
	// Integrated loudness, LUFS; TruePeak, the most the peaks reach, dBTP; Range, the loudness range left to a sound
	// that has to be compressed, LU.
	Integrated float64
	TruePeak   float64
	Range      float64
	// Measured is the loudness of the source (Runner.ExtractAudioMeasured): loudnorm then changes the volume by one
	// gain (its linear mode), the dynamics stay as recorded. When the gain would push the peaks over TruePeak, or the
	// range is wider than Range, loudnorm compresses as without a measurement. Nil: one pass with no measurement,
	// loudnorm adjusts the gain as the sound goes (its dynamic mode, a 3 s window).
	Measured *Loudness
}

// soundChain is the processing of the joined sound: the noise reduction, then the loudness. Empty for a muted sound
// or without either. Only numbers come from the caller.
func soundChain(opts RenderOptions) string {
	if opts.Mute {
		return ""
	}

	filters := make([]string, 0, 2)
	if opts.Denoise {
		filters = append(filters, denoiseFilter)
	}

	if opts.Loudness != nil {
		filters = append(filters, loudnormFilter(opts.Loudness), "aresample="+soundRate)
	}

	return strings.Join(filters, ",")
}

// loudnormFilter: "loudnorm=I=-14.0:TP=-1.5:LRA=11.0" and the measurement for the linear mode.
func loudnormFilter(l *Loudnorm) string {
	f := "loudnorm=I=" + formatLevel(l.Integrated) + ":TP=" + formatLevel(l.TruePeak) + ":LRA=" + formatLevel(l.Range)

	if m := l.Measured; m != nil {
		f += ":measured_I=" + formatLevel(m.Integrated) + ":measured_TP=" + formatLevel(m.TruePeak) +
			":measured_LRA=" + formatLevel(m.Range) + ":measured_thresh=" + formatLevel(m.Threshold) + ":linear=true"
	}

	return f
}

func formatLevel(v float64) string {
	return strconv.FormatFloat(v, 'f', 1, float64Bits)
}

// parseLoudness reads the last summary of ebur128 in the log:
//
//	[Parsed_ebur128_0 @ 0x…] Summary:
//	  Integrated loudness:
//	    I:         -23.0 LUFS
//	    Threshold: -33.0 LUFS
//	  Loudness range:
//	    LRA:         5.1 LU
//	    …
//	  True peak:
//	    Peak:       -2.9 dBFS
//
// Nil if there is none. -inf (silence) is minLevel.
func parseLoudness(log string) *Loudness {
	at := strings.LastIndex(log, summaryMarker)
	if at < 0 {
		return nil
	}

	summary := log[at:]

	integrated := integratedRe.FindStringSubmatch(summary)
	lra := rangeRe.FindStringSubmatch(summary)
	peak := truePeakRe.FindStringSubmatch(summary)

	if integrated == nil || lra == nil || peak == nil {
		return nil
	}

	return &Loudness{
		Integrated: parseLevel(integrated[1], 0),
		Threshold:  parseLevel(integrated[2], 0),
		Range:      min(parseLevel(lra[1], maxRange), maxRange),
		TruePeak:   parseLevel(peak[1], maxRange),
	}
}

// parseLevel reads a level clamped to [minLevel, highest]; "-inf" is minLevel.
func parseLevel(s string, highest float64) float64 {
	v, err := strconv.ParseFloat(s, float64Bits)
	if err != nil {
		return minLevel
	}

	return min(max(v, minLevel), highest)
}
