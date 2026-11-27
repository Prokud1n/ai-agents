package roughcut

import (
	"cmp"
	"math"
	"slices"
	"sort"

	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// How far a screen note that starts in a cut piece moves on to the next kept piece (seconds of the source). The worker
// cuts by the sound, not by Whisper's word edges: a kept word whose start Whisper put early in the silence usually
// starts the kept piece up to wordInnerReach later (and a frame or two of the grid), so a note set near it moves by up
// to noteShiftReach. A word stretched further (cutGrid.around looks for its sound deeper) starts later still: a note
// set on its start is moved with the word (notesOnTrimmed) before the reach is counted. A section heading stands where
// the trainer reads it out («ошибка номер два», «пункт первый»), and that reading is cut from the sound: the plate
// replaces it, so the section moves to where the speech goes on after the reading and its pause.
//
// NoteCheckSlack: the app checks the notes on its plan by the margins (no sound) with the reach shortened by it, so a
// note the app lets through is never dropped by the worker's cut; in doubt the model is asked to move it.
const (
	// noteWordEpsilon: a note within it of a word's start stands on that word (the editor rounds to milliseconds).
	noteWordEpsilon   = 0.0015
	noteShiftMargin   = 0.2
	noteShiftReach    = wordInnerReach + noteShiftMargin
	sectionShiftReach = 3.0
	NoteCheckSlack    = noteShiftReach
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
	return NoteShown(n, keep, 0)
}

// NoteShown reports whether RemapScreenNotes keeps the note on the cut by keep, with its reach shortened by slack
// (NoteCheckSlack: the stricter check of the app).
func NoteShown(n ScreenNote, keep []Interval, slack float64) bool {
	if _, ok := findInterval(keep, n.Start); ok {
		return true
	}

	reach := noteShiftReach
	if n.Kind == SectionNote {
		reach = sectionShiftReach
	}

	i := sort.Search(len(keep), func(i int) bool { return keep[i].Start > n.Start })

	return i < len(keep) && keep[i].Start-n.Start <= reach-slack+gridEpsilon
}

// notesOnTrimmed moves the notes that start at a word of src (as the editor sets them: at the word's start) to the start
// trimmedSrc gives that word (trimSilentEdges: its silent start was cut off by the sound), so a note on a kept word is
// never lost however early Whisper started it. notes itself when nothing moved.
func notesOnTrimmed(notes []ScreenNote, src, trimmedSrc *transcription.Result) []ScreenNote {
	if len(notes) == 0 || src == trimmedSrc || len(src.Words) != len(trimmedSrc.Words) {
		return notes
	}

	out := make([]ScreenNote, 0, len(notes))

	for _, n := range notes {
		moved := n

		for i, w := range src.Words {
			if t := trimmedSrc.Words[i].Start; t != w.Start && math.Abs(n.Start-w.Start) < noteWordEpsilon {
				moved.Start = t

				break
			}
		}

		out = append(out, moved)
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
