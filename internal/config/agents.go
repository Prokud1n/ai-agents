package config

// Agents configures the agent registry: every subdirectory of Dir is an agent (agent.md + skills/).
type Agents struct {
	Dir string `env:"AGENTS_DIR" envDefault:"./agents"`
}
