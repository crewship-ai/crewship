import { getModelLabel } from "@/lib/cli-adapters"

/**
 * The builtin crew templates the setup wizard offers, mirrored from
 * internal/database/builtin/crew-templates/*.yaml: slug, name, description,
 * icon and colour are the YAML's own, so the tile a person picks looks like
 * the crew they get. Agent slugs match the seeder's, so the avatars match too.
 *
 * Moved here from the retired live preview (Harbor 2, #3409): the approved
 * layout has no preview pane, and the crew step's "What you get" panel reads
 * the same catalogue.
 */
export type CrewTemplateSlug =
  | "software-development"
  | "devops-sre"
  | "content-marketing"
  | "accounting-finance"
  | "blank"

export type HandoffMode = "browser" | "cli"

export interface CrewTemplateMeta {
  name: string
  description: string
  /** A CREW_ICONS id (lib/crew-icons.ts), as in the template YAML. */
  icon: string
  /** A palette id, as in the template YAML. */
  color: string
  agents: { name: string; slug: string; role: string; lead?: boolean }[]
}

/**
 * The model id the builtin templates pin for every agent — `llm_model` in the
 * template YAML. Stored as an id, never a label: getModelLabel is the one place
 * that turns ids into names, so this cannot advertise a model nothing runs.
 */
export const TEMPLATE_MODEL_ID = "claude-sonnet-5"

export const TEMPLATE_MODEL_LABEL = getModelLabel(TEMPLATE_MODEL_ID)

export const TEMPLATES: Record<CrewTemplateSlug, CrewTemplateMeta> = {
  "software-development": {
    name: "Software Development",
    description: "Full dev team: Tech Lead, Backend Dev, Frontend Dev, QA Engineer",
    icon: "code",
    color: "blue",
    agents: [
      { name: "Tech Lead", slug: "tech-lead-software-development", role: "Architect", lead: true },
      { name: "Backend Dev", slug: "backend-dev-software-development", role: "Engineer" },
      { name: "Frontend Dev", slug: "frontend-dev-software-development", role: "Engineer" },
      { name: "QA Engineer", slug: "qa-engineer-software-development", role: "Quality" },
    ],
  },
  "devops-sre": {
    name: "DevOps / SRE",
    description: "Infrastructure team: SRE Lead, Platform Engineer, Security Analyst, CI/CD Specialist",
    icon: "wrench",
    color: "rose",
    agents: [
      { name: "SRE Lead", slug: "sre-lead-devops-sre", role: "Reliability", lead: true },
      { name: "Platform Eng", slug: "platform-eng-devops-sre", role: "Infra" },
      { name: "Security Analyst", slug: "security-analyst-devops-sre", role: "Security" },
      { name: "CI/CD Specialist", slug: "cicd-specialist-devops-sre", role: "Deploy" },
    ],
  },
  "content-marketing": {
    name: "Content Marketing",
    description: "Marketing team: Content Lead, Researcher, Copywriter, SEO Specialist",
    icon: "megaphone",
    color: "violet",
    agents: [
      { name: "Content Lead", slug: "content-lead-content-marketing", role: "Strategy", lead: true },
      { name: "Researcher", slug: "researcher-content-marketing", role: "Insights" },
      { name: "Copywriter", slug: "copywriter-content-marketing", role: "Writing" },
      { name: "SEO Specialist", slug: "seo-specialist-content-marketing", role: "Distribution" },
    ],
  },
  "accounting-finance": {
    name: "Accounting & Finance",
    description: "Finance team: Finance Lead, Bookkeeper, Tax Analyst, Reporting Specialist",
    icon: "chart",
    color: "emerald",
    agents: [
      { name: "Finance Lead", slug: "finance-lead-accounting-finance", role: "Strategy", lead: true },
      { name: "Bookkeeper", slug: "bookkeeper-accounting-finance", role: "Ledger" },
      { name: "Tax Analyst", slug: "tax-analyst-accounting-finance", role: "Compliance" },
      { name: "Reporting", slug: "reporting-accounting-finance", role: "Analytics" },
    ],
  },
  blank: {
    name: "Start blank",
    description: "One agent to start with; you add the rest.",
    icon: "plus",
    color: "blue",
    agents: [{ name: "Your first agent", slug: "blank-first-agent", role: "You pick the role", lead: true }],
  },
}

/** The four team templates, in the order the step shows them. Blank is offered on its own. */
export const TEAM_TEMPLATE_SLUGS: Exclude<CrewTemplateSlug, "blank">[] = [
  "software-development",
  "devops-sre",
  "content-marketing",
  "accounting-finance",
]
