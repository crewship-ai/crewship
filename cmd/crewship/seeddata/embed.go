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

//go:embed askforms/*.json
var askFormsFS embed.FS

// AgentAskForms returns the ask-form document for the given slug — the JSON
// array stored verbatim in agents.ask_forms. An empty slug means "this agent
// has no forms" and returns "", which the seeder reads as "send nothing".
//
// Panics on a slug with no file, like AgentPrompt: the files ship inside the
// binary, so a missing one is a build-time mistake and not something a running
// seed can do anything about. agents_chat_surface_test.go catches it first.
func AgentAskForms(slug string) string {
	if slug == "" {
		return ""
	}
	data, err := askFormsFS.ReadFile("askforms/" + slug + ".json")
	if err != nil {
		panic("missing ask forms for: " + slug)
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
