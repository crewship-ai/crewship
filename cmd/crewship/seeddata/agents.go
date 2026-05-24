package seeddata

import (
	"fmt"

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
	return doc.Agents
}
