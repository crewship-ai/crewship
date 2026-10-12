"use client"

import { motion, useReducedMotion } from "motion/react"
import { KeyRound } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { StatusPill } from "@/components/ui/status-pill"
import { formatRelativeShort } from "@/lib/time"
import { cn } from "@/lib/utils"
import {
  agentsMissingCredentials,
  domainMeta,
  skillIcon,
  skillName,
  skillTrust,
  sourceLabel,
  type SkillAgentRef,
  type SkillRow,
} from "./skills-model"

// One skill on the Skills page (#3033). The card answers, in this order:
// what it is (domain tile, name, vendor/slug), whether to trust it (one
// StatusPill), what it does, what it needs, who has it and whether it is
// used. Severity lives only in the pill; domains share one colour and differ by icon.

/** The skill's own icon (its domain's when it has none) in a Harbor icon tile. Every skill wears the same tint. */
export function SkillTile({
  category,
  icon,
  size = "md",
  className,
}: {
  category: string
  icon?: string | null
  size?: "sm" | "md" | "lg"
  className?: string
}) {
  const Icon = skillIcon({ icon, category })
  const box = size === "lg" ? "h-11 w-11 rounded-xl" : size === "sm" ? "h-6 w-6 rounded-md" : "h-8 w-8 rounded-lg"
  const glyph = size === "lg" ? "h-5 w-5" : size === "sm" ? "h-3 w-3" : "h-4 w-4"
  return (
    <span
      aria-hidden
      className={cn("icon-tile inline-flex shrink-0 items-center justify-center", box, className)}
    >
      <Icon className={glyph} />
    </span>
  )
}

/** Up to `max` overlapping agent faces. */
export function HolderStack({ agents, max = 5, size = 18 }: { agents: SkillAgentRef[]; max?: number; size?: 16 | 18 | 20 }) {
  const box = size === 16 ? "h-4 w-4" : size === 20 ? "h-5 w-5" : "h-[18px] w-[18px]"
  return (
    <span className="flex shrink-0 -space-x-1.5">
      {agents.slice(0, max).map((a) => (
        <AgentAvatar
          key={a.agent_id}
          seed={a.avatar_seed ?? a.agent_slug}
          style={a.avatar_style}
          avatarUrl={a.avatar_url}
          alt={a.agent_name}
          title={`${a.agent_name}${a.crew_name ? ` · ${a.crew_name}` : ""}`}
          width={size}
          height={size}
          className={cn("rounded-full bg-foreground/[0.04] ring-2 ring-card", box)}
        />
      ))}
    </span>
  )
}

export function SkillCard({ skill, index = 0, onOpen }: { skill: SkillRow; index?: number; onOpen: (id: string) => void }) {
  const reduce = useReducedMotion()
  const trust = skillTrust(skill)
  const d = domainMeta(skill.category)
  const holders = skill.installed_on ?? []
  const missing = agentsMissingCredentials(skill)
  const usage = skill.usage
  const vendor = skill.vendor && skill.vendor !== "—" ? `${skill.vendor}/` : ""
  return (
    <motion.button
      type="button"
      data-testid="skill-card"
      onClick={() => onOpen(skill.id)}
      initial={reduce ? false : { opacity: 0, y: 8 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.3, ease: [0.22, 1, 0.36, 1], delay: Math.min(index, 12) * 0.025 }}
      className="group card-interactive card-hover flex min-w-0 flex-col gap-2.5 border border-border bg-card px-4 py-3.5 text-left"
    >
      <div className="flex min-w-0 items-start gap-2.5">
        <SkillTile category={skill.category} icon={skill.icon} className="transition-transform duration-300 group-hover:-rotate-[4deg] group-hover:scale-[1.08]" />
        <div className="min-w-0 flex-1">
          <div className="truncate text-body font-semibold leading-[18px] text-foreground">{skillName(skill)}</div>
          <div className="truncate font-mono text-micro text-muted-foreground-soft">
            {vendor}
            {skill.slug}
          </div>
        </div>
        <StatusPill tone={trust.tone} label={trust.label} />
      </div>
      <p className="line-clamp-2 min-h-[34px] text-label leading-[17px] text-muted-foreground">
        {skill.description || "No description."}
      </p>
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="inline-flex items-center gap-1 rounded-full border border-border px-2 py-px text-micro text-muted-foreground">
          <d.icon className="h-3 w-3" aria-hidden />
          {d.label}
        </span>
        <span className="inline-flex items-center rounded-full border border-border px-2 py-px text-micro text-muted-foreground">
          {sourceLabel(skill.source)}
        </span>
        {(skill.needs_credentials ?? []).map((c) => (
          <span
            key={c}
            title="Needs this credential"
            className="inline-flex items-center gap-1 rounded-full border border-dashed border-border px-2 py-px font-mono text-micro text-muted-foreground"
          >
            <KeyRound className="h-3 w-3" aria-hidden />
            {c}
          </span>
        ))}
      </div>
      <div className="mt-auto flex items-center gap-2 border-t border-border/60 pt-2.5 text-micro text-muted-foreground">
        {holders.length > 0 ? (
          <>
            <HolderStack agents={holders} />
            <span className="tabular-nums">
              {holders.length} {holders.length === 1 ? "agent" : "agents"}
            </span>
          </>
        ) : (
          <span className="text-muted-foreground-soft">Not on any agent</span>
        )}
        <span className="flex-1" />
        {missing.length > 0 && (
          <span className="text-warn" title={`${missing.length} agent${missing.length === 1 ? "" : "s"} missing a credential`}>
            <KeyRound className="h-3.5 w-3.5" aria-label="Missing credential" />
          </span>
        )}
        {usage && usage.uses_total > 0 ? (
          <span className="font-mono tabular-nums">
            {usage.uses_7d} uses · {formatRelativeShort(usage.last_used_at)}
          </span>
        ) : (
          <span className="font-mono text-muted-foreground-soft">never used</span>
        )}
      </div>
    </motion.button>
  )
}
