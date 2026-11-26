package roughcut

import (
	"math"
	"slices"

	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
)

// Cut points by the sound (T3.19b). Whisper's word times are off by tens of milliseconds both ways, and the endings of
// Russian words are often «eaten» (their time ends before the sound does): margins of a fixed length around the words
// clip phrases or leave long silences. With the level of the source sound (RMS in 10 ms windows, ffmpeg.Levels) the
// speech is found around the words: a pause is cut where it is really silent, at its quietest frame, down to a short
// breath, and a phrase keeps its tail.
const (
	// speechReach: the sound of a word is looked for this far past Whisper's edge of it (a late start, an eaten end).
	speechReach = 0.3
	// speechHole: a quieter stretch this short inside the sound (the closure of a stop: «т», «к», «п») does not end it.
	speechHole = 0.12
	// breathPause is the silence a pause is compressed to (the tail of one phrase and the lead of the next), before the
	// frame grid rounds the cut points outwards (+ up to two frames): 0.12–0.19 s, so the phrases do not run together.
	breathPause = 0.12
	// silenceGuard: at least this much silence is kept after the sound and before it, so the fades (10 ms) never touch it.
	silenceGuard = 0.03
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
	// longPause is the pause counted in the decisions log: the references have about one above 0.4 s per reel.
	longPause = 0.3
	// silentLevel: a window of digital silence (-inf dBFS).
	silentLevel = -120.0
	gridEpsilon = 1e-6
)

// speechLevels is the sound level of the source on the transcript's timeline, with the threshold of speech.
type speechLevels struct {
	window    float64
	db        []float64
	threshold float64
}

// newSpeechLevels prepares the levels for the cut; nil without levels or when the sound has no contrast between
// speech and silence.
func newSpeechLevels(l *ffmpeg.Levels) *speechLevels {
	if l == nil || l.Window <= 0 || len(l.DB) == 0 {
		return nil
	}

	sorted := slices.Clone(l.DB)
	slices.Sort(sorted)

	floor := sorted[int(float64(len(sorted)-1)*floorPercentile)]
	loud := sorted[int(float64(len(sorted)-1)*loudPercentile)]

	if loud-floor < minLevelContrast {
		return nil
	}

	return &speechLevels{
		window: l.Window, db: l.DB, threshold: floor + max(minSpeechAboveFloor, (loud-floor)*speechShareOfRange),
	}
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
// threshold that follows it without a hole longer than speechHole, at most limit.
func (s *speechLevels) speechEnd(from, limit float64) float64 {
	last := from

	for t := math.Floor(from/s.window) * s.window; t < limit; t += s.window {
		switch {
		case s.at(t+s.window/2) > s.threshold:
			last = max(last, min(t+s.window, limit))
		case t-last >= speechHole:
			return last
		}
	}

	return last
}

// speechStart is where the sound of a word starting at to (by Whisper) really starts: the start of the first window
// above the threshold before it without a hole longer than speechHole, at least limit.
func (s *speechLevels) speechStart(to, limit float64) float64 {
	first := to

	for t := math.Ceil(to/s.window) * s.window; t > limit; t -= s.window {
		switch {
		case s.at(t-s.window/2) > s.threshold:
			first = min(first, max(t-s.window, limit))
		case first-t >= speechHole:
			return first
		}
	}

	return first
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

// quietest is the point of the grid within [from, to] where the sound is the quietest (the earliest of equals); the
// grid point nearest to the middle when there is none in it.
func (g *cutGrid) quietest(from, to float64) float64 {
	step := 1 / g.fps
	if g.fps <= 0 {
		step = g.levels.window
	}

	best, level := g.ceil(from), math.Inf(1)

	for t := g.ceil(from); t <= to+gridEpsilon; t += step {
		if l := g.levels.around(t); l < level {
			best, level = t, l
		}
	}

	if math.IsInf(level, 1) {
		return g.floor((from+to)/2 + step/2)
	}

	return best
}

// pause is the cut of the silence between a kept word ending at prevEnd and the next kept one starting at nextStart,
// with nothing removed between them: none (ok false) if the silence actually heard is not longer than maxPause.
// The tail of the first phrase ends at the quietest point of the first breathPause of the silence, the next phrase
// starts so that breathPause of silence is left in all.
func (g *cutGrid) pause(prevEnd, nextStart, maxPause float64) (end, next float64, ok bool) {
	middle := (prevEnd + nextStart) / 2
	heardEnd := g.levels.speechEnd(prevEnd, min(prevEnd+speechReach, middle))
	heardStart := g.levels.speechStart(nextStart, max(nextStart-speechReach, middle))

	if heardStart-heardEnd <= maxPause {
		return 0, 0, false
	}

	end = g.quietest(heardEnd+silenceGuard, heardEnd+breathPause-silenceGuard)
	next = g.floor(min(heardStart-silenceGuard, heardStart-(breathPause-(end-heardEnd))))

	return end, next, next > end
}

// around is the cut between a kept word ending at prevEnd and the next kept one starting at nextStart when the
// words removed between them span [removedStart, removedEnd]: each side is cut at its quietest point between the
// sound of its word (with silenceGuard) and the margin, never into the removed words.
func (g *cutGrid) around(prevEnd, nextStart, removedStart, removedEnd float64, s Settings) (end, next float64) {
	heardEnd := g.levels.speechEnd(prevEnd, min(prevEnd+s.PadAfter, removedStart))
	heardStart := g.levels.speechStart(nextStart, max(nextStart-s.PadBefore, removedEnd))

	end = g.quietest(min(heardEnd+silenceGuard, removedStart), min(heardEnd+s.PadAfter, removedStart))
	next = g.quietest(max(heardStart-s.PadBefore, removedEnd), max(heardStart-silenceGuard, removedEnd))

	return max(end, prevEnd), min(next, nextStart)
}

// lead is the start of the reel before a word starting at start, not before from (a removed word or the source start).
func (g *cutGrid) lead(start, from float64) float64 {
	heard := g.levels.speechStart(start, max(start-speechReach, from))

	return max(g.floor(heard-edgeLead), from, 0)
}

// tail is the end of the reel after a word ending at end, not after to (a removed word or the source end).
func (g *cutGrid) tail(end, to float64) float64 {
	heard := g.levels.speechEnd(end, min(end+speechReach, to))

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
