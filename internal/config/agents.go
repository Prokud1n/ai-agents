package config

// Agents configures the agent registry: every subdirectory of Dir is an agent (agent.md + optional local
// skills/), every subdirectory of SkillsDir is a shared skill that agents list by name.
type Agents struct {
	Dir       string `env:"AGENTS_DIR" envDefault:"./agents"`
	SkillsDir string `env:"SKILLS_DIR" envDefault:"./skills"`
}
