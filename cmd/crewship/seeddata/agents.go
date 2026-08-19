package seeddata

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// AgentDef defines an agent to seed. PromptSlug is used to load the system
// prompt from the embedded prompts/ directory.
type AgentDef struct {
	Name           string `yaml:"name"`
	Slug           string `yaml:"slug"`
	CrewSlug       string `yaml:"crew_slug"`
	RoleTitle      string `yaml:"role_title"`
	AgentRole      string `yaml:"agent_role"`
	CLIAdapter     string `yaml:"cli_adapter"`
	LLMProvider    string `yaml:"llm_provider"`
	LLMModel       string `yaml:"llm_model"`
	ToolProfile    string `yaml:"tool_profile"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	MemoryEnabled  bool   `yaml:"memory_enabled"`
	PromptSlug     string `yaml:"prompt_slug"` // matches prompts/{slug}.md
	// SuggestedPrompts is agents.suggested_prompts verbatim: the chips shown
	// under an empty chat, one question per line. Left empty for most agents
	// on purpose — the fallback role packs (lib/agent-suggestions.ts) still
	// answer for those, and a demo where every agent has chips shows nothing
	// about which ones were written for the job.
	//
	// Capped by the server at 8 prompts of 120 characters
	// (internal/api/agents_suggested_prompts.go); the caps are asserted on
	// this file in agents_chat_surface_test.go so an over-long line is a red
	// test rather than a 400 mid-seed.
	SuggestedPrompts string `yaml:"suggested_prompts,omitempty"`
	// AskFormsSlug names the questionnaire document in askforms/{slug}.json,
	// the same indirection PromptSlug uses for prompts/{slug}.md. The JSON is
	// stored verbatim in agents.ask_forms and is validated by
	// internal/askforms at test time and again by the server on write.
	AskFormsSlug string `yaml:"ask_forms_slug,omitempty"`
	// RequiresEnv gates seeding of this agent: when non-empty, the agent is
	// only created if the named env var == "1". Used for the opt-in
	// local-Ollama demo agent (CREWSHIP_SEED_OLLAMA).
	RequiresEnv string `yaml:"requires_env,omitempty"`
}

// Agents — the 12 demo agents seeded into a fresh workspace.
//
// Loaded from builtin/agents.yaml at init time. Migrated from a Go-literal
// list in F2 step 6.
var Agents = mustLoadAgents()

func mustLoadAgents() []AgentDef {
	data, err := builtinFS.ReadFile("builtin/agents.yaml")
	if err != nil {
		panic(fmt.Sprintf("seeddata: read builtin/agents.yaml: %v", err))
	}
	var doc struct {
		Agents []AgentDef `yaml:"agents"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		panic(fmt.Sprintf("seeddata: parse builtin/agents.yaml: %v", err))
	}
	if len(doc.Agents) == 0 {
		panic("seeddata: builtin/agents.yaml decoded to zero agents — schema drift?")
	}
	return doc.Agents
}

// ActiveAgents returns the agents that should be seeded in the current
// environment — the same env-gate rule as ActiveCrews (RequiresEnv empty, or
// its env var == "1").
func ActiveAgents() []AgentDef {
	out := make([]AgentDef, 0, len(Agents))
	for _, a := range Agents {
		if a.RequiresEnv != "" && os.Getenv(a.RequiresEnv) != "1" {
			continue
		}
		out = append(out, a)
	}
	return out
}
