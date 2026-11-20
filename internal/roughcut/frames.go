package roughcut

import "math"

const usPerSecond = 1_000_000

// SnapToFrames moves the edges of kept intervals and cuts to the nearest frame of a fps video, so every
// rendered fragment holds whole frames and its picture lasts exactly as long as its sound: otherwise each
// joint would shift the picture against the sound and the transcript by up to a frame. Intervals that
// become empty are dropped, touching ones merged; nothing goes past duration. fps <= 0 changes nothing.
func SnapToFrames(p *Plan, fps float64) Plan {
	out := *p
	out.Keep = append([]Interval(nil), p.Keep...)
	out.Cuts = append([]Cut(nil), p.Cuts...)
	out.Candidates = append([]Candidate(nil), p.Candidates...)
	out.FrameRate = 0 // never trusted from an edited plan: only the probed rate is written

	if fps <= 0 {
		return out
	}

	out.FrameRate = math.Round(fps*msPerSecond) / msPerSecond

	// The last whole frame of the source: a fragment never ends inside a frame.
	lastFrame := math.Floor(p.SourceDuration*fps) / fps

	snap := func(t float64) float64 {
		return roundMicro(min(math.Round(t*fps), math.Floor(p.SourceDuration*fps)) / fps)
	}

	keep := make([]Interval, 0, len(p.Keep))

	for _, iv := range p.Keep {
		s := Interval{Start: snap(iv.Start), End: snap(iv.End)}
		if s.End <= s.Start {
			continue
		}

		if n := len(keep); n > 0 && s.Start <= keep[n-1].End {
			keep[n-1].End = max(keep[n-1].End, s.End)

			continue
		}

		keep = append(keep, s)
	}

	cuts := make([]Cut, 0, len(p.Cuts))

	for _, c := range p.Cuts {
		s := c
		s.Start, s.End = snap(c.Start), snap(c.End)

		if s.End > s.Start {
			cuts = append(cuts, s)
		}
	}

	if n := len(cuts); n > 0 && cuts[n-1].End >= roundMicro(lastFrame) {
		cuts[n-1].End = p.SourceDuration // the cut at the end still runs to the real end
	}

	candidates := make([]Candidate, 0, len(p.Candidates))

	for i := range p.Candidates {
		snapped := p.Candidates[i]
		snapped.Cut = Interval{Start: snap(snapped.Cut.Start), End: snap(snapped.Cut.End)}
		candidates = append(candidates, snapped)
	}

	out.Keep, out.Cuts, out.Candidates = keep, cuts, candidates
	out.Duration = totalDuration(keep)

	return out
}

func roundMicro(t float64) float64 {
	return math.Round(t*usPerSecond) / usPerSecond
}
