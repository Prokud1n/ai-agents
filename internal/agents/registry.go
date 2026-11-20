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
	agents       map[string]*Agent
	tools        map[string]Tool
	sharedSkills []string
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
	registry, err := LoadRegistry(cfg.Dir, cfg.SkillsDir, tools)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	log.InfoContext(ctx, "agents: loaded", "dir", cfg.Dir, "skills_dir", cfg.SkillsDir,
		"names", registry.Names(), "shared_skills", registry.sharedSkills, "tools", sortedKeys(registry.tools))

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

	registry.warnUnavailableTools(ctx, log)

	return registry, nil
}

// warnUnavailableTools logs every agent that lists a tool which is unavailable now (see toolAvailability):
// the agent still starts, and the tool answers it with an error.
func (r *Registry) warnUnavailableTools(ctx context.Context, log *slog.Logger) {
	for _, u := range r.unavailableTools() {
		log.WarnContext(ctx, "agents: tool is unavailable, the agent will get an error from it",
			logKeyAgent, u.agent, "tool", u.tool, "reason", u.reason.Error())
	}
}

type unavailableTool struct {
	agent  string
	tool   string
	reason error
}

// unavailableTools lists the (agent, tool) pairs where the agent may call a tool that reports itself
// unavailable, sorted by agent name and in the order of the agent's tools.
func (r *Registry) unavailableTools() []unavailableTool {
	var unavailable []unavailableTool

	for _, name := range r.Names() {
		for _, toolName := range r.agents[name].Tools {
			checker, ok := r.tools[toolName].(toolAvailability)
			if !ok {
				continue
			}

			if err := checker.Available(); err != nil {
				unavailable = append(unavailable, unavailableTool{agent: name, tool: toolName, reason: err})
			}
		}
	}

	return unavailable
}

// LoadRegistry loads shared skills from skillsDir and agents from agentsDir; tools are all tools agents
// may reference. A missing skillsDir means no shared skills.
func LoadRegistry(agentsDir, skillsDir string, tools []Tool) (*Registry, error) {
	byName, err := registerTools(tools)
	if err != nil {
		return nil, err
	}

	shared, err := loadSharedSkills(os.DirFS(skillsDir), skillsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load shared skills from %q: %w", skillsDir, err)
	}

	loaded, err := loadAgents(os.DirFS(agentsDir), agentsDir, shared, byName)
	if err != nil {
		return nil, fmt.Errorf("failed to load agents from %q: %w", agentsDir, err)
	}

	return &Registry{agents: loaded, tools: byName, sharedSkills: sortedKeys(shared.byName)}, nil
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
