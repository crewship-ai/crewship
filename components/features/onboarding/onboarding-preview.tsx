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
import { CLI_ADAPTERS, getAdapterConfig, getModelLabel } from "@/lib/cli-adapters"
import { getAdapterBrand } from "@/lib/cli-adapter-brand"
import { CrewshipLogo } from "@/components/branding/crewship-logo"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"

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
}

/**
 * The model id the builtin crew templates pin for every agent they
 * deploy — `llm_model` in internal/database/builtin/crew-templates/*.yaml.
 * The preview card claims "N agents · <model>", so this has to track those
 * files: change them and change this, or the wizard promises one model and
 * deploys another.
 *
 * It was previously five hand-typed copies of the string "Claude Sonnet 4.6",
 * which matched neither the templates nor the wizard's own model picker
 * (ANTHROPIC_MODELS in lib/cli-adapters.ts) — the preview advertised a third
 * model that nothing in the product would ever run.
 *
 * Stored as an id, not a label: getModelLabel is the one place that turns
 * model ids into display names, so the preview reads the same as every other
 * surface in the app and cannot drift into its own spelling.
 */
const TEMPLATE_MODEL_ID = "claude-sonnet-5"

/** Display name for TEMPLATE_MODEL_ID — the only place this file decides
 *  what model name the preview shows. */
const TEMPLATE_MODEL_LABEL = getModelLabel(TEMPLATE_MODEL_ID)

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
  },
  blank: {
    name: "Blank crew",
    iconColor: "#A1A1AA",
    iconBg: "rgba(161, 161, 170, 0.12)",
    iconBorder: "rgba(161, 161, 170, 0.40)",
    Icon: Plus,
    agents: [{ name: "Your first agent", slug: "blank-first-agent", role: "(you'll pick)", lead: true }],
  },
}

interface Props {
  workspaceName: string
  crewSlug: CrewTemplateSlug | null
  mode: HandoffMode | null
  pairingPending?: boolean
  adapterKey?: string
  /** The model id picked on the model step, shown on the toolchain card. */
  model?: string
}

/** Apple-tight easing — cubic-bezier(0.16, 1, 0.3, 1). Matches the
 *  crewship-web hero reveals so the onboarding feels continuous with
 *  the marketing site. */
const ease = [0.16, 1, 0.3, 1] as const

export function OnboardingPreview({ workspaceName, crewSlug, mode, pairingPending, adapterKey, model }: Props) {
  const template = crewSlug ? TEMPLATES[crewSlug] : null
  const adapterCfg = adapterKey ? getAdapterConfig(adapterKey) : undefined
  const modelLabel = model ? getModelLabel(model) : ""
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

      <Connector reduce={reduce} delay={0.2} />

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
              {/* Inline styles are dynamic brand colors from the crew
                  template registry. Tailwindifying these would require
                  baking every brand palette into the tailwind safelist
                  or duplicating the registry as a class map — deferred
                  in favour of keeping the registry single-source. */}
              <div
                className="w-10 h-10 rounded-xl flex items-center justify-center border"
                style={{ background: template.iconBg, borderColor: template.iconBorder }}
              >
                <template.Icon className="h-5 w-5" style={{ color: template.iconColor }} />
              </div>
              <div className="min-w-0">
                <div className="font-semibold truncate tracking-tight">{template.name}</div>
                <div className="text-xs text-muted-foreground">
                  {template.agents.length} {template.agents.length === 1 ? "agent" : "agents"} · {TEMPLATE_MODEL_LABEL}
                </div>
              </div>
            </div>
            {/* Crew roster uses the DiceBear bottts-neutral robot
                style — the same avatar look the rest of the app
                renders for deployed agents (lib/agent-avatar.ts
                DEFAULT_AVATAR_STYLE). Robots side-step every
                demographic landmine that human faces introduce in
                a global product. Each agent's seed is its slug, so
                the same role always renders the same robot.
                Primary label is the role; no first names. */}
            <div className="space-y-2">
              {template.agents.map((a, i) => (
                <motion.div
                  key={a.slug}
                  initial={reduce ? { opacity: 0 } : { opacity: 0, x: -8 }}
                  animate={{ opacity: 1, x: 0 }}
                  transition={{ duration: 0.32, ease, delay: 0.08 + i * 0.06 }}
                  className="flex items-center gap-2.5 text-sm"
                >
                  <div className="relative shrink-0">
                    <Image
                      src={getAgentAvatarUrl(a.slug, "bottts-neutral")}
                      alt={a.name}
                      width={32}
                      height={32}
                      className="rounded-lg bg-muted ring-1 ring-border"
                      unoptimized
                    />
                    {a.lead && (
                      <span className="absolute -bottom-0.5 -right-0.5 flex h-3.5 w-3.5 items-center justify-center rounded-full bg-warn text-black shadow-sm">
                        <Star className="h-2 w-2 fill-current" />
                      </span>
                    )}
                  </div>
                  <span className="flex-1 min-w-0 truncate">{a.name}</span>
                  <span className="text-[10px] uppercase tracking-wider text-muted-foreground shrink-0">
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
            // Sized to the crew card that lands here — a header plus four
            // agent rows. As a thin strip it left the pane looking ~85% empty
            // on step one, which reads as a rendering failure rather than as
            // an empty state, and the layout jumped when the real card
            // arrived. An empty state should be a promise at the right scale.
            //
            // Only from `sm` up, though: stacked on a phone the preview is
            // below the form and off-screen while you type, so reserving a
            // card's worth of height there is pure dead scroll for a landing
            // nobody watches.
            className="flex min-h-[120px] items-center justify-center rounded-[20px] border border-dashed border-border bg-card/40 p-6 text-center text-sm text-muted-foreground sm:min-h-[248px]"
          >
            {/* Not "on the left" — stacked on a phone there is no left, and
                the picker is above this, not beside it. */}
            Your crew lands here once you pick one
          </motion.div>
        )}
      </AnimatePresence>

      {/* Toolchain card — from the model step on. This was a one-line status
          badge ("Ready to launch with Claude Code in the browser"); it is now
          a card of the same weight as the workspace and crew cards above it,
          because it is the third real object the wizard creates: the model
          credential the agents will run on. Real brand mark, the adapter's
          name, the model, and the handoff state. */}
      <AnimatePresence>
        {mode && (
          <>
            <Connector reduce={reduce} delay={0.05} />
            <motion.div
              role="status"
              aria-live="polite"
              key={`${mode}-${adapterKey ?? ""}-${pairingPending}`}
              initial={reduce ? { opacity: 0 } : { opacity: 0, y: 10, scale: 0.96 }}
              animate={{ opacity: 1, y: 0, scale: 1 }}
              exit={{ opacity: 0, scale: 0.96 }}
              transition={{ duration: 0.4, ease }}
              className="bg-card border border-border rounded-[20px] p-4 shadow-lg"
            >
              <div className="flex items-center gap-3">
                {AdapterIcon && brand && (
                  // Adapter-brand colors come from a runtime registry
                  // (lib/cli-adapter-brand.ts) keyed off the user's
                  // selection — same reason as the template icon above:
                  // forcing every brand into a Tailwind class map would
                  // duplicate the source of truth.
                  <span
                    className="w-10 h-10 rounded-xl flex items-center justify-center shrink-0 border"
                    style={{ background: brand.bg, borderColor: brand.border }}
                  >
                    <AdapterIcon className="h-5 w-5" style={{ color: brand.fg }} />
                  </span>
                )}
                <div className="flex-1 min-w-0">
                  <div className="font-semibold truncate tracking-tight">
                    {adapterCfg?.label ?? "Model"}
                  </div>
                  <div className="text-xs text-muted-foreground truncate">
                    {modelLabel ? `${modelLabel} · ` : ""}
                    {mode === "browser" ? "Chat in browser" : "Paired with your CLI"}
                  </div>
                </div>
                <span
                  className={`inline-flex shrink-0 items-center gap-1.5 rounded-full border px-2 py-0.5 text-[11px] font-medium ${
                    pairingPending
                      ? "border-warn/30 bg-warn/10 text-warn"
                      : "border-success/30 bg-success/10 text-success"
                  }`}
                >
                  {pairingPending ? <Clock className="h-3 w-3" /> : <Check className="h-3 w-3" />}
                  {pairingPending ? "Waiting for CLI" : "Ready"}
                </span>
              </div>
              <div className="mt-3 border-t border-border pt-2.5 text-xs text-muted-foreground leading-snug">
                {mode === "browser" && adapterCfg && (
                  <>
                    Ready to launch with <strong className="font-medium text-foreground">{adapterCfg.label}</strong> in the browser.
                  </>
                )}
                {mode === "browser" && !adapterCfg && "Ready to launch in the browser."}
                {mode === "cli" && pairingPending && "Waiting for your local CLI to connect…"}
                {mode === "cli" && !pairingPending && "Paired with your local CLI."}
              </div>
            </motion.div>
          </>
        )}
      </AnimatePresence>
    </div>
  )
}

/**
 * The line between two cards. A hairline used to do this, and at the scale
 * of two 20px-radius cards a 1px grey stick reads as a rendering artefact
 * rather than as a relationship. This is a short gradient stem with a dot at
 * the joint — the same "this feeds into that" gesture the crews canvas draws
 * between agents, at preview size.
 */
function Connector({ reduce, delay }: { reduce: boolean | null; delay: number }) {
  return (
    <div className="flex justify-center my-1.5" aria-hidden="true">
      <motion.div
        initial={reduce ? { opacity: 0 } : { opacity: 0, scaleY: 0 }}
        animate={{ opacity: 1, scaleY: 1 }}
        transition={{ duration: 0.35, ease, delay }}
        className="flex origin-top flex-col items-center"
      >
        <div className="h-5 w-px bg-gradient-to-b from-border via-primary/50 to-primary/70" />
        <div className="h-1.5 w-1.5 rounded-full bg-primary/70 ring-2 ring-primary/15" />
      </motion.div>
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
