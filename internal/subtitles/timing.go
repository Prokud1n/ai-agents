package subtitles

import "math"

// Display timing, in seconds. A subtitle appears leadIn before its first word and stays hold after its last one,
// so it is on screen within ±0.1 s of the speech; it is never shorter than minCueDuration if the next one leaves
// room, and a gap shorter than flickerGap between two subtitles is closed so the text does not blink.
const (
	leadIn         = 0.05
	hold           = 0.1
	minCueDuration = 0.8
	flickerGap     = 0.3
	// minShown keeps a subtitle squeezed by overlapping word times visible for at least one ASS time unit.
	minShown = 0.01

	centisecondsPerSecond = 100
)

// cueTiming are the limits of timeCuesWith: the shortest and the longest subtitle and the gap to the next one that is
// closed (the subtitle stays until the next starts).
type cueTiming struct {
	minDuration, maxDuration, closeGap float64
}

// defaultTiming is the timing of the pieces of before (groupCues).
var defaultTiming = cueTiming{minDuration: minCueDuration, maxDuration: maxCueDuration, closeGap: flickerGap}

// phraseTiming is the timing of the phrases of the style: a phrase stays at least min_duration and until the next one
// across a pause up to hold_gap.
func phraseTiming(p *PhraseLayout) cueTiming {
	return cueTiming{minDuration: p.MinDuration, maxDuration: p.MaxDuration, closeGap: p.HoldGap}
}

// timeCues sets display times with defaultTiming.
func timeCues(cues []cue) []cue {
	return timeCuesWith(cues, defaultTiming)
}

// timeCuesWith sets display times: lead-in and hold around the words, minimum duration, no overlaps (a subtitle ends
// where the next begins at the latest), no blinking gaps. Times are rounded to ASS centiseconds.
func timeCuesWith(cues []cue, timing cueTiming) []cue {
	starts := make([]float64, len(cues))

	for i, c := range cues {
		starts[i] = roundCS(max(c.Start-leadIn, 0))
		if i > 0 {
			starts[i] = roundCS(max(starts[i], starts[i-1]+minShown))
		}
	}

	timed := make([]cue, 0, len(cues))

	for i, c := range cues {
		start := starts[i]
		end := max(c.End+hold, start+timing.minDuration)

		// The last word may be timed by Whisper over a long pause: do not keep it longer than a whole subtitle.
		end = min(end, start+timing.maxDuration+hold)

		if i+1 < len(cues) {
			next := starts[i+1]
			if next-end < timing.closeGap {
				end = next
			}

			end = min(end, next)
		}

		end = roundCS(max(end, start+minShown))
		timed = append(timed, cue{Start: start, End: end, Lines: c.Lines})
	}

	return timed
}

func roundCS(seconds float64) float64 {
	return math.Round(seconds*centisecondsPerSecond) / centisecondsPerSecond
}
