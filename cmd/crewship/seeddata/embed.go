package seeddata

import "embed"

//go:embed prompts/*.md
var promptsFS embed.FS

// AgentPrompt returns the system prompt for the given agent slug.
func AgentPrompt(slug string) string {
	data, err := promptsFS.ReadFile("prompts/" + slug + ".md")
	if err != nil {
		panic("missing prompt for agent: " + slug)
	}
	return string(data)
}
