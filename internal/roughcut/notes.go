package roughcut

import (
	"cmp"
	"slices"
)

// RemapScreenNotes puts the screen notes of an edit onto the timeline of the cut by keep: ordered by time, without the
// notes that start in a cut piece (what they named is not in the reel), an end inside a cut moved to where the reel
// goes on, an end not after the start dropped (0: the subtitles choose). Nil when none is left.
func RemapScreenNotes(notes []ScreenNote, keep []Interval) []ScreenNote {
	if len(notes) == 0 || len(keep) == 0 {
		return nil
	}

	sorted := slices.Clone(notes)
	slices.SortStableFunc(sorted, func(a, b ScreenNote) int { return cmp.Compare(a.Start, b.Start) })

	out := make([]ScreenNote, 0, len(sorted))

	for _, n := range sorted {
		if _, ok := findInterval(keep, n.Start); !ok {
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

// editScreenNotes are the screen notes of a talking-head edit, nil without one or in a workout.
func editScreenNotes(edit *Edit) []ScreenNote {
	if edit == nil || edit.Mode == WorkoutMode {
		return nil
	}

	return edit.ScreenNotes
}
