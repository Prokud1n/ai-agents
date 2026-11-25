package editlocal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Prokud1n/ai-agents/internal/media"
)

// StateFile in the output directory keeps what a revision needs: the task, the source in S3 and the last version.
const StateFile = "state.json"

var (
	// ErrNoState: the output directory has no state of an edit (no version made there yet).
	ErrNoState = errors.New("no edit in the output directory")
	// ErrInvalidState: the state file cannot be used (edited by hand, another format).
	ErrInvalidState = errors.New("invalid state of the local edit")
)

// State is the local edit in its output directory.
type State struct {
	TaskID int64 `json:"task_id"`
	// SourceKey of the source video in S3 (MEDIA_KEY_PREFIX).
	SourceKey string `json:"source_key"`
	// SourceFile is the name of the local file the source came from, for people.
	SourceFile string `json:"source_file"`
	// Caption is the trainer's caption of the source (the Input of every version).
	Caption string `json:"caption,omitempty"`
	// Version is the last version made; its files are in v<Version>/.
	Version   int        `json:"version"`
	Revisions []Revision `json:"revisions,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Revision is the text of the revision that made the version.
type Revision struct {
	Version int    `json:"version"`
	Text    string `json:"text"`
}

// VersionDir is the directory of the version's files in the output directory.
func VersionDir(version int) string {
	return "v" + strconv.Itoa(version)
}

// Next is the state after the revision is made: the next version with the revision added; s is not changed.
func (s *State) Next(revision string) *State {
	next := *s
	next.Version = s.Version + 1

	revisions := make([]Revision, 0, len(s.Revisions)+1)
	revisions = append(revisions, s.Revisions...)
	revisions = append(revisions, Revision{Version: next.Version, Text: revision})
	next.Revisions = revisions

	return &next
}

func (s *State) validate() error {
	switch {
	case !IsLocalTaskID(s.TaskID):
		return fmt.Errorf("task %d is not a local edit: %w", s.TaskID, ErrInvalidState)
	case s.Version < 1:
		return fmt.Errorf("version %d: %w", s.Version, ErrInvalidState)
	}

	if _, err := media.ParseKey(s.SourceKey); err != nil {
		return fmt.Errorf("source key: %w: %w", ErrInvalidState, err)
	}

	return nil
}

// LoadState reads the state of the edit in dir: ErrNoState when there is none, ErrInvalidState when it is broken.
func LoadState(dir string) (*State, error) {
	raw, err := os.ReadFile(filepath.Clean(filepath.Join(dir, StateFile)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", dir, ErrNoState)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", StateFile, err)
	}

	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("failed to decode %s: %w: %w", StateFile, ErrInvalidState, err)
	}

	if err := state.validate(); err != nil {
		return nil, err
	}

	return &state, nil
}

// SaveState writes the state to dir at once (a temporary file renamed over the old one), so an interrupted run leaves
// the previous state whole.
func SaveState(dir string, state *State) error {
	if err := state.validate(); err != nil {
		return err
	}

	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode %s: %w", StateFile, err)
	}

	tmp, err := os.CreateTemp(dir, "."+StateFile+"-*")
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", StateFile, err)
	}

	_, writeErr := tmp.Write(append(raw, '\n'))
	if err := errors.Join(writeErr, tmp.Close()); err != nil {
		_ = os.Remove(tmp.Name())

		return fmt.Errorf("failed to write %s: %w", StateFile, err)
	}

	if err := os.Rename(tmp.Name(), filepath.Join(dir, StateFile)); err != nil {
		_ = os.Remove(tmp.Name())

		return fmt.Errorf("failed to save %s: %w", StateFile, err)
	}

	return nil
}
