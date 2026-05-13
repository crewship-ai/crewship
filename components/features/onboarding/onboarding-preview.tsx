"use client"

import { motion, AnimatePresence, useReducedMotion } from "motion/react"
import { Building2, Star, User, Check, Clock } from "lucide-react"
import { CLI_ADAPTERS, getAdapterConfig } from "@/lib/cli-adapters"
import { getAdapterBrand } from "@/lib/cli-adapter-brand"
import { CrewshipLogo } from "@/components/branding/crewship-logo"

/**
 * OnboardingPreview — right pane of the split-screen Variant D
 * onboarding. Animates the workspace + crew + adapter cards into view
 * with the same staggered fade-up pattern the crewship-web hero uses
 * (motion/react, Apple-tight easing, ~350ms per element). Respects
 * prefers-reduced-motion via useReducedMotion().
 *
 * Tile sequence:
 *   1) Workspace card (always visible, name updates live)
 *   2) Crew card (empty state → filled with agents on template pick)
 *   3) Adapter handoff badge (browser or CLI mode, step 3 only)
 *
 * The component is layout-agnostic: parent controls placement
 * (split-screen on lg, stacked under form on sm).
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
  iconColor: string
  iconBg: string
  iconBorder: string
  emoji: string
  agents: { name: string; role: string; lead?: boolean }[]
  defaultModel: string
}

const TEMPLATES: Record<CrewTemplateSlug, CrewTemplateMeta> = {
  "software-development": {
    name: "Software Dev",
    iconColor: "#5DA1FF",
    iconBg: "rgba(30, 123, 254, 0.12)",
    iconBorder: "rgba(30, 123, 254, 0.40)",
    emoji: "💻",
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
    iconColor: "#F472B6",
    iconBg: "rgba(244, 114, 182, 0.12)",
    iconBorder: "rgba(244, 114, 182, 0.40)",
    emoji: "🔧",
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
    iconColor: "#C084FC",
    iconBg: "rgba(192, 132, 252, 0.12)",
    iconBorder: "rgba(192, 132, 252, 0.40)",
    emoji: "📢",
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
    iconColor: "#34D399",
    iconBg: "rgba(52, 211, 153, 0.12)",
    iconBorder: "rgba(52, 211, 153, 0.40)",
    emoji: "🧮",
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
    iconColor: "#A1A1AA",
    iconBg: "rgba(161, 161, 170, 0.12)",
    iconBorder: "rgba(161, 161, 170, 0.40)",
    emoji: "➕",
    agents: [{ name: "Your first agent", role: "(you'll pick)", lead: true }],
    defaultModel: "Claude Sonnet 4.6",
  },
}

interface Props {
  workspaceName: string
  crewSlug: CrewTemplateSlug | null
  mode: HandoffMode | null
  pairingPending?: boolean
  adapterKey?: string
}

/** Apple-tight easing — cubic-bezier(0.16, 1, 0.3, 1). Matches the
 *  crewship-web hero reveals so the onboarding feels continuous with
 *  the marketing site. */
const ease = [0.16, 1, 0.3, 1] as const

export function OnboardingPreview({ workspaceName, crewSlug, mode, pairingPending, adapterKey }: Props) {
  const template = crewSlug ? TEMPLATES[crewSlug] : null
  const adapterCfg = adapterKey ? getAdapterConfig(adapterKey) : undefined
  const brand = adapterKey ? getAdapterBrand(adapterKey) : undefined
  const reduce = useReducedMotion()
  const AdapterIcon = adapterCfg?.icon

  return (
    <div className="w-full max-w-md mx-auto">
      <motion.div
        initial={reduce ? { opacity: 0 } : { opacity: 0, y: 8 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.4, ease }}
        className="text-[11px] uppercase tracking-[0.18em] text-muted-foreground mb-4 flex items-center gap-2"
      >
        <CrewshipLogo className="h-3.5 w-3.5 text-primary" />
        Live preview
      </motion.div>

      {/* Workspace card — always present, fades in on mount */}
      <motion.div
        initial={reduce ? { opacity: 0 } : { opacity: 0, y: 14, scale: 0.98 }}
        animate={{ opacity: 1, y: 0, scale: 1 }}
        transition={{ duration: 0.55, ease, delay: 0.05 }}
        className="bg-card border border-border rounded-[20px] p-4 flex items-center gap-3 shadow-lg"
      >
        <div className="w-10 h-10 rounded-xl bg-gradient-to-br from-[#1B75FE] to-[#2B90FF] flex items-center justify-center text-white shadow-md shadow-primary/30">
          <Building2 className="h-5 w-5" />
        </div>
        <div className="flex-1 min-w-0">
          <div className="font-semibold truncate tracking-tight">
            {workspaceName || <span className="text-muted-foreground italic">unnamed workspace</span>}
          </div>
          <div className="text-xs text-muted-foreground">Workspace</div>
        </div>
      </motion.div>

      {/* connector */}
      <div className="flex justify-center my-2">
        <motion.div
          initial={reduce ? { opacity: 0 } : { opacity: 0, scaleY: 0 }}
          animate={{ opacity: 1, scaleY: 1 }}
          transition={{ duration: 0.35, ease, delay: 0.2 }}
          style={{ originY: 0 }}
          className="w-px h-6 bg-border"
        />
      </div>

      {/* Crew card — empty state vs filled, animated transition between */}
      <AnimatePresence mode="wait">
        {template ? (
          <motion.div
            key={crewSlug}
            initial={reduce ? { opacity: 0 } : { opacity: 0, y: 14, scale: 0.96 }}
            animate={{ opacity: 1, y: 0, scale: 1 }}
            exit={reduce ? { opacity: 0 } : { opacity: 0, y: -8, scale: 0.98 }}
            transition={{ duration: 0.4, ease }}
            className="bg-card border border-border rounded-[20px] p-4 shadow-lg"
          >
            <div className="flex items-center gap-3 mb-3 pb-3 border-b border-border">
              <div
                className="w-10 h-10 rounded-xl flex items-center justify-center text-lg"
                style={{ background: template.iconBg, borderColor: template.iconBorder, borderWidth: 1 }}
              >
                <span style={{ color: template.iconColor }}>{template.emoji}</span>
              </div>
              <div className="min-w-0">
                <div className="font-semibold truncate tracking-tight">{template.name}</div>
                <div className="text-xs text-muted-foreground">
                  {template.agents.length} {template.agents.length === 1 ? "agent" : "agents"} · {template.defaultModel}
                </div>
              </div>
            </div>
            <div className="space-y-2">
              {template.agents.map((a, i) => (
                <motion.div
                  key={a.name}
                  initial={reduce ? { opacity: 0 } : { opacity: 0, x: -8 }}
                  animate={{ opacity: 1, x: 0 }}
                  transition={{ duration: 0.32, ease, delay: 0.08 + i * 0.06 }}
                  className="flex items-center gap-2 text-sm"
                >
                  <div className="w-7 h-7 rounded-full bg-muted flex items-center justify-center text-muted-foreground">
                    {a.lead ? <Star className="h-3.5 w-3.5 text-amber-400" /> : <User className="h-3.5 w-3.5" />}
                  </div>
                  <span className="flex-1 min-w-0 truncate">{a.name}</span>
                  <span className="text-[10px] uppercase tracking-wider text-muted-foreground">
                    {a.lead ? "Lead" : a.role}
                  </span>
                </motion.div>
              ))}
            </div>
          </motion.div>
        ) : (
          <motion.div
            key="empty"
            initial={reduce ? { opacity: 0 } : { opacity: 0, y: 10 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0 }}
            transition={{ duration: 0.35, ease, delay: 0.25 }}
            className="bg-card/40 border border-dashed border-border rounded-[20px] p-6 text-center text-sm text-muted-foreground"
          >
            Pick a crew template ↑
          </motion.div>
        )}
      </AnimatePresence>

      {/* Adapter handoff badge — appears in step 3 only */}
      <AnimatePresence>
        {mode && (
          <motion.div
            key={`${mode}-${adapterKey ?? ""}-${pairingPending}`}
            initial={reduce ? { opacity: 0 } : { opacity: 0, y: 10, scale: 0.96 }}
            animate={{ opacity: 1, y: 0, scale: 1 }}
            exit={{ opacity: 0, scale: 0.96 }}
            transition={{ duration: 0.4, ease }}
            className={`mt-4 rounded-[20px] p-3 text-xs flex items-center gap-3 shadow-md border ${
              pairingPending
                ? "bg-amber-500/10 border-amber-500/30 text-amber-700 dark:text-amber-300"
                : "bg-emerald-500/10 border-emerald-500/30 text-emerald-700 dark:text-emerald-300"
            }`}
          >
            {AdapterIcon && brand && (
              <span
                className="w-7 h-7 rounded-lg flex items-center justify-center shrink-0"
                style={{ background: brand.bg, borderColor: brand.border, borderWidth: 1 }}
              >
                <AdapterIcon className="h-4 w-4" style={{ color: brand.fg }} />
              </span>
            )}
            <span className="flex-1 min-w-0 leading-snug">
              {mode === "browser" && adapterCfg && (
                <>
                  Ready to launch with <strong className="font-semibold">{adapterCfg.label}</strong> in the browser
                </>
              )}
              {mode === "browser" && !adapterCfg && "Ready to launch in the browser"}
              {mode === "cli" && pairingPending && (
                <>
                  Waiting for your local CLI to connect…
                </>
              )}
              {mode === "cli" && !pairingPending && (
                <>
                  Paired with your local CLI
                </>
              )}
            </span>
            {pairingPending ? (
              <Clock className="h-4 w-4 shrink-0" />
            ) : (
              <Check className="h-4 w-4 shrink-0" />
            )}
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  )
}

/** Returns the brand colors for an adapter — re-exported here so the
 *  onboarding page can render adapter chips with the same fills the
 *  preview uses. */
export function brandFor(adapterKey: string) {
  return getAdapterBrand(adapterKey)
}

// Re-export so parent page can reference the same registry.
export { CLI_ADAPTERS }
