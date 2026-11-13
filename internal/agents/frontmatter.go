package agents

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	frontmatterDelimiter = "---"
	lineBreak            = "\n"
)

// agentFrontmatter is the YAML header of agent.md.
type agentFrontmatter struct {
	Name        string   `yaml:"name"`
	DisplayName string   `yaml:"display_name"`
	Model       string   `yaml:"model"`
	Tools       []string `yaml:"tools"`
	Skills      []string `yaml:"skills"`
}

// skillFrontmatter is the YAML header of SKILL.md.
type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// document is a markdown file split into its YAML frontmatter and body.
type document struct {
	frontmatter []byte
	body        string
}

// splitFrontmatter splits "---\n<yaml>\n---\n<body>". The body is trimmed.
func splitFrontmatter(content []byte) (document, error) {
	text := strings.ReplaceAll(string(content), "\r\n", lineBreak)
	text = strings.TrimPrefix(text, "\ufeff")

	first, rest, found := strings.Cut(text, lineBreak)
	if !found || strings.TrimSpace(first) != frontmatterDelimiter {
		return document{}, fmt.Errorf("file must start with a %q line opening the YAML frontmatter: %w",
			frontmatterDelimiter, ErrInvalidAgent)
	}

	var header strings.Builder

	for {
		line, tail, more := strings.Cut(rest, lineBreak)
		if strings.TrimSpace(line) == frontmatterDelimiter {
			return document{frontmatter: []byte(header.String()), body: strings.TrimSpace(tail)}, nil
		}

		if !more {
			return document{}, fmt.Errorf("frontmatter is not closed with a %q line: %w",
				frontmatterDelimiter, ErrInvalidAgent)
		}

		header.WriteString(line)
		header.WriteByte('\n')

		rest = tail
	}
}

// decodeFrontmatter decodes YAML strictly: unknown fields are errors, so typos do not pass silently.
func decodeFrontmatter(data []byte, out any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	if err := decoder.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("failed to parse frontmatter: %w: %w", ErrInvalidAgent, err)
	}

	return nil
}
