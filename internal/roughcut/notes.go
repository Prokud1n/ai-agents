package roughcut

import (
	"cmp"
	"slices"
	"sort"
)

// How far a screen note that starts in a cut piece moves on to the next kept piece (seconds of the source). The cut
// points stand by the sound, not by Whisper's word edges: a note set on the first word of a phrase may start up to
// about 0.3 s before the kept piece (Whisper starts a word early in the silence before it); a note on a removed word
// (a filler, a cut take) further away is dropped as before. A section heading stands where
// the trainer reads it out («страх номер два», «пункт первый»), and that reading is cut from the sound: the plate
// replaces it, so the section moves to where the speech goes on after the reading and its pause.
const (
	noteShiftReach    = 0.3
	sectionShiftReach = 3.0
)

// RemapScreenNotes puts the screen notes of an edit onto the timeline of the cut by keep: ordered by time; a note that
// starts in a cut piece moves to the start of the next kept piece when it is at most noteShiftReach later
// (sectionShiftReach for a section heading), otherwise it is dropped (what it named is not in the reel); an end inside
// a cut moves to where the reel goes on, an end not after the start is dropped (0: the subtitles choose). Nil when
// none is left.
func RemapScreenNotes(notes []ScreenNote, keep []Interval) []ScreenNote {
	if len(notes) == 0 || len(keep) == 0 {
		return nil
	}

	sorted := slices.Clone(notes)
	slices.SortStableFunc(sorted, func(a, b ScreenNote) int { return cmp.Compare(a.Start, b.Start) })

	out := make([]ScreenNote, 0, len(sorted))

	for _, n := range sorted {
		if !noteShown(n, keep) {
			continue
		}

		moved := n
		moved.Start = roundTime(ReelTime(keep, n.Start))
		moved.End = 0

		if n.End > n.Start {
			if end := roundTime(ReelTime(keep, n.End)); end > moved.Start {
				moved.End = end
			}
		}

		out = append(out, moved)
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

// noteShown: the note starts in a kept piece, or in a cut one with the next kept piece within its reach.
func noteShown(n ScreenNote, keep []Interval) bool {
	if _, ok := findInterval(keep, n.Start); ok {
		return true
	}

	reach := noteShiftReach
	if n.Kind == SectionNote {
		reach = sectionShiftReach
	}

	i := sort.Search(len(keep), func(i int) bool { return keep[i].Start > n.Start })

	return i < len(keep) && keep[i].Start-n.Start <= reach+gridEpsilon
}

// editScreenNotes are the screen notes of a talking-head edit, nil without one or in a workout.
func editScreenNotes(edit *Edit) []ScreenNote {
	if edit == nil || edit.Mode == WorkoutMode {
		return nil
	}

	return edit.ScreenNotes
}
