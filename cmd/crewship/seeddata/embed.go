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

// builtinFS holds the seed-data catalogues that used to live as Go
// struct literals inline in skills.go / agents.go / crews.go /
// integrations.go / issues.go. Migrated to YAML in F2 step 6 so
// non-Go contributors can edit a skill body or add an agent without
// writing Go. Loaders sit next to their respective Def types and
// panic on parse failure (build-time bug, not runtime data
// problem — the files ship with the binary).
//
//go:embed builtin/*.yaml
var builtinFS embed.FS
