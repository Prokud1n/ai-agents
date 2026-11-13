package agents

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// loadAgents loads every subdirectory of fsys as an agent (hidden ones, starting with ".", are skipped).
// All problems are collected and returned together, each prefixed with its file path under root.
func loadAgents(fsys fs.FS, root string, tools map[string]Tool) (map[string]*Agent, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("failed to read agents directory %q: %w", root, err)
	}

	loaded := make(map[string]*Agent, len(entries))

	var errs []error

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		agent, err := loadAgent(fsys, entry.Name(), tools)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path.Join(root, entry.Name(), agentFileName), err))

			continue
		}

		loaded[agent.Name] = agent
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	return loaded, nil
}

// loadAgent loads dir/agent.md and the skills it lists.
func loadAgent(fsys fs.FS, dir string, tools map[string]Tool) (*Agent, error) {
	content, err := fs.ReadFile(fsys, path.Join(dir, agentFileName))
	if err != nil {
		return nil, fmt.Errorf("failed to read agent file: %w: %w", ErrInvalidAgent, err)
	}

	doc, err := splitFrontmatter(content)
	if err != nil {
		return nil, err
	}

	var meta agentFrontmatter
	if err := decodeFrontmatter(doc.frontmatter, &meta); err != nil {
		return nil, err
	}

	if err := validateAgent(&meta, doc.body, dir, tools); err != nil {
		return nil, err
	}

	agent := &Agent{
		Name:        meta.Name,
		DisplayName: meta.DisplayName,
		Model:       meta.Model,
		Tools:       slices.Clone(meta.Tools),
		Prompt:      doc.body,
		Skills:      make([]Skill, 0, len(meta.Skills)),
	}

	var errs []error

	for _, name := range meta.Skills {
		skill, err := loadSkill(fsys, path.Join(dir, skillsDirName, name), name)
		if err != nil {
			errs = append(errs, fmt.Errorf("skill %q (%s): %w", name, path.Join(skillsDirName, name, skillFileName), err))

			continue
		}

		agent.Skills = append(agent.Skills, skill)
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	return agent, nil
}

// validateAgent checks required fields, names, duplicates and that every tool is registered.
func validateAgent(meta *agentFrontmatter, body, dir string, tools map[string]Tool) error {
	var errs []error

	switch {
	case meta.Name == "":
		errs = append(errs, invalid("name is required"))
	case meta.Name != dir:
		errs = append(errs, invalid(fmt.Sprintf("name %q must match the directory name %q", meta.Name, dir)))
	case !namePattern.MatchString(meta.Name):
		errs = append(errs, invalid(fmt.Sprintf("name %q must match %s", meta.Name, namePattern)))
	}

	if strings.TrimSpace(meta.DisplayName) == "" {
		errs = append(errs, invalid("display_name is required"))
	}

	if body == "" {
		errs = append(errs, invalid("body (the system prompt of the role) is empty"))
	}

	errs = append(errs, duplicates("tools", meta.Tools)...)
	errs = append(errs, duplicates("skills", meta.Skills)...)

	for _, name := range meta.Tools {
		if name == LoadSkillToolName {
			errs = append(errs, invalid(fmt.Sprintf("tool %q is built in: it is given to agents with skills automatically",
				LoadSkillToolName)))

			continue
		}

		if _, ok := tools[name]; !ok {
			errs = append(errs, invalid(fmt.Sprintf("unknown tool %q, registered tools: %v", name, sortedKeys(tools))))
		}
	}

	for _, name := range meta.Skills {
		if !namePattern.MatchString(name) {
			errs = append(errs, invalid(fmt.Sprintf("skill name %q must match %s", name, namePattern)))
		}
	}

	return errors.Join(errs...)
}

// loadSkill loads dir/SKILL.md; the skill name must equal its directory name.
func loadSkill(fsys fs.FS, dir, name string) (Skill, error) {
	content, err := fs.ReadFile(fsys, path.Join(dir, skillFileName))
	if err != nil {
		return Skill{}, fmt.Errorf("skill is listed in agent.md but its file is missing: %w: %w", ErrInvalidAgent, err)
	}

	doc, err := splitFrontmatter(content)
	if err != nil {
		return Skill{}, err
	}

	var meta skillFrontmatter
	if err := decodeFrontmatter(doc.frontmatter, &meta); err != nil {
		return Skill{}, err
	}

	var errs []error

	if meta.Name != name {
		errs = append(errs, invalid(fmt.Sprintf("name %q must match the directory name %q", meta.Name, name)))
	}

	if strings.TrimSpace(meta.Description) == "" {
		errs = append(errs, invalid("description is required"))
	}

	if doc.body == "" {
		errs = append(errs, invalid("body (the skill instruction) is empty"))
	}

	if len(errs) > 0 {
		return Skill{}, errors.Join(errs...)
	}

	return Skill{Name: meta.Name, Description: strings.TrimSpace(meta.Description), Body: doc.body}, nil
}

func invalid(msg string) error {
	return fmt.Errorf("%s: %w", msg, ErrInvalidAgent)
}

func duplicates(field string, names []string) []error {
	seen := make(map[string]struct{}, len(names))

	var errs []error

	for _, name := range names {
		if _, ok := seen[name]; ok {
			errs = append(errs, invalid(fmt.Sprintf("%s: %q is listed twice", field, name)))
		}

		seen[name] = struct{}{}
	}

	return errs
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	return keys
}
