"use client"

import { Building2, Code2, Wrench, Megaphone, Calculator, Plus, Star, User, Check, Clock } from "lucide-react"
import type { LucideIcon } from "lucide-react"

/**
 * OnboardingPreview is the right-pane "live preview" of the split-screen
 * Variant D onboarding wizard. Receives workspace name, the picked crew
 * template (or null for empty state), and the chosen handoff mode.
 * Animates new sections in via the grow-in keyframe defined locally so
 * the user sees the workspace materialize as they make choices.
 *
 * Mobile note: the parent page stacks this BELOW the form on <lg
 * breakpoints; the component itself is layout-agnostic.
 */

export type CrewTemplateSlug =
  | "software-development"
  | "devops-sre"
  | "content-marketing"
  | "accounting-finance"
  | "blank"

export type HandoffMode = "browser" | "cli"

interface CrewTemplateMeta {
  name: string
  icon: LucideIcon
  iconBg: string
  iconBorder: string
  agents: { name: string; role: string; lead?: boolean }[]
  defaultModel: string
}

const TEMPLATES: Record<CrewTemplateSlug, CrewTemplateMeta> = {
  "software-development": {
    name: "Software Dev",
    icon: Code2,
    iconBg: "bg-blue-500/15",
    iconBorder: "border-blue-500/30",
    agents: [
      { name: "Tech Lead", role: "Architect", lead: true },
      { name: "Backend Dev", role: "Engineer" },
      { name: "Frontend Dev", role: "Engineer" },
      { name: "QA Engineer", role: "Quality" },
    ],
    defaultModel: "Claude Sonnet 4.6",
  },
  "devops-sre": {
    name: "DevOps / SRE",
    icon: Wrench,
    iconBg: "bg-rose-500/15",
    iconBorder: "border-rose-500/30",
    agents: [
      { name: "SRE Lead", role: "Reliability", lead: true },
      { name: "Platform Eng", role: "Infra" },
      { name: "Security Analyst", role: "Security" },
      { name: "CI/CD Specialist", role: "Deploy" },
    ],
    defaultModel: "Claude Sonnet 4.6",
  },
  "content-marketing": {
    name: "Content Marketing",
    icon: Megaphone,
    iconBg: "bg-violet-500/15",
    iconBorder: "border-violet-500/30",
    agents: [
      { name: "Content Lead", role: "Strategy", lead: true },
      { name: "Researcher", role: "Insights" },
      { name: "Copywriter", role: "Writing" },
      { name: "SEO Specialist", role: "Distribution" },
    ],
    defaultModel: "Claude Sonnet 4.6",
  },
  "accounting-finance": {
    name: "Accounting & Finance",
    icon: Calculator,
    iconBg: "bg-emerald-500/15",
    iconBorder: "border-emerald-500/30",
    agents: [
      { name: "Finance Lead", role: "Strategy", lead: true },
      { name: "Bookkeeper", role: "Ledger" },
      { name: "Tax Analyst", role: "Compliance" },
      { name: "Reporting", role: "Analytics" },
    ],
    defaultModel: "Claude Sonnet 4.6",
  },
  blank: {
    name: "Blank crew",
    icon: Plus,
    iconBg: "bg-muted",
    iconBorder: "border-border",
    agents: [{ name: "Your first agent", role: "(you'll pick)", lead: true }],
    defaultModel: "Claude Sonnet 4.6",
  },
}

interface Props {
  workspaceName: string
  crewSlug: CrewTemplateSlug | null
  mode: HandoffMode | null
  pairingPending?: boolean
  adapterLabel?: string
}

export function OnboardingPreview({ workspaceName, crewSlug, mode, pairingPending, adapterLabel }: Props) {
  const template = crewSlug ? TEMPLATES[crewSlug] : null

  return (
    <div className="w-full max-w-md mx-auto">
      <div className="text-xs uppercase tracking-wider text-muted-foreground mb-4">Live preview</div>

      {/* workspace */}
      <div className="bg-card border border-border rounded-xl p-4 flex items-center gap-3">
        <div className="w-10 h-10 rounded-lg bg-gradient-to-br from-primary/80 to-purple-500/80 flex items-center justify-center text-primary-foreground">
          <Building2 className="h-5 w-5" />
        </div>
        <div className="flex-1 min-w-0">
          <div className="font-semibold truncate">{workspaceName || "(unnamed workspace)"}</div>
          <div className="text-xs text-muted-foreground">Workspace</div>
        </div>
      </div>

      {/* connector */}
      <div className="flex justify-center my-2">
        <div className="w-px h-6 bg-border" />
      </div>

      {/* crew */}
      {template ? (
        <div
          key={crewSlug ?? "none"}
          className="bg-card border border-border rounded-xl p-4 animate-grow-in"
        >
          <div className="flex items-center gap-3 mb-3 pb-3 border-b border-border">
            <div
              className={`w-10 h-10 rounded-lg ${template.iconBg} border ${template.iconBorder} flex items-center justify-center`}
            >
              <template.icon className="h-5 w-5" />
            </div>
            <div className="min-w-0">
              <div className="font-semibold truncate">{template.name}</div>
              <div className="text-xs text-muted-foreground">
                {template.agents.length} {template.agents.length === 1 ? "agent" : "agents"} · {template.defaultModel}
              </div>
            </div>
          </div>
          <div className="space-y-2">
            {template.agents.map((a) => (
              <div key={a.name} className="flex items-center gap-2 text-sm">
                <div className="w-6 h-6 rounded-full bg-muted flex items-center justify-center text-muted-foreground">
                  {a.lead ? <Star className="h-3 w-3" /> : <User className="h-3 w-3" />}
                </div>
                <span className="flex-1 min-w-0 truncate">{a.name}</span>
                <span className="text-[10px] uppercase tracking-wider text-muted-foreground">
                  {a.lead ? "Lead" : a.role}
                </span>
              </div>
            ))}
          </div>
        </div>
      ) : (
        <div className="bg-card border border-dashed border-border rounded-xl p-6 text-center text-sm text-muted-foreground">
          Pick a crew template ↑
        </div>
      )}

      {/* adapter / handoff */}
      {mode && (
        <div
          key={`mode-${mode}-${adapterLabel ?? ""}`}
          className={`mt-4 rounded-xl p-3 text-xs flex items-center gap-2 animate-grow-in ${
            pairingPending
              ? "bg-amber-500/10 border border-amber-500/30 text-amber-700 dark:text-amber-400"
              : "bg-emerald-500/10 border border-emerald-500/30 text-emerald-700 dark:text-emerald-400"
          }`}
        >
          {pairingPending ? <Clock className="h-4 w-4" /> : <Check className="h-4 w-4" />}
          <span>
            {mode === "browser" && adapterLabel && `Ready to launch with ${adapterLabel} in browser`}
            {mode === "browser" && !adapterLabel && "Ready to launch in browser"}
            {mode === "cli" && pairingPending && "Waiting for your local CLI to pair…"}
            {mode === "cli" && !pairingPending && "Paired with your local CLI"}
          </span>
        </div>
      )}

      <style jsx global>{`
        @keyframes grow-in {
          from {
            opacity: 0;
            transform: scale(0.95);
          }
          to {
            opacity: 1;
            transform: scale(1);
          }
        }
        .animate-grow-in {
          animation: grow-in 0.35s cubic-bezier(0.2, 0.9, 0.3, 1.2);
        }
      `}</style>
    </div>
  )
}
