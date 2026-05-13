"use client"

import { motion, AnimatePresence, useReducedMotion } from "motion/react"
import Image from "next/image"
import {
  Building2,
  Star,
  Check,
  Clock,
  Code2,
  Wrench,
  Megaphone,
  Calculator,
  Plus,
  type LucideIcon,
} from "lucide-react"
import { CLI_ADAPTERS, getAdapterConfig } from "@/lib/cli-adapters"
import { getAdapterBrand } from "@/lib/cli-adapter-brand"
import { CrewshipLogo } from "@/components/branding/crewship-logo"
import { getLocalizedAgentAvatar } from "@/lib/agent-avatar-locale"
import { getCrewNames } from "@/lib/agent-names-locale"
import { DIVERSE_CREW_COMPOSITIONS } from "@/lib/agent-locale-composition"

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
  Icon: LucideIcon
  agents: { name: string; slug: string; role: string; lead?: boolean }[]
  defaultModel: string
}

/**
 * Crew template metadata for the preview pane. Agents carry slug
 * strings so the right-pane avatars stay deterministic across
 * re-renders (DiceBear seeds the SVG generation from the slug). The
 * slugs here mirror the ones the backend's seed_crew_templates.go
 * uses, so a real workspace deploy + the preview show the same face
 * for each role.
 */
export const TEMPLATES: Record<CrewTemplateSlug, CrewTemplateMeta> = {
  "software-development": {
    name: "Software Dev",
    iconColor: "#5DA1FF",
    iconBg: "rgba(30, 123, 254, 0.12)",
    iconBorder: "rgba(30, 123, 254, 0.40)",
    Icon: Code2,
    agents: [
      { name: "Tech Lead", slug: "tech-lead-software-development", role: "Architect", lead: true },
      { name: "Backend Dev", slug: "backend-dev-software-development", role: "Engineer" },
      { name: "Frontend Dev", slug: "frontend-dev-software-development", role: "Engineer" },
      { name: "QA Engineer", slug: "qa-engineer-software-development", role: "Quality" },
    ],
    defaultModel: "Claude Sonnet 4.6",
  },
  "devops-sre": {
    name: "DevOps / SRE",
    iconColor: "#F472B6",
    iconBg: "rgba(244, 114, 182, 0.12)",
    iconBorder: "rgba(244, 114, 182, 0.40)",
    Icon: Wrench,
    agents: [
      { name: "SRE Lead", slug: "sre-lead-devops-sre", role: "Reliability", lead: true },
      { name: "Platform Eng", slug: "platform-eng-devops-sre", role: "Infra" },
      { name: "Security Analyst", slug: "security-analyst-devops-sre", role: "Security" },
      { name: "CI/CD Specialist", slug: "cicd-specialist-devops-sre", role: "Deploy" },
    ],
    defaultModel: "Claude Sonnet 4.6",
  },
  "content-marketing": {
    name: "Content Marketing",
    iconColor: "#C084FC",
    iconBg: "rgba(192, 132, 252, 0.12)",
    iconBorder: "rgba(192, 132, 252, 0.40)",
    Icon: Megaphone,
    agents: [
      { name: "Content Lead", slug: "content-lead-content-marketing", role: "Strategy", lead: true },
      { name: "Researcher", slug: "researcher-content-marketing", role: "Insights" },
      { name: "Copywriter", slug: "copywriter-content-marketing", role: "Writing" },
      { name: "SEO Specialist", slug: "seo-specialist-content-marketing", role: "Distribution" },
    ],
    defaultModel: "Claude Sonnet 4.6",
  },
  "accounting-finance": {
    name: "Accounting & Finance",
    iconColor: "#34D399",
    iconBg: "rgba(52, 211, 153, 0.12)",
    iconBorder: "rgba(52, 211, 153, 0.40)",
    Icon: Calculator,
    agents: [
      { name: "Finance Lead", slug: "finance-lead-accounting-finance", role: "Strategy", lead: true },
      { name: "Bookkeeper", slug: "bookkeeper-accounting-finance", role: "Ledger" },
      { name: "Tax Analyst", slug: "tax-analyst-accounting-finance", role: "Compliance" },
      { name: "Reporting", slug: "reporting-accounting-finance", role: "Analytics" },
    ],
    defaultModel: "Claude Sonnet 4.6",
  },
  blank: {
    name: "Blank crew",
    iconColor: "#A1A1AA",
    iconBg: "rgba(161, 161, 170, 0.12)",
    iconBorder: "rgba(161, 161, 170, 0.40)",
    Icon: Plus,
    agents: [{ name: "Your first agent", slug: "blank-first-agent", role: "(you'll pick)", lead: true }],
    defaultModel: "Claude Sonnet 4.6",
  },
}

interface Props {
  workspaceName: string
  crewSlug: CrewTemplateSlug | null
  mode: HandoffMode | null
  pairingPending?: boolean
  adapterKey?: string
  /**
   * Selected workspace language. Biases the avatar palette toward
   * the locale (subtle skin/hair-colour ranges) so a Czech user
   * sees a believably-local team, an English user sees the default
   * globally-mixed pool. Falls through to default for languages we
   * don't map.
   */
  language?: string
}

/** Apple-tight easing — cubic-bezier(0.16, 1, 0.3, 1). Matches the
 *  crewship-web hero reveals so the onboarding feels continuous with
 *  the marketing site. */
const ease = [0.16, 1, 0.3, 1] as const

export function OnboardingPreview({ workspaceName, crewSlug, mode, pairingPending, adapterKey, language }: Props) {
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
                className="w-10 h-10 rounded-xl flex items-center justify-center"
                style={{ background: template.iconBg, borderColor: template.iconBorder, borderWidth: 1 }}
              >
                <template.Icon className="h-5 w-5" style={{ color: template.iconColor }} />
              </div>
              <div className="min-w-0">
                <div className="font-semibold truncate tracking-tight">{template.name}</div>
                <div className="text-xs text-muted-foreground">
                  {template.agents.length} {template.agents.length === 1 ? "agent" : "agents"} · {template.defaultModel}
                </div>
              </div>
            </div>
            {/* Crew composition rules:
                - Mono-ethnic locales (Czech, Japanese, Hungarian…)
                  all four agents share the picked locale's palette
                  and draw from one name pool, with dedup. Matches
                  the real demographics of those countries.
                - Multi-ethnic locales (English / French / German /
                  Dutch / Spanish / Brazilian PT) — each slot has its
                  own (palette, name pool) from
                  DIVERSE_CREW_COMPOSITIONS. Reflects the actual
                  population mix; rendering an all-white US team
                  would be obviously wrong. */}
            <div className="space-y-2">
              {(() => {
                const effectiveLocale = language ?? "English"
                const slugs = template.agents.map((x) => x.slug)
                const diverse = DIVERSE_CREW_COMPOSITIONS[effectiveLocale]
                const monoNames = diverse ? null : getCrewNames(slugs, effectiveLocale)
                return template.agents.map((a, i) => {
                  let palette: string
                  let personName: string
                  if (diverse) {
                    const slot = diverse[i % diverse.length]
                    palette = slot.palette
                    // Index this slot's pool by a cheap slug hash so
                    // different crew templates (Dev / Ops / Marketing)
                    // surface different teammates within each group.
                    let h = 0
                    for (let k = 0; k < a.slug.length; k++)
                      h = (h * 31 + a.slug.charCodeAt(k)) >>> 0
                    personName = slot.namePool[h % slot.namePool.length]
                  } else {
                    palette = effectiveLocale
                    personName = monoNames![a.slug]
                  }
                  const avatarSrc = getLocalizedAgentAvatar(a.slug, palette)
                  return (
                  <motion.div
                    key={a.slug}
                    initial={reduce ? { opacity: 0 } : { opacity: 0, x: -8 }}
                    animate={{ opacity: 1, x: 0 }}
                    transition={{ duration: 0.32, ease, delay: 0.08 + i * 0.06 }}
                    className="flex items-center gap-2.5 text-sm"
                  >
                    <div className="relative shrink-0">
                      <Image
                        src={avatarSrc}
                        alt={personName}
                        width={32}
                        height={32}
                        className="rounded-full bg-muted ring-1 ring-border"
                        unoptimized
                      />
                      {a.lead && (
                        <span className="absolute -bottom-0.5 -right-0.5 flex h-3.5 w-3.5 items-center justify-center rounded-full bg-amber-400 text-amber-950 shadow-sm">
                          <Star className="h-2 w-2 fill-current" />
                        </span>
                      )}
                    </div>
                    <div className="flex-1 min-w-0">
                      <div className="truncate leading-tight">{personName}</div>
                      <div className="text-[11px] text-muted-foreground truncate leading-tight">
                        {a.name}
                      </div>
                    </div>
                    <span className="text-[10px] uppercase tracking-wider text-muted-foreground shrink-0">
                      {a.lead ? "Lead" : a.role}
                    </span>
                  </motion.div>
                )
              })
              })()}
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
