package agents

import "regexp"

const (
	moduleName    = "agents"
	agentFileName = "agent.md"
	skillFileName = "SKILL.md"
	skillsDirName = "skills"
	// templatePrefix marks directories that are not loaded as agents, e.g. the _example template.
	templatePrefix = "_"
	logKeyAgent    = "agent"
)

var (
	// namePattern is the format of agent and skill names; an agent or skill name equals its directory name.
	namePattern = regexp.MustCompile(`^[a-z0-9_][a-z0-9_-]*$`)
	// toolNamePattern is the tool name format accepted by the Claude API.
	toolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
)

// Agent is a role loaded from agents/<name>/agent.md.
type Agent struct {
	Name        string
	DisplayName string
	// Model overrides LLM_DEFAULT_MODEL; empty means the default.
	Model string
	// Tools are the names of the registered tools the agent may call.
	Tools []string
	// Skills are in the order of the frontmatter list.
	Skills []Skill
	// Prompt is the body of agent.md: the system prompt of the role.
	Prompt string
}

// Skill is an instruction loaded from agents/<agent>/skills/<name>/SKILL.md. The system prompt lists
// only Name and Description; the model loads Body on demand with the load_skill tool.
type Skill struct {
	Name        string
	Description string
	Body        string
}

func (a *Agent) skill(name string) (Skill, bool) {
	for i := range a.Skills {
		if a.Skills[i].Name == name {
			return a.Skills[i], true
		}
	}

	return Skill{}, false
}

func (a *Agent) skillNames() []string {
	names := make([]string, 0, len(a.Skills))
	for i := range a.Skills {
		names = append(names, a.Skills[i].Name)
	}

	return names
}
