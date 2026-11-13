package agents

import (
	"fmt"
	"log/slog"
	"os"

	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/config"
)

// Registry holds the agents loaded from AGENTS_DIR and the registered tools.
type Registry struct {
	agents map[string]*Agent
	tools  map[string]Tool
}

type RegistryParams struct {
	fx.In

	Config *config.Agents
	Log    *slog.Logger
	Tools  []Tool `group:"agent_tools"`
}

// NewRegistry loads and validates agents: any problem stops the application start.
func NewRegistry(p RegistryParams) (*Registry, error) {
	registry, err := LoadRegistry(p.Config.Dir, p.Tools)
	if err != nil {
		return nil, err
	}

	p.Log.Info("agents: loaded", "dir", p.Config.Dir, "names", registry.Names(), "tools", sortedKeys(registry.tools))

	return registry, nil
}

// LoadRegistry loads agents from dir; tools are all tools agents may reference.
func LoadRegistry(dir string, tools []Tool) (*Registry, error) {
	byName := make(map[string]Tool, len(tools))

	for _, t := range tools {
		name := t.Name()
		if _, exists := byName[name]; exists || name == LoadSkillToolName {
			return nil, fmt.Errorf("failed to register tool %q: name is taken: %w", name, ErrInvalidAgent)
		}

		byName[name] = t
	}

	loaded, err := loadAgents(os.DirFS(dir), dir, byName)
	if err != nil {
		return nil, fmt.Errorf("failed to load agents from %q: %w", dir, err)
	}

	return &Registry{agents: loaded, tools: byName}, nil
}

func (r *Registry) Get(name string) (*Agent, error) {
	agent, ok := r.agents[name]
	if !ok {
		return nil, fmt.Errorf("failed to find agent %q, available: %v: %w", name, r.Names(), ErrAgentNotFound)
	}

	return agent, nil
}

// Names returns agent names in sorted order.
func (r *Registry) Names() []string {
	return sortedKeys(r.agents)
}

// toolsFor returns the tools an agent may call: its allowed tools plus load_skill when it has skills.
func (r *Registry) toolsFor(agent *Agent) []Tool {
	tools := make([]Tool, 0, len(agent.Tools)+1)

	if len(agent.Skills) > 0 {
		tools = append(tools, &loadSkillTool{agent: agent})
	}

	for _, name := range agent.Tools {
		tools = append(tools, r.tools[name])
	}

	return tools
}
