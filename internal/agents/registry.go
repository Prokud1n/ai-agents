package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/Prokud1n/ai-agents/internal/config"
)

// Registry holds the agents loaded from AGENTS_DIR and the registered tools.
type Registry struct {
	agents map[string]*Agent
	tools  map[string]Tool
}

type priceLookup interface {
	HasPrice(model string) bool
}

// NewRegistry loads and validates agents: any problem stops the application start.
// tools come from the ToolsGroup value group (see fx.go).
func NewRegistry(
	cfg *config.Agents,
	llmCfg *config.LLM,
	prices priceLookup,
	log *slog.Logger,
	tools []Tool,
) (*Registry, error) {
	registry, err := LoadRegistry(cfg.Dir, tools)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	log.InfoContext(ctx, "agents: loaded", "dir", cfg.Dir, "names", registry.Names(), "tools", sortedKeys(registry.tools))

	if len(registry.agents) == 0 {
		log.WarnContext(ctx, "agents: no agents found, agent runs are not possible", "dir", cfg.Dir)
	}

	for _, name := range registry.Names() {
		model := firstNonEmpty(registry.agents[name].Model, llmCfg.DefaultModel)
		if !prices.HasPrice(model) {
			log.WarnContext(ctx, "agents: model has no price in LLM_PRICES, its cost will not be recorded",
				logKeyAgent, name, "model", model)
		}
	}

	return registry, nil
}

// LoadRegistry loads agents from dir; tools are all tools agents may reference.
func LoadRegistry(dir string, tools []Tool) (*Registry, error) {
	byName, err := registerTools(tools)
	if err != nil {
		return nil, err
	}

	loaded, err := loadAgents(os.DirFS(dir), dir, byName)
	if err != nil {
		return nil, fmt.Errorf("failed to load agents from %q: %w", dir, err)
	}

	return &Registry{agents: loaded, tools: byName}, nil
}

// registerTools checks tool names (unique, the API format, not load_skill) and input schemas (valid JSON).
func registerTools(tools []Tool) (map[string]Tool, error) {
	byName := make(map[string]Tool, len(tools))

	for _, t := range tools {
		name := t.Name()

		if !toolNamePattern.MatchString(name) {
			return nil, fmt.Errorf("failed to register tool %q: name must match %s: %w", name, toolNamePattern, ErrInvalidAgent)
		}

		if _, exists := byName[name]; exists || name == LoadSkillToolName {
			return nil, fmt.Errorf("failed to register tool %q: name is taken: %w", name, ErrInvalidAgent)
		}

		if !json.Valid(t.InputSchema()) {
			return nil, fmt.Errorf("failed to register tool %q: input schema is not valid JSON: %w", name, ErrInvalidAgent)
		}

		byName[name] = t
	}

	return byName, nil
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
