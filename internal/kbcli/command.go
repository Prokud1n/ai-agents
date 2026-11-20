// Package kbcli is the `kb` command: loads brand materials into the knowledge base, searches and deletes them.
package kbcli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/knowledge"
)

// Usage is printed on invalid arguments.
const Usage = `usage:
  kb ingest <file or folder> [-kind brand|product|post|other] [-force]
                                                                 load .md, .txt, .docx, .pdf files from KB_ROOT
  kb search [-limit 5] [-kind brand,post] [--] "<query>"         find fragments by meaning
  kb delete [--] <source>                                        remove a document (source as printed by ingest)
  kb reembed                                                     fill in missing embeddings, move to the current model

Files are ingested only from KB_ROOT (default ./kb); a document's source is its path relative to it.
Without -kind an existing document keeps its kind (a new one is "other"); changing the kind needs -force.`

var ErrUsage = errors.New("invalid arguments")

// flagsEnd ends the flags of a command, as in the flag package.
const flagsEnd = "--"

type Action string

const (
	IngestAction  Action = "ingest"
	SearchAction  Action = "search"
	DeleteAction  Action = "delete"
	ReembedAction Action = "reembed"
)

type Command struct {
	Action Action
	Arg    string // path for ingest, query for search, source for delete; empty for reembed
	Kind   knowledge.Kind
	Kinds  []knowledge.Kind
	Limit  int
	Force  bool // ingest: allow changing the kind of existing documents
}

// ParseCommand parses arguments without the program name. Flags may go before or after the positional argument.
func ParseCommand(args []string) (Command, error) {
	if len(args) == 0 {
		return Command{}, fmt.Errorf("no command: %w", ErrUsage)
	}

	cmd := Command{Action: Action(args[0])}

	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var kind string

	switch cmd.Action {
	case IngestAction:
		fs.StringVar(&kind, "kind", "", "document kind; empty keeps the existing one")
		fs.BoolVar(&cmd.Force, "force", false, "allow changing the kind of existing documents")
	case SearchAction:
		fs.StringVar(&kind, "kind", "", "comma-separated kinds to search in")
		fs.IntVar(&cmd.Limit, "limit", knowledge.DefaultLimit, "number of results")
	case DeleteAction, ReembedAction:
	default:
		return Command{}, fmt.Errorf("unknown command %q: %w", args[0], ErrUsage)
	}

	positional, err := parseInterspersed(fs, args[1:])
	if err != nil {
		return Command{}, err
	}

	if cmd.Action == ReembedAction {
		if len(positional) != 0 {
			return Command{}, fmt.Errorf("reembed takes no arguments: %w", ErrUsage)
		}

		return cmd, nil
	}

	if len(positional) != 1 || strings.TrimSpace(positional[0]) == "" {
		return Command{}, fmt.Errorf("%s needs exactly one argument: %w", cmd.Action, ErrUsage)
	}

	cmd.Arg = positional[0]

	if err := withKinds(&cmd, kind); err != nil {
		return Command{}, err
	}

	return cmd, nil
}

func withKinds(cmd *Command, kind string) error {
	switch cmd.Action {
	case IngestAction:
		if kind == "" {
			return nil
		}

		k, err := knowledge.ParseKind(kind)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrUsage, err)
		}

		cmd.Kind = k
	case SearchAction:
		for name := range strings.SplitSeq(kind, ",") {
			if name = strings.TrimSpace(name); name == "" {
				continue
			}

			k, err := knowledge.ParseKind(name)
			if err != nil {
				return fmt.Errorf("%w: %w", ErrUsage, err)
			}

			cmd.Kinds = append(cmd.Kinds, k)
		}
	case DeleteAction, ReembedAction:
	}

	return nil
}

// parseInterspersed lets flags follow positional arguments: `kb ingest ./brand -kind brand`.
// "--" ends the flags: everything after it is positional, so a query may start with "-" (`kb search -- "-5 кг"`).
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string

	for {
		if err := fs.Parse(args); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUsage, err)
		}

		if fs.NArg() == 0 {
			return positional, nil
		}

		if consumed := len(args) - fs.NArg(); consumed > 0 && args[consumed-1] == flagsEnd {
			return append(positional, fs.Args()...), nil
		}

		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}
