package migrator

import (
	"errors"
	"fmt"
)

// Command is the action of the standalone migrate binary.
type Command string

const (
	UpCommand   Command = "up"   // apply all pending migrations
	DownCommand Command = "down" // roll back one migration
)

var errUsage = errors.New("usage: migrate up|down")

// ParseCommand parses command-line arguments (without the program name).
func ParseCommand(args []string) (Command, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("failed to parse migrate command: %w", errUsage)
	}

	switch cmd := Command(args[0]); cmd {
	case UpCommand, DownCommand:
		return cmd, nil
	default:
		return "", fmt.Errorf("failed to parse migrate command %q: %w", args[0], errUsage)
	}
}
