package ffmpeg

import (
	"regexp"
	"strconv"
)

// Silence is a pause in the audio, in seconds from the start.
type Silence struct {
	Start float64
	End   float64
}

var (
	silenceStartRe = regexp.MustCompile(`silence_start: (-?[0-9.]+)`)
	silenceEndRe   = regexp.MustCompile(`silence_end: (-?[0-9.]+)`)
)

// parseSilences reads silencedetect lines from ffmpeg stderr:
//
//	[silencedetect @ 0x...] silence_start: 1.234
//	[silencedetect @ 0x...] silence_end: 2.5 | silence_duration: 1.266
//
// A pause still open at the end of the file has no silence_end and is skipped: nothing is cut after it.
func parseSilences(stderr string) []Silence {
	starts := silenceStartRe.FindAllStringSubmatchIndex(stderr, -1)
	ends := silenceEndRe.FindAllStringSubmatchIndex(stderr, -1)

	silences := make([]Silence, 0, len(ends))
	endIdx := 0

	for _, start := range starts {
		// Pair each start with the first end that follows it in the log.
		for endIdx < len(ends) && ends[endIdx][0] < start[0] {
			endIdx++
		}

		if endIdx == len(ends) {
			break
		}

		startSec, err := strconv.ParseFloat(stderr[start[2]:start[3]], float64Bits)
		if err != nil {
			continue
		}

		endSec, err := strconv.ParseFloat(stderr[ends[endIdx][2]:ends[endIdx][3]], float64Bits)
		if err != nil {
			continue
		}

		endIdx++

		silences = append(silences, Silence{Start: max(startSec, 0), End: endSec})
	}

	return silences
}
