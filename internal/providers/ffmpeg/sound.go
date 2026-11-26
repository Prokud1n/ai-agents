package ffmpeg

import (
	"math"
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

	// rumbleFilter cuts the rumble below 80 Hz (wind, traffic, handling of the phone) before the noise reduction.
	rumbleFilter = "highpass=f=80"
	// noiseReduction: afftdn lowers the stationary noise under the floor by at most 10 dB (12 by default); a stronger
	// reduction (nr 20+) makes the voice sound «under water».
	noiseReduction = 10
	// afftdnDelay: afftdn puts its output 25 ms late (its FFT window, at 44.1 and 48 kHz alike, ffmpeg 6.1 and 8.1). The
	// sound is padded by it at the end and trimmed by it at the start, so it stays in place and as long.
	afftdnDelay = "0.025"
	// The noise floor of afftdn follows the loudness of the recording: speech is about 36 dB above its noise, so a quiet
	// recording (-30 LUFS) gets a floor of -66 dBFS instead of a fixed -50, which would eat into its speech.
	noiseFloorBelowSpeech = 36.0
	minNoiseFloor         = -80.0 // the range of afftdn's nf
	maxNoiseFloor         = -35.0
	// defaultNoiseFloor without a measurement: a recording at about -14 LUFS.
	defaultNoiseFloor = -50.0

	// The limiter after the gain: a lookahead of 5 ms (compensated: latency=1, the sound is not delayed), a release of
	// 50 ms, no automatic level (it would raise the sound again).
	limiterOptions    = ":attack=5:release=50:level=false:latency=1"
	amplitudeDecibels = 20.0 // an amplitude ratio of a level in dB is 10^(dB/20)

	// soundRate is the sample rate of the processed sound: loudnorm works at 192 kHz in its dynamic mode.
	soundRate  = "48000"
	soundLabel = "j"

	// Limits of a measured level: a silent sound measures -inf, which neither ffmpeg's options nor JSON take.
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

// LoudnessTarget is the loudness the joined sound of a render is brought to.
type LoudnessTarget struct {
	// Integrated loudness, LUFS; TruePeak, the most the peaks reach, dBTP; Range, the loudness range the one-pass
	// loudnorm leaves (LU).
	Integrated float64
	TruePeak   float64
	Range      float64
	// Measured is the loudness of the source (Runner.ExtractAudioMeasured): the sound then gets one gain, the target
	// minus the measured loudness, and a limiter keeps its peaks at TruePeak — the dynamics stay as recorded. (loudnorm's
	// linear mode would rarely do it: the peaks of phone speech are 13–20 dB above its loudness, so the gain would push
	// them over, and loudnorm compresses instead.) Nil: loudnorm in one pass, adjusting the gain as the sound goes (its
	// dynamic mode, a 3 s window), for a transcript stored before the measurement.
	Measured *Loudness
	// Correction (dB) is added to the gain by the measurement: the render measures the processed sound of the cut
	// before it (Runner.fitLoudness) and corrects the miss the limiter and the cut make.
	Correction float64
}

// Gain is the gain of a measured sound, dB, with the correction; 0 without a measurement.
func (l *LoudnessTarget) Gain() float64 {
	if l.Measured == nil {
		return 0
	}

	return l.Integrated - l.Measured.Integrated + l.Correction
}

// Denoise is the light noise reduction of speech.
type Denoise struct {
	// NoiseFloor of afftdn, dBFS (NoiseFloorFor).
	NoiseFloor float64
}

// NoiseFloorFor is the noise floor of a recording of the measured loudness: 36 dB under it, within -80..-35 dBFS;
// -50 without a measurement.
func NoiseFloorFor(measured *Loudness) float64 {
	if measured == nil {
		return defaultNoiseFloor
	}

	return min(max(math.Round(measured.Integrated-noiseFloorBelowSpeech), minNoiseFloor), maxNoiseFloor)
}

// soundChain is the processing of the joined sound: the noise reduction, then the loudness. Empty for a muted sound
// or without either. Only numbers come from the caller.
func soundChain(opts RenderOptions) string {
	if opts.Mute {
		return ""
	}

	filters := make([]string, 0, 2)
	if opts.Denoise != nil {
		filters = append(filters, denoiseFilter(opts.Denoise))
	}

	if opts.Loudness != nil {
		filters = append(filters, loudnessFilter(opts.Loudness), "aresample="+soundRate)
	}

	return strings.Join(filters, ",")
}

// denoiseFilter: the highpass and afftdn, with the delay of afftdn compensated.
func denoiseFilter(d *Denoise) string {
	return "apad=pad_dur=" + afftdnDelay + "," + rumbleFilter + ",afftdn=nr=" + strconv.Itoa(noiseReduction) +
		":nf=" + formatLevel(d.NoiseFloor) + ",atrim=start=" + afftdnDelay + ",asetpts=PTS-STARTPTS"
}

// loudnessFilter: "volume=13.6dB,alimiter=limit=0.794328:…" with a measurement, the one-pass loudnorm without.
func loudnessFilter(l *LoudnessTarget) string {
	if l.Measured != nil {
		limit := math.Pow(decimalBase, l.TruePeak/amplitudeDecibels)

		return "volume=" + formatLevel(l.Gain()) + "dB,alimiter=limit=" + formatPrecise(limit) + limiterOptions
	}

	return "loudnorm=I=" + formatLevel(l.Integrated) + ":TP=" + formatLevel(l.TruePeak) + ":LRA=" + formatLevel(l.Range)
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
