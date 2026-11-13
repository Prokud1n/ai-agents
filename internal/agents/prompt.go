package agents

import "strings"

const skillsIntro = "## Skills\n\n" +
	"You have skills: detailed instructions for specific tasks. When a task matches a skill description, " +
	"call the `" + LoadSkillToolName + "` tool with the skill name first and follow the instructions it returns.\n"

// BuildSystemPrompt is the role prompt followed by the list of skills (name and description only:
// skill bodies are loaded on demand with load_skill).
func BuildSystemPrompt(agent *Agent) string {
	if len(agent.Skills) == 0 {
		return agent.Prompt
	}

	var b strings.Builder

	b.WriteString(agent.Prompt)
	b.WriteString("\n\n")
	b.WriteString(skillsIntro)
	b.WriteByte('\n')

	for i := range agent.Skills {
		b.WriteString("- `")
		b.WriteString(agent.Skills[i].Name)
		b.WriteString("`: ")
		b.WriteString(agent.Skills[i].Description)
		b.WriteByte('\n')
	}

	return strings.TrimRight(b.String(), lineBreak)
}
