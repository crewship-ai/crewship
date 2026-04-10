package seeddata

// AgentDef defines an agent to seed. PromptSlug is used to load the system
// prompt from the embedded prompts/ directory.
type AgentDef struct {
	Name           string
	Slug           string
	CrewSlug       string
	RoleTitle      string
	AgentRole      string
	CLIAdapter     string
	LLMProvider    string
	LLMModel       string
	ToolProfile    string
	TimeoutSeconds int
	MemoryEnabled  bool
	PromptSlug     string // matches prompts/{slug}.md
}

var Agents = []AgentDef{
	// Engineering crew
	{
		Name: "Tomáš", Slug: "tomas", CrewSlug: "engineering",
		RoleTitle: "Technical Architect", AgentRole: "LEAD",
		CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC", LLMModel: "claude-sonnet-4-5",
		ToolProfile: "FULL", TimeoutSeconds: 3600, MemoryEnabled: true, PromptSlug: "tomas",
	},
	{
		Name: "Viktor", Slug: "viktor", CrewSlug: "engineering",
		RoleTitle: "Backend Engineer", AgentRole: "AGENT",
		CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC", LLMModel: "claude-haiku-4-5",
		ToolProfile: "CODING", TimeoutSeconds: 1800, MemoryEnabled: true, PromptSlug: "viktor",
	},
	{
		Name: "Nela", Slug: "nela", CrewSlug: "engineering",
		RoleTitle: "Frontend Engineer", AgentRole: "AGENT",
		CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC", LLMModel: "claude-haiku-4-5",
		ToolProfile: "CODING", TimeoutSeconds: 1800, MemoryEnabled: true, PromptSlug: "nela",
	},
	{
		Name: "Martin", Slug: "martin", CrewSlug: "engineering",
		RoleTitle: "Infrastructure Engineer", AgentRole: "AGENT",
		CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC", LLMModel: "claude-haiku-4-5",
		ToolProfile: "CODING", TimeoutSeconds: 2400, MemoryEnabled: true, PromptSlug: "martin",
	},

	// Quality crew
	{
		Name: "Eva", Slug: "eva", CrewSlug: "quality",
		RoleTitle: "Quality Director", AgentRole: "LEAD",
		CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC", LLMModel: "claude-sonnet-4-5",
		ToolProfile: "FULL", TimeoutSeconds: 3600, MemoryEnabled: true, PromptSlug: "eva",
	},
	{
		Name: "Daniel", Slug: "daniel", CrewSlug: "quality",
		RoleTitle: "Code Reviewer", AgentRole: "AGENT",
		CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC", LLMModel: "claude-haiku-4-5",
		ToolProfile: "MINIMAL", TimeoutSeconds: 1800, MemoryEnabled: true, PromptSlug: "daniel",
	},
	{
		Name: "Petra", Slug: "petra", CrewSlug: "quality",
		RoleTitle: "Test Engineer", AgentRole: "AGENT",
		CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC", LLMModel: "claude-haiku-4-5",
		ToolProfile: "CODING", TimeoutSeconds: 2400, MemoryEnabled: true, PromptSlug: "petra",
	},
	{
		Name: "Jakub", Slug: "jakub", CrewSlug: "quality",
		RoleTitle: "Security Analyst", AgentRole: "AGENT",
		CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC", LLMModel: "claude-haiku-4-5",
		ToolProfile: "MINIMAL", TimeoutSeconds: 2400, MemoryEnabled: true, PromptSlug: "jakub",
	},

	// DevOps crew
	{
		Name: "Ondřej", Slug: "ondrej", CrewSlug: "devops",
		RoleTitle: "SRE Lead", AgentRole: "LEAD",
		CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC", LLMModel: "claude-sonnet-4-5",
		ToolProfile: "FULL", TimeoutSeconds: 3600, MemoryEnabled: true, PromptSlug: "ondrej",
	},
	{
		Name: "Radek", Slug: "radek", CrewSlug: "devops",
		RoleTitle: "Platform Engineer", AgentRole: "AGENT",
		CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC", LLMModel: "claude-haiku-4-5",
		ToolProfile: "CODING", TimeoutSeconds: 2400, MemoryEnabled: true, PromptSlug: "radek",
	},

	// Research crew
	{
		Name: "Lucie", Slug: "lucie", CrewSlug: "research",
		RoleTitle: "Research Director", AgentRole: "LEAD",
		CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC", LLMModel: "claude-sonnet-4-5",
		ToolProfile: "FULL", TimeoutSeconds: 3600, MemoryEnabled: true, PromptSlug: "lucie",
	},
	{
		Name: "Filip", Slug: "filip", CrewSlug: "research",
		RoleTitle: "Data Analyst", AgentRole: "AGENT",
		CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC", LLMModel: "claude-haiku-4-5",
		ToolProfile: "CODING", TimeoutSeconds: 2400, MemoryEnabled: true, PromptSlug: "filip",
	},
}
