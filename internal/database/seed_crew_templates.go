package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// CrewTemplateAgent defines an agent within a crew template.
type CrewTemplateAgent struct {
	Name         string   `json:"name"`
	Slug         string   `json:"slug"`
	RoleTitle    string   `json:"role_title"`
	AgentRole    string   `json:"agent_role"`
	CLIAdapter   string   `json:"cli_adapter"`
	LLMProvider  string   `json:"llm_provider"`
	LLMModel     string   `json:"llm_model"`
	ToolProfile  string   `json:"tool_profile"`
	SystemPrompt string   `json:"system_prompt"`
	Skills       []string `json:"skills,omitempty"`
}

var builtinCrewTemplates = []struct {
	name        string
	slug        string
	description string
	icon        string
	color       string
	category    string
	agents      []CrewTemplateAgent
}{
	{
		name: "Software Development", slug: "software-development",
		description: "Full dev team: Tech Lead, Backend Dev, Frontend Dev, QA Engineer",
		icon: "💻", color: "#3B82F6", category: "ENGINEERING",
		agents: []CrewTemplateAgent{
			{
				Name: "Tech Lead", Slug: "tech-lead", RoleTitle: "Technical Architect",
				AgentRole: "LEAD", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "FULL",
				SystemPrompt: "You are the Technical Architect and Lead of this development crew. You coordinate work across team members, review architectural decisions, and ensure code quality standards are met. Break down complex tasks and delegate to specialists.",
				Skills: []string{"code-review", "architecture"},
			},
			{
				Name: "Backend Dev", Slug: "backend-dev", RoleTitle: "Backend Engineer",
				AgentRole: "AGENT", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "CODING",
				SystemPrompt: "You are a Backend Engineer. You implement server-side features, API endpoints, database queries, and write comprehensive tests. Follow TDD — write tests first, then implement.",
				Skills: []string{"coding-assistant"},
			},
			{
				Name: "Frontend Dev", Slug: "frontend-dev", RoleTitle: "Frontend Engineer",
				AgentRole: "AGENT", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "CODING",
				SystemPrompt: "You are a Frontend Engineer. You build user interfaces, implement responsive designs, and create accessible components. Use modern frameworks and follow established UI patterns.",
				Skills: []string{"coding-assistant"},
			},
			{
				Name: "QA Engineer", Slug: "qa-engineer", RoleTitle: "Quality Assurance",
				AgentRole: "AGENT", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "CODING",
				SystemPrompt: "You are a QA Engineer. You write and maintain test suites, perform code reviews focused on correctness and security, and ensure test coverage for critical paths.",
				Skills: []string{"testing-specialist", "code-reviewer"},
			},
		},
	},
	{
		name: "DevOps / SRE", slug: "devops-sre",
		description: "Infrastructure team: SRE Lead, Platform Engineer, Security Analyst, CI/CD Specialist",
		icon: "🔧", color: "#EF4444", category: "ENGINEERING",
		agents: []CrewTemplateAgent{
			{
				Name: "SRE Lead", Slug: "sre-lead", RoleTitle: "Site Reliability Lead",
				AgentRole: "LEAD", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "FULL",
				SystemPrompt: "You are the SRE Lead. You oversee infrastructure reliability, coordinate incident response, and ensure systems meet SLA targets. Delegate tasks to platform, security, and CI/CD specialists.",
			},
			{
				Name: "Platform Engineer", Slug: "platform-eng", RoleTitle: "Platform Engineer",
				AgentRole: "AGENT", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "CODING",
				SystemPrompt: "You are a Platform Engineer. You manage container infrastructure, Kubernetes clusters, networking, and cloud resources. Write Infrastructure-as-Code (Terraform, Helm).",
				Skills: []string{"devops-helper"},
			},
			{
				Name: "Security Analyst", Slug: "security-analyst", RoleTitle: "Security Analyst",
				AgentRole: "AGENT", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "MINIMAL",
				SystemPrompt: "You are a Security Analyst. You audit codebases for vulnerabilities, review credential handling, verify auth flows, and ensure compliance with security standards (OWASP, SOC2).",
				Skills: []string{"security-auditor"},
			},
			{
				Name: "CI/CD Specialist", Slug: "cicd-specialist", RoleTitle: "CI/CD Engineer",
				AgentRole: "AGENT", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "CODING",
				SystemPrompt: "You are a CI/CD Specialist. You build and maintain deployment pipelines, automate testing workflows, manage build systems, and ensure fast, reliable deployments.",
				Skills: []string{"devops-helper"},
			},
		},
	},
	{
		name: "Content Marketing", slug: "content-marketing",
		description: "Marketing team: Content Lead, Researcher, Copywriter, SEO Specialist",
		icon: "📈", color: "#8B5CF6", category: "MARKETING",
		agents: []CrewTemplateAgent{
			{
				Name: "Content Lead", Slug: "content-lead", RoleTitle: "Content Strategy Lead",
				AgentRole: "LEAD", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "FULL",
				SystemPrompt: "You are the Content Strategy Lead. You plan content calendars, coordinate research and writing tasks, ensure brand consistency, and review all published content for quality.",
			},
			{
				Name: "Researcher", Slug: "researcher", RoleTitle: "Market Researcher",
				AgentRole: "AGENT", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "MINIMAL",
				SystemPrompt: "You are a Market Researcher. You gather competitive intelligence, analyze market trends, collect data for content briefs, and provide insights to guide content strategy.",
			},
			{
				Name: "Copywriter", Slug: "copywriter", RoleTitle: "Senior Copywriter",
				AgentRole: "AGENT", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "CODING",
				SystemPrompt: "You are a Senior Copywriter. You write blog posts, landing pages, email sequences, and social media content. Follow the brand voice guide and optimize for readability.",
			},
			{
				Name: "SEO Specialist", Slug: "seo-specialist", RoleTitle: "SEO Analyst",
				AgentRole: "AGENT", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "MINIMAL",
				SystemPrompt: "You are an SEO Analyst. You perform keyword research, optimize content for search engines, analyze rankings, and recommend technical SEO improvements.",
			},
		},
	},
	{
		name: "Accounting & Finance", slug: "accounting-finance",
		description: "Finance team: Finance Lead, Bookkeeper, Tax Analyst, Reporting Specialist",
		icon: "📊", color: "#10B981", category: "BUSINESS",
		agents: []CrewTemplateAgent{
			{
				Name: "Finance Lead", Slug: "finance-lead", RoleTitle: "Finance Director",
				AgentRole: "LEAD", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "FULL",
				SystemPrompt: "You are the Finance Director. You oversee all financial operations, coordinate bookkeeping, tax preparation, and financial reporting. Ensure accuracy and compliance with accounting standards.",
			},
			{
				Name: "Bookkeeper", Slug: "bookkeeper", RoleTitle: "Bookkeeper",
				AgentRole: "AGENT", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "CODING",
				SystemPrompt: "You are a Bookkeeper. You process invoices, categorize transactions, reconcile accounts, and maintain accurate financial records. Work with spreadsheets and accounting data files.",
			},
			{
				Name: "Tax Analyst", Slug: "tax-analyst", RoleTitle: "Tax Analyst",
				AgentRole: "AGENT", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "MINIMAL",
				SystemPrompt: "You are a Tax Analyst. You prepare tax returns, analyze tax obligations, identify deductions and credits, and ensure compliance with local and international tax regulations.",
			},
			{
				Name: "Reporting Specialist", Slug: "reporting-spec", RoleTitle: "Financial Reporting",
				AgentRole: "AGENT", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "CODING",
				SystemPrompt: "You are a Financial Reporting Specialist. You create financial statements, dashboards, cash flow reports, and budget analyses. Present data clearly for stakeholders.",
			},
		},
	},
	{
		name: "Customer Support", slug: "customer-support",
		description: "Support team: Support Lead, Tier 1 Agent, Tier 2 Specialist, Knowledge Manager",
		icon: "🎧", color: "#F59E0B", category: "OPERATIONS",
		agents: []CrewTemplateAgent{
			{
				Name: "Support Lead", Slug: "support-lead", RoleTitle: "Support Manager",
				AgentRole: "LEAD", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "FULL",
				SystemPrompt: "You are the Support Manager. You triage incoming issues, coordinate between support tiers, escalate critical problems, and ensure customer satisfaction targets are met.",
			},
			{
				Name: "Tier 1 Agent", Slug: "tier1-agent", RoleTitle: "Support Agent",
				AgentRole: "AGENT", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "MINIMAL",
				SystemPrompt: "You are a Tier 1 Support Agent. You handle initial customer inquiries, answer common questions using the knowledge base, and escalate complex issues to Tier 2.",
			},
			{
				Name: "Tier 2 Specialist", Slug: "tier2-specialist", RoleTitle: "Technical Support",
				AgentRole: "AGENT", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "CODING",
				SystemPrompt: "You are a Tier 2 Technical Support Specialist. You investigate complex technical issues, reproduce bugs, provide detailed solutions, and work with engineering when needed.",
			},
			{
				Name: "Knowledge Manager", Slug: "knowledge-mgr", RoleTitle: "Knowledge Base Manager",
				AgentRole: "AGENT", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "CODING",
				SystemPrompt: "You are the Knowledge Base Manager. You maintain FAQ articles, update help documentation, create troubleshooting guides, and analyze support trends to proactively create content.",
			},
		},
	},
	{
		name: "Research & Analysis", slug: "research-analysis",
		description: "Research team: Research Lead, Data Collector, Analyst, Report Writer",
		icon: "🔍", color: "#06B6D4", category: "RESEARCH",
		agents: []CrewTemplateAgent{
			{
				Name: "Research Lead", Slug: "research-lead", RoleTitle: "Research Director",
				AgentRole: "LEAD", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "FULL",
				SystemPrompt: "You are the Research Director. You define research objectives, coordinate data collection and analysis tasks, ensure methodology rigor, and synthesize findings into actionable insights.",
			},
			{
				Name: "Data Collector", Slug: "data-collector", RoleTitle: "Data Acquisition Specialist",
				AgentRole: "AGENT", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "CODING",
				SystemPrompt: "You are a Data Acquisition Specialist. You gather data from web sources, APIs, documents, and databases. Clean and structure raw data for analysis. Handle scraping and ETL tasks.",
			},
			{
				Name: "Analyst", Slug: "analyst", RoleTitle: "Data Analyst",
				AgentRole: "AGENT", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "CODING",
				SystemPrompt: "You are a Data Analyst. You analyze datasets, identify patterns and trends, perform statistical analysis, and create visualizations. Use Python, pandas, and data science tools.",
			},
			{
				Name: "Report Writer", Slug: "report-writer", RoleTitle: "Research Writer",
				AgentRole: "AGENT", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC",
				LLMModel: "claude-sonnet-4-20250514", ToolProfile: "CODING",
				SystemPrompt: "You are a Research Writer. You transform analytical findings into clear, well-structured reports, executive summaries, and presentations. Ensure data is presented accurately and accessibly.",
			},
		},
	},
}

// SeedBuiltinCrewTemplates inserts bundled crew templates if they don't exist.
func SeedBuiltinCrewTemplates(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	for _, bt := range builtinCrewTemplates {
		var exists bool
		err := db.QueryRowContext(ctx,
			`SELECT 1 FROM crew_templates WHERE slug = ? AND is_builtin = 1`, bt.slug).Scan(&exists)
		if err == nil {
			continue
		}
		if err != sql.ErrNoRows {
			return fmt.Errorf("check crew template %s: %w", bt.slug, err)
		}

		agentsJSON, err := json.Marshal(bt.agents)
		if err != nil {
			return fmt.Errorf("marshal agents for %s: %w", bt.slug, err)
		}

		id := generateSeedID("ct")
		now := time.Now().UTC().Format(time.RFC3339)
		if _, err := db.ExecContext(ctx, `
			INSERT OR IGNORE INTO crew_templates (id, name, slug, description, icon, color, category, agents_json, is_builtin, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			id, bt.name, bt.slug, bt.description, bt.icon, bt.color, bt.category, string(agentsJSON), now, now); err != nil {
			logger.Warn("failed to seed crew template", "slug", bt.slug, "error", err)
		}
	}
	return nil
}
