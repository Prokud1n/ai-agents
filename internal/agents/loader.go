package agents

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// skillSet is the shared skills of SKILLS_DIR; root is used in error messages.
type skillSet struct {
	root   string
	byName map[string]Skill
}

// loadSharedSkills loads every subdirectory of fsys as a skill (hidden and "_" directories are skipped).
// A missing directory means no shared skills: an agent listing one then fails with the expected path.
func loadSharedSkills(fsys fs.FS, root string) (skillSet, error) {
	set := skillSet{root: root, byName: map[string]Skill{}}

	entries, err := fs.ReadDir(fsys, ".")
	if errors.Is(err, fs.ErrNotExist) {
		return set, nil
	}

	if err != nil {
		return skillSet{}, fmt.Errorf("failed to read skills directory %q: %w", root, err)
	}

	var errs []error

	for _, entry := range entries {
		if skipEntry(entry) {
			continue
		}

		name := entry.Name()
		if !namePattern.MatchString(name) {
			errs = append(errs, withPath(path.Join(root, name),
				invalid(fmt.Sprintf("skill directory name must match %s", namePattern))))

			continue
		}

		skill, err := loadSkill(fsys, name, name)
		if err != nil {
			errs = append(errs, withPath(path.Join(root, name, skillFileName), err))

			continue
		}

		set.byName[name] = skill
	}

	if len(errs) > 0 {
		return skillSet{}, errors.Join(errs...)
	}

	return set, nil
}

// loadAgents loads every subdirectory of fsys as an agent. Directories starting with "." (hidden) or "_"
// (templates such as _example) are skipped. All problems are collected and returned together, each
// prefixed with its file path under root.
func loadAgents(fsys fs.FS, root string, shared skillSet, tools map[string]Tool) (map[string]*Agent, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("failed to read agents directory %q: %w", root, err)
	}

	loaded := make(map[string]*Agent, len(entries))

	var errs []error

	for _, entry := range entries {
		if skipEntry(entry) {
			continue
		}

		agent, err := loadAgent(fsys, entry.Name(), shared, tools)
		if err != nil {
			errs = append(errs, withPath(path.Join(root, entry.Name(), agentFileName), err))

			continue
		}

		loaded[agent.Name] = agent
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	return loaded, nil
}

func skipEntry(entry fs.DirEntry) bool {
	return !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || strings.HasPrefix(entry.Name(), templatePrefix)
}

// loadAgent loads dir/agent.md and resolves the skills it lists: a local skill (dir/skills/<name>) or
// a shared one; a local skill with the name of a shared one is an error, not a silent override.
func loadAgent(fsys fs.FS, dir string, shared skillSet, tools map[string]Tool) (*Agent, error) {
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

	local, err := localSkillNames(fsys, path.Join(dir, skillsDirName))
	if err != nil {
		return nil, err
	}

	if err := conflicts(local, shared); err != nil {
		return nil, err
	}

	skills, err := resolveSkills(fsys, dir, &meta, local, shared)
	if err != nil {
		return nil, err
	}

	return &Agent{
		Name:        meta.Name,
		DisplayName: meta.DisplayName,
		Model:       meta.Model,
		Tools:       slices.Clone(meta.Tools),
		Prompt:      doc.body,
		Skills:      skills,
	}, nil
}

// localSkillNames lists the skill directories of an agent; no skills/ directory means none.
func localSkillNames(fsys fs.FS, dir string) (map[string]struct{}, error) {
	names := map[string]struct{}{}

	entries, err := fs.ReadDir(fsys, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return names, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to read local skills: %w: %w", ErrInvalidAgent, err)
	}

	for _, entry := range entries {
		if !skipEntry(entry) {
			names[entry.Name()] = struct{}{}
		}
	}

	return names, nil
}

func conflicts(local map[string]struct{}, shared skillSet) error {
	var errs []error

	for _, name := range sortedKeys(local) {
		if _, ok := shared.byName[name]; ok {
			errs = append(errs, invalid(fmt.Sprintf(
				"local skill %q (%s) has the name of a shared skill (%s): rename one of them",
				name, path.Join(skillsDirName, name, skillFileName), path.Join(shared.root, name, skillFileName))))
		}
	}

	return errors.Join(errs...)
}

// resolveSkills loads the listed skills in their order and applies the agent's overrides.
func resolveSkills(
	fsys fs.FS,
	dir string,
	meta *agentFrontmatter,
	local map[string]struct{},
	shared skillSet,
) ([]Skill, error) {
	skills := make([]Skill, 0, len(meta.Skills))

	var errs []error

	for _, name := range meta.Skills {
		localPath := path.Join(skillsDirName, name, skillFileName)

		var (
			skill Skill
			err   error
		)

		_, isLocal := local[name]
		sharedSkill, isShared := shared.byName[name]

		switch {
		case isLocal:
			skill, err = loadSkill(fsys, path.Join(dir, skillsDirName, name), name)
		case isShared:
			skill = sharedSkill
		case strings.HasPrefix(name, templatePrefix):
			err = invalid(fmt.Sprintf("skill directories starting with %q are templates and are skipped: "+
				"rename the skill (expected a local %s or a shared %s)",
				templatePrefix, localPath, path.Join(shared.root, name, skillFileName)))
		default:
			err = invalid(fmt.Sprintf("not found: expected a local %s or a shared %s",
				localPath, path.Join(shared.root, name, skillFileName)))
		}

		if err != nil {
			errs = append(errs, fmt.Errorf("skill %q: %w", name, err))

			continue
		}

		skill.Override = strings.TrimSpace(meta.SkillOverrides[name])
		skills = append(skills, skill)
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	return skills, nil
}

// validateAgent checks required fields, names, duplicates, overrides and that every tool is registered.
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
	errs = append(errs, validateTools(meta.Tools, tools)...)
	errs = append(errs, validateSkillNames(meta)...)

	return errors.Join(errs...)
}

func validateTools(names []string, tools map[string]Tool) []error {
	var errs []error

	for _, name := range names {
		if name == LoadSkillToolName {
			errs = append(errs, invalid(fmt.Sprintf("tool %q is built in: it is given to agents with skills automatically",
				LoadSkillToolName)))

			continue
		}

		if _, ok := tools[name]; !ok {
			errs = append(errs, invalid(fmt.Sprintf("unknown tool %q, registered tools: %v", name, sortedKeys(tools))))
		}
	}

	return errs
}

// validateSkillNames checks skill names (they are also path elements, so no traversal) and that every
// override is for a listed skill and is not empty.
func validateSkillNames(meta *agentFrontmatter) []error {
	var errs []error

	for _, name := range meta.Skills {
		if !namePattern.MatchString(name) {
			errs = append(errs, invalid(fmt.Sprintf("skill name %q must match %s", name, namePattern)))
		}
	}

	for _, name := range sortedKeys(meta.SkillOverrides) {
		if !slices.Contains(meta.Skills, name) {
			errs = append(errs, invalid(fmt.Sprintf("skill_overrides: skill %q is not in skills %v", name, meta.Skills)))

			continue
		}

		if strings.TrimSpace(meta.SkillOverrides[name]) == "" {
			errs = append(errs, invalid(fmt.Sprintf("skill_overrides: override of %q is empty", name)))
		}
	}

	return errs
}

// loadSkill loads dir/SKILL.md; the skill name must equal its directory name.
func loadSkill(fsys fs.FS, dir, name string) (Skill, error) {
	content, err := fs.ReadFile(fsys, path.Join(dir, skillFileName))
	if err != nil {
		return Skill{}, fmt.Errorf("failed to read skill file: %w: %w", ErrInvalidAgent, err)
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
	return withPath(msg, ErrInvalidAgent)
}

// withPath prefixes err with a file path (or another context) in error messages.
func withPath(prefix string, err error) error {
	return fmt.Errorf("%s: %w", prefix, err)
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
