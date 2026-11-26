package roughcut

import (
	"math"
	"slices"
	"sort"

	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// Cut points by the sound (T3.19b). Whisper's word times are off by tens of milliseconds both ways, and the endings of
// Russian words are often «eaten» (their time ends before the sound does): margins of a fixed length around the words
// clip phrases or leave long silences. With the level of the source sound (RMS in 10 ms windows, ffmpeg.Levels) the
// speech is found around the words: a pause is cut where it is really silent, at its quietest frame, down to a short
// breath, and a phrase keeps its tail.
const (
	// speechReach: the sound of a word is looked for this far past Whisper's edge of it (a late start, an eaten end).
	speechReach = 0.3
	// wordInnerReach: Whisper also stretches a word into the silence around it (an end late by 0.3–0.35 s, a start
	// early by 0.3 s were measured on the trainer's videos): the sound is looked for this far inside the word, not past
	// its middle.
	wordInnerReach = 0.4
	// speechHole: a quieter stretch this short inside the sound (the closure of a stop: «т», «к», «п») does not end it.
	speechHole = 0.12
	// breathPause is the silence a pause is compressed to (the tail of one phrase and the lead of the next), so the
	// phrases do not run together: 0.15 s, ± half a frame of the grid the cut points stand on (0.13–0.17 s at 30 fps).
	breathPause = 0.15
	// The tail of a phrase is cut tailMin..tailMax after its sound ends (its decay and the room), at the quietest frame
	// of it, the latest of equals; the next phrase starts at least leadMin after its silence.
	tailMin = 0.06
	tailMax = 0.10
	leadMin = 0.04
	// edgeLead and edgeTail are kept before the first and after the last sound of the reel.
	edgeLead = 0.1
	edgeTail = 0.15
	// The threshold of speech: speechShareOfRange of the way from the noise floor (the 10th percentile of the windows)
	// to the loud speech (the 95th), at least minSpeechAboveFloor above the floor. Breaths and room noise stay under
	// it, the fricative endings («с», «ш») above. Without minLevelContrast between the two the sound tells nothing
	// (music, constant noise, a tone) and the margins around the words are used.
	floorPercentile     = 0.10
	loudPercentile      = 0.95
	speechShareOfRange  = 0.4
	minSpeechAboveFloor = 10.0
	minLevelContrast    = 15.0
	// Digital silence: windows at audibleLevel and under are not heard (the floor of 16-bit sound is about -96 dBFS);
	// the floor is taken from the audible windows when they are at least minAudibleShare of the sound and minFloorDepth
	// under the loud speech (room noise is 40–50 dB under it, the quiet speech 15–25), otherwise it is not under
	// digitalFloor.
	audibleLevel    = -100.0
	digitalFloor    = -90.0
	minAudibleShare = 0.2
	minFloorDepth   = 30.0
	// longPause is the pause counted in the decisions log: the references have about one above 0.4 s per reel.
	longPause = 0.3
	// silentLevel: a window of digital silence (-inf dBFS).
	silentLevel = -120.0
	gridEpsilon = 1e-6
	levelTenths = 10
)

// speechLevels is the sound level of the source on the transcript's timeline, with the threshold of speech.
type speechLevels struct {
	window    float64
	db        []float64
	threshold float64
}

// levelStats are the noise floor and the loud speech (the loudPercentile of the windows) of the sound, dBFS; ok false
// without levels. A gate or a phone can put digital silence (-inf) into the pauses; when it is more than
// floorPercentile of the windows it would be the floor, and the threshold would fall under the room noise of the other
// pauses (they would sound like speech and stay). So the floor is the floorPercentile of the audible windows (above
// audibleLevel) when they are at least minAudibleShare of all and their floor is at least minFloorDepth under the loud
// speech (not the quiet speech of a sound whose pauses are all digital silence); otherwise the floorPercentile of all
// the windows, not under digitalFloor.
func levelStats(l *ffmpeg.Levels) (floor, loud float64, ok bool) {
	if l == nil || l.Window <= 0 || len(l.DB) == 0 {
		return 0, 0, false
	}

	sorted := slices.Clone(l.DB)
	slices.Sort(sorted)
	loud = percentile(sorted, loudPercentile)

	audible := sorted[sort.SearchFloat64s(sorted, math.Nextafter(audibleLevel, math.Inf(1))):]
	if float64(len(audible)) >= float64(len(sorted))*minAudibleShare {
		if f := percentile(audible, floorPercentile); loud-f >= minFloorDepth {
			return f, loud, true
		}
	}

	return max(percentile(sorted, floorPercentile), digitalFloor), loud, true
}

// percentile is the p-th share of sorted (not empty) levels.
func percentile(sorted []float64, p float64) float64 {
	return sorted[int(float64(len(sorted)-1)*p)]
}

// speechThreshold is the level speech is above: speechShareOfRange of the way from the floor to the loud speech, at
// least minSpeechAboveFloor above the floor.
func speechThreshold(floor, loud float64) float64 {
	return floor + max(minSpeechAboveFloor, (loud-floor)*speechShareOfRange)
}

// soundLevelsOf are the levels of the sound for the decisions log; nil without levels.
func soundLevelsOf(l *ffmpeg.Levels) *SoundLevels {
	floor, loud, ok := levelStats(l)
	if !ok {
		return nil
	}

	return &SoundLevels{
		NoiseFloor: roundLevel(floor), Speech: roundLevel(loud), Threshold: roundLevel(speechThreshold(floor, loud)),
		Used: loud-floor >= minLevelContrast,
	}
}

// roundLevel keeps tenths of a dB.
func roundLevel(db float64) float64 {
	return math.Round(db*levelTenths) / levelTenths
}

// newSpeechLevels prepares the levels for the cut; nil without levels or when the sound has no contrast between
// speech and silence.
func newSpeechLevels(l *ffmpeg.Levels) *speechLevels {
	floor, loud, ok := levelStats(l)
	if !ok || loud-floor < minLevelContrast {
		return nil
	}

	return &speechLevels{window: l.Window, db: l.DB, threshold: speechThreshold(floor, loud)}
}

// at is the level of the window holding t; silence outside the sound.
func (s *speechLevels) at(t float64) float64 {
	i := int(math.Floor(t / s.window))
	if i < 0 || i >= len(s.db) {
		return silentLevel
	}

	return s.db[i]
}

// around is the louder of the windows on both sides of t: the level a cut at t goes through.
func (s *speechLevels) around(t float64) float64 {
	return max(s.at(t-s.window/2), s.at(t+s.window/2))
}

// speechEnd is where the sound of a word ending at from (by Whisper) really ends: the end of the last window above the
// threshold that follows it without a hole longer than speechHole, at most limit. Whisper also stretches words into
// the silence after them: when nothing is heard right at or after from, the end is the last window above the threshold
// inside the word, not before inner (from when the word must not be shortened); from if the word is silent there too.
func (s *speechLevels) speechEnd(from, inner, limit float64) float64 {
	return s.heardEdge(from, inner, limit, forward)
}

// speechStart is where the sound of a word starting at to (by Whisper) really starts: the start of the first window
// above the threshold before it without a hole longer than speechHole, at least limit. When nothing is heard right at
// or before to (Whisper started the word early, in the silence), the start is the first window above the threshold
// inside the word, not after inner (to when the word must not be shortened); to if the word is silent there too.
func (s *speechLevels) speechStart(to, inner, limit float64) float64 {
	return s.heardEdge(to, inner, limit, backward)
}

// Directions of heardEdge: out of a word past its end, or back past its start.
const (
	forward  = 1.0
	backward = -1.0
)

// heardEdge is speechEnd (d forward) or speechStart (d backward) of a word edge: times are mirrored by d, so «later»
// is d*t > d*u for both.
func (s *speechLevels) heardEdge(edge, inner, limit, d float64) float64 {
	first := d * math.Floor(d*edge/s.window) * s.window // the grid point at or before the edge, looking out
	last := edge

scan:
	for t := first; d*t < d*limit; t += d * s.window {
		switch {
		case s.at(t+d*s.window/2) > s.threshold:
			last = d * max(d*last, min(d*(t+d*s.window), d*limit))
		case d*(t-last) >= speechHole:
			break scan
		}
	}

	if d*last > d*edge || d*inner >= d*edge || s.at(edge-d*s.window/2) > s.threshold {
		return last
	}

	// Nothing heard at the edge or past it: the last sound inside the word.
	for t := first - d*s.window; d*(t+d*s.window) >= d*inner-gridEpsilon; t -= d * s.window {
		if s.at(t+d*s.window/2) > s.threshold {
			return d * max(min(d*(t+d*s.window), d*edge), d*inner)
		}
	}

	return edge
}

// innerEnd is how far inside a word its end may be moved by the sound: not before its middle and not more than
// wordInnerReach before Whisper's end.
func innerEnd(w transcription.Word) float64 {
	return max((w.Start+w.End)/2, w.End-wordInnerReach)
}

// innerStart is how far inside a word its start may be moved by the sound: not after its middle and not more than
// wordInnerReach after Whisper's start.
func innerStart(w transcription.Word) float64 {
	return min((w.Start+w.End)/2, w.Start+wordInnerReach)
}

// cutGrid places cut points on the frame grid of the render (k/fps), so the frame snapping does not move them.
type cutGrid struct {
	levels *speechLevels
	fps    float64
}

func (g *cutGrid) floor(t float64) float64 {
	if g.fps <= 0 {
		return t
	}

	return math.Floor(t*g.fps+gridEpsilon) / g.fps
}

func (g *cutGrid) ceil(t float64) float64 {
	if g.fps <= 0 {
		return t
	}

	return math.Ceil(t*g.fps-gridEpsilon) / g.fps
}

func (g *cutGrid) round(t float64) float64 {
	if g.fps <= 0 {
		return t
	}

	return math.Round(t*g.fps) / g.fps
}

// quietest is the point of the grid within [from, to] where the sound is the quietest: the earliest of equals, the
// latest with latest set; the grid point nearest to the middle when there is none in it.
func (g *cutGrid) quietest(from, to float64, latest bool) float64 {
	step := 1 / g.fps
	if g.fps <= 0 {
		step = g.levels.window
	}

	best, level := g.ceil(from), math.Inf(1)

	for t := g.ceil(from); t <= to+gridEpsilon; t += step {
		l := g.levels.around(t)
		if l < level || (latest && l == level) {
			best, level = t, l
		}
	}

	if math.IsInf(level, 1) {
		return g.round((from + to) / 2)
	}

	return best
}

// pause is the cut of the silence between a kept word prev and the next kept one w, with nothing removed between
// them: none (ok false) if the silence actually heard is not longer than maxPause. Whisper's edges may lie in the
// silence (a stretched end or an early start): the silence is measured from the sound, inside the words too.
// The tail of the first phrase ends at the quietest frame tailMin..tailMax after its sound, the next phrase starts so
// that breathPause of silence is left in all (at least leadMin before its sound).
func (g *cutGrid) pause(prev, w transcription.Word, maxPause float64) (end, next float64, ok bool) {
	prevEnd, nextStart := prev.End, w.Start
	middle := (prevEnd + nextStart) / 2
	heardEnd := g.levels.speechEnd(prevEnd, innerEnd(prev), min(prevEnd+speechReach, middle))
	heardStart := g.levels.speechStart(nextStart, innerStart(w), max(nextStart-speechReach, middle))

	if heardStart-heardEnd <= maxPause {
		return 0, 0, false
	}

	end = g.quietest(heardEnd+tailMin, heardEnd+tailMax, true)
	next = min(g.round(heardStart-(breathPause-(end-heardEnd))), g.floor(heardStart-leadMin))

	return end, next, next > end
}

// around is the cut between a kept word ending at prevEnd and the next kept one starting at nextStart when the
// words removed between them span [removedStart, removedEnd]: the tail as at a pause, the start of the next word at
// its quietest frame between the margin and leadMin before its sound, never into the removed words. Too little
// silence left is made up to breathPause: the next word starts earlier (not before removedEnd), then the tail ends
// later (not after removedStart).
func (g *cutGrid) around(prevEnd, nextStart, removedStart, removedEnd float64, s Settings) (end, next float64) {
	latestEnd, earliestNext := max(g.floor(removedStart), prevEnd), min(g.ceil(removedEnd), nextStart)
	heardEnd := g.levels.speechEnd(prevEnd, prevEnd, min(prevEnd+s.PadAfter, removedStart))
	heardStart := g.levels.speechStart(nextStart, nextStart, max(nextStart-s.PadBefore, removedEnd))

	end = g.quietest(min(heardEnd+tailMin, latestEnd), min(heardEnd+tailMax, latestEnd), true)
	next = g.quietest(max(heardStart-s.PadBefore, earliestNext), max(heardStart-leadMin, earliestNext), false)
	end, next = max(end, prevEnd), min(next, nextStart)

	if short := breathPause - (end - heardEnd) - (heardStart - next); short > 0 {
		next = max(g.floor(next-short), earliestNext)
	}

	if short := breathPause - (end - heardEnd) - (heardStart - next); short > 0 {
		end = min(g.ceil(end+short), latestEnd)
	}

	return end, next
}

// lead is the start of the reel before a word starting at start, not before from (a removed word or the source start).
func (g *cutGrid) lead(start, from float64) float64 {
	heard := g.levels.speechStart(start, start, max(start-speechReach, from))

	return max(g.floor(heard-edgeLead), from, 0)
}

// tail is the end of the reel after a word ending at end, not after to (a removed word or the source end).
func (g *cutGrid) tail(end, to float64) float64 {
	heard := g.levels.speechEnd(end, end, min(end+speechReach, to))

	return max(min(g.ceil(heard+edgeTail), to), end)
}

// longPauses counts the silences longer than longPause inside the reel (not at its very start and end): the
// windows of the kept intervals below the threshold, one after another across the joints.
func (s *speechLevels) longPauses(keep []Interval) int {
	count, silence, heard := 0, 0.0, false

	for _, iv := range keep {
		for t := iv.Start; t < iv.End; t += s.window {
			if s.at(t) > s.threshold {
				if heard && silence > longPause {
					count++
				}

				silence, heard = 0, true

				continue
			}

			silence += min(s.window, iv.End-t)
		}
	}

	return count
}
