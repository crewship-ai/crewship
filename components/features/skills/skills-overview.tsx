"use client"

import { useMemo } from "react"
import { motion, useReducedMotion } from "motion/react"
import {
  Activity,
  AlertTriangle,
  CheckCircle2,
  CircleDashed,
  KeyRound,
  LayoutGrid,
  List,
  MessageSquare,
  Plus,
  Search,
  Sparkles,
  Users,
  X,
  Zap,
  type LucideIcon,
} from "lucide-react"
import Link from "next/link"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Button } from "@/components/ui/button"
import { CrewIcon } from "@/components/ui/crew-icon"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { Skeleton } from "@/components/ui/skeleton"
import { StatusPill } from "@/components/ui/status-pill"
import { entityHref } from "@/lib/entity-links"
import { formatRelativeShort } from "@/lib/time"
import { cn } from "@/lib/utils"
import { HolderStack, SkillCard, SkillTile } from "./skill-card"
import {
  MATURITY_LABEL,
  TRUST_LABEL,
  agentsMissingCredentials,
  domainMeta,
  matchesSkill,
  popoverFilterCount,
  skillName,
  skillTrust,
  sortSkills,
  sourceLabel,
  usageTotals,
  type ProposedSkill,
  type SkillFilters,
  type SkillRow,
  type SkillSort,
  type SkillsAgent,
  type SkillsCrew,
} from "./skills-model"

// What /skills shows with no skill open (#3033): what needs a person, how
// skills are used, then the catalog through the explorer's filters.

const EASE = [0.22, 1, 0.36, 1] as const

interface AttentionItem {
  key: string
  tone: "danger" | "warn" | "blue"
  icon: LucideIcon
  title: string
  detail: string
  verb: string
  onAct: () => void
}

const TONE_SQUARE: Record<AttentionItem["tone"], string> = {
  danger: "border-destructive/25 bg-destructive/[0.07] text-destructive",
  warn: "border-warn/25 bg-warn/[0.07] text-warn",
  blue: "border-primary/25 bg-primary/[0.07] text-primary-hover",
}

function AttentionStrip({ items }: { items: AttentionItem[] }) {
  const reduce = useReducedMotion()
  if (items.length === 0) return null
  return (
    <section data-testid="skills-attention" className="overflow-hidden rounded-xl border border-warn/20 bg-card">
      <div className="flex items-center gap-2 border-b border-border/60 px-4 py-3">
        <AlertTriangle className="h-4 w-4 text-warn" aria-hidden />
        <h2 className="text-body font-semibold">Needs your attention</h2>
        <span className="rounded-full bg-warn/12 px-2 py-px text-micro tabular-nums text-warn">{items.length}</span>
      </div>
      <div className="grid divide-y divide-border/60 lg:grid-cols-3 lg:divide-x lg:divide-y-0">
        {items.slice(0, 3).map((it, i) => (
          <motion.div
            key={it.key}
            initial={reduce ? false : { opacity: 0, y: 8, backgroundColor: "rgba(30,123,254,0.16)" }}
            animate={{ opacity: 1, y: 0, backgroundColor: "rgba(30,123,254,0)" }}
            transition={{ duration: 0.3, ease: EASE, delay: i * 0.06, backgroundColor: { duration: 2.2 } }}
            className="flex items-start gap-3 px-4 py-3"
          >
            <span className={cn("flex h-8 w-8 shrink-0 items-center justify-center rounded-lg border", TONE_SQUARE[it.tone])}>
              <it.icon className="h-4 w-4" aria-hidden />
            </span>
            <div className="min-w-0">
              <div className="text-control font-semibold">{it.title}</div>
              <div className="mt-0.5 text-label text-muted-foreground">{it.detail}</div>
              <button
                type="button"
                onClick={it.onAct}
                className="mt-2 rounded-md border border-border/70 px-2.5 py-1 text-label transition-colors hover:border-line-strong hover:bg-foreground/[0.04]"
              >
                {it.verb}
              </button>
            </div>
          </motion.div>
        ))}
      </div>
    </section>
  )
}

function UsageTiles({ skills }: { skills: SkillRow[] }) {
  const reduce = useReducedMotion()
  const t = usageTotals(skills)
  const errored = skills.filter((s) => (s.usage?.errors_7d ?? 0) > 0)
  const tiles: { icon: LucideIcon; tone: string; value: number; label: string; detail: string }[] = [
    { icon: Users, tone: "text-primary-hover bg-primary/10 border-primary/20", value: t.held, label: "in use", detail: `of ${t.total} skills are on an agent` },
    { icon: Zap, tone: "text-success bg-success/10 border-success/20", value: t.uses, label: "uses", detail: "times agents called a skill" },
    {
      icon: AlertTriangle,
      tone: "text-destructive bg-destructive/10 border-destructive/20",
      value: t.errors,
      label: "errors",
      detail: errored.length ? `${skillName(errored[0])}${errored.length > 1 ? ` and ${errored.length - 1} more` : ""} exited with an error` : "no skill call failed",
    },
    { icon: CircleDashed, tone: "text-muted-foreground bg-foreground/[0.04] border-border", value: t.idle, label: "idle", detail: "not on any agent" },
  ]
  return (
    <section className="flex flex-col gap-2" aria-label="Skill usage">
      <div className="flex items-center gap-1.5">
        <Activity className="h-3.5 w-3.5 text-primary-hover" aria-hidden />
        <h2 className="eyebrow">Skill usage</h2>
        <span className="font-mono text-micro text-muted-foreground-soft">7d</span>
      </div>
      <div className="grid grid-cols-2 gap-2 sm:gap-3 xl:grid-cols-4">
        {tiles.map((tile, i) => (
          <div key={tile.label} className="lift flex min-h-[72px] items-center gap-3 rounded-card border border-border bg-card px-3 py-2.5 sm:px-4 sm:py-3">
            <motion.span
              initial={reduce ? false : { rotate: -12, scale: 0.82, opacity: 0 }}
              animate={{ rotate: 0, scale: 1, opacity: 1 }}
              transition={{ type: "spring", stiffness: 420, damping: 22, delay: i * 0.07 }}
              className={cn("hidden h-8 w-8 shrink-0 items-center justify-center rounded-lg border sm:flex", tile.tone)}
            >
              <tile.icon className="h-4 w-4" aria-hidden />
            </motion.span>
            <div className="min-w-0">
              <div className="flex items-baseline gap-1.5">
                <span className="text-heading font-semibold leading-none tabular-nums">{tile.value}</span>
                <span className="text-micro font-semibold uppercase tracking-wider text-muted-foreground">{tile.label}</span>
              </div>
              <div className="mt-1 truncate text-label text-muted-foreground">{tile.detail}</div>
            </div>
          </div>
        ))}
      </div>
    </section>
  )
}

function SkillsTable({ rows, onOpen }: { rows: SkillRow[]; onOpen: (id: string) => void }) {
  return (
    <div className="overflow-x-auto rounded-card border border-border bg-card">
      <table className="w-full border-collapse text-control">
        <thead>
          <tr className="border-b border-border text-left text-label text-muted-foreground">
            <th className="px-4 py-2 font-medium">Skill</th>
            <th className="px-4 py-2 font-medium">Trust</th>
            <th className="px-4 py-2 font-medium">Domain</th>
            <th className="px-4 py-2 font-medium">Agents</th>
            <th className="px-4 py-2 font-medium">Uses (7d)</th>
            <th className="px-4 py-2 font-medium">Last used</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((s) => {
            const trust = skillTrust(s)
            return (
              <tr
                key={s.id}
                tabIndex={0}
                onClick={() => onOpen(s.id)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") onOpen(s.id)
                }}
                className="row-hover cursor-pointer border-b border-border/50 last:border-0"
              >
                <td className="whitespace-nowrap px-4 py-2">
                  <span className="inline-flex items-center gap-2">
                    <SkillTile category={s.category} icon={s.icon} size="sm" />
                    <span className="font-medium">{skillName(s)}</span>
                  </span>
                </td>
                <td className="px-4 py-2">
                  <StatusPill tone={trust.tone} label={trust.label} />
                </td>
                <td className="whitespace-nowrap px-4 py-2 text-muted-foreground">{domainMeta(s.category).label}</td>
                <td className="whitespace-nowrap px-4 py-2">
                  {(s.installed_on?.length ?? 0) > 0 ? (
                    <span className="inline-flex items-center gap-1.5">
                      <HolderStack agents={s.installed_on ?? []} max={4} />
                      <span className="tabular-nums text-muted-foreground">{s.installed_on?.length}</span>
                    </span>
                  ) : (
                    <span className="text-muted-foreground-soft">—</span>
                  )}
                </td>
                <td className="px-4 py-2 font-mono tabular-nums">{s.usage?.uses_7d || "—"}</td>
                <td className="whitespace-nowrap px-4 py-2 font-mono text-muted-foreground">
                  {s.usage?.last_used_at ? formatRelativeShort(s.usage.last_used_at) : "never"}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

function AgentHeader({
  agent,
  crew,
  skills,
  onAddSkills,
}: {
  agent: SkillsAgent
  crew: SkillsCrew | undefined
  skills: SkillRow[]
  onAddSkills: () => void
}) {
  const missing = skills.flatMap((s) => (s.installed_on ?? []).filter((a) => a.agent_id === agent.id && (a.missing_credentials?.length ?? 0) > 0))
  return (
    <section className="flex flex-wrap items-center gap-3 rounded-card border border-border bg-card px-4 py-3">
      <AgentAvatar
        seed={agent.avatar_seed ?? agent.slug}
        style={agent.avatar_style}
        avatarUrl={agent.avatar_url}
        alt=""
        width={36}
        height={36}
        className="h-9 w-9 shrink-0 rounded-full bg-foreground/[0.04]"
      />
      <div className="min-w-0 flex-1">
        <div className="text-body font-semibold">
          {agent.name}
          {agent.role_title && <span className="font-normal text-muted-foreground"> · {agent.role_title}</span>}
        </div>
        <div className="mt-0.5 flex flex-wrap items-center gap-1.5 text-label text-muted-foreground">
          {crew && (
            <>
              <CrewIcon icon={crew.icon ?? "users"} color={crew.color} size="sm" className="h-4 w-4 rounded [&>svg]:h-2.5 [&>svg]:w-2.5" />
              {crew.name} ·
            </>
          )}
          <span className="tabular-nums">
            {skills.length} {skills.length === 1 ? "skill" : "skills"}
          </span>
          {missing.length > 0 && <span className="text-warn">· {missing.length} missing a credential</span>}
        </div>
      </div>
      <Button variant="outline" size="sm" onClick={onAddSkills}>
        <Plus className="h-3.5 w-3.5" />
        Skills for {agent.name}
      </Button>
      <Button variant="ghost" size="sm" asChild>
        <Link href={entityHref({ kind: "chat", agentSlug: agent.slug })}>
          <MessageSquare className="h-3.5 w-3.5" />
          Chat
        </Link>
      </Button>
    </section>
  )
}

function ProposedPane({
  proposed,
  crews,
  canReview,
  busy,
  onReview,
}: {
  proposed: ProposedSkill[]
  crews: SkillsCrew[]
  canReview: boolean
  busy: boolean
  onReview: (p: ProposedSkill, approve: boolean) => void
}) {
  return (
    <section className="flex flex-col gap-3">
      <div className="flex items-center gap-1.5">
        <Sparkles className="h-3.5 w-3.5 text-purple" aria-hidden />
        <h2 className="eyebrow">Proposed by agents</h2>
        <span className="font-mono text-micro text-muted-foreground-soft">{proposed.length} waiting</span>
      </div>
      {proposed.length === 0 ? (
        <InlineEmpty
          icon={Sparkles}
          text={
            canReview
              ? "Nothing proposed. Agents propose a skill when something they keep recalling from memory is worth making a habit."
              : "Proposals are reviewed by workspace managers."
          }
        />
      ) : (
        proposed.map((p) => {
          const crew = crews.find((c) => c.id === p.crew_id)
          return (
            <div key={`${p.crew_id}/${p.file_name}`} className="flex flex-wrap items-start gap-3 rounded-card border border-border bg-card px-4 py-3.5">
              <SkillTile category={p.category} />
              <div className="min-w-[14rem] flex-1">
                <div className="flex flex-wrap items-center gap-2 text-body font-semibold">
                  {p.name}
                  <StatusPill tone="purple" label="Proposed" />
                  {p.description_quality && p.description_quality !== "OK" && <StatusPill tone="warn" label="Weak trigger" />}
                </div>
                <p className="mt-1 text-label text-muted-foreground">{p.description}</p>
                {crew && (
                  <div className="mt-2 flex items-center gap-1.5 text-label text-muted-foreground">
                    <CrewIcon icon={crew.icon ?? "users"} color={crew.color} size="sm" className="h-4 w-4 rounded [&>svg]:h-2.5 [&>svg]:w-2.5" />
                    From {crew.name}
                  </div>
                )}
              </div>
              <div className="flex gap-2">
                <Button variant="outline" size="sm" disabled={busy} onClick={() => onReview(p, false)}>
                  Reject
                </Button>
                <Button size="sm" disabled={busy} onClick={() => onReview(p, true)}>
                  <CheckCircle2 className="h-3.5 w-3.5" />
                  Approve
                </Button>
              </div>
            </div>
          )
        })
      )}
    </section>
  )
}

export interface SkillsOverviewProps {
  skills: SkillRow[]
  loading: boolean
  error: boolean
  onRetry: () => void
  agents: SkillsAgent[]
  crews: SkillsCrew[]
  proposed: ProposedSkill[]
  canReview: boolean
  reviewBusy: boolean
  onReview: (p: ProposedSkill, approve: boolean) => void
  filters: SkillFilters
  onChange: (patch: Partial<SkillFilters>) => void
  onClearAll: () => void
  sort: SkillSort
  onSort: (s: SkillSort) => void
  layout: "grid" | "list"
  onLayout: (l: "grid" | "list") => void
  onOpen: (id: string, tab?: string) => void
  onAddSkillsToAgent: (agentId: string) => void
}

export function SkillsOverview(props: SkillsOverviewProps) {
  const { skills, filters, onChange, sort, agents, crews } = props
  const visible = useMemo(() => sortSkills(skills.filter((s) => matchesSkill(s, filters)), sort), [skills, filters, sort])

  if (props.loading) {
    return (
      <div className="flex flex-col gap-3 p-4 md:p-5" aria-busy>
        <Skeleton className="h-24 rounded-xl" />
        <div className="grid grid-cols-2 gap-3 xl:grid-cols-4">
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-[72px] rounded-card" />
          ))}
        </div>
        <div className="grid gap-3 [grid-template-columns:repeat(auto-fill,minmax(min(100%,290px),1fr))]">
          {[0, 1, 2, 3, 4, 5].map((i) => (
            <Skeleton key={i} className="h-[178px] rounded-card" />
          ))}
        </div>
      </div>
    )
  }
  if (props.error) {
    return (
      <div className="p-4 md:p-5">
        <InlineEmpty
          icon={AlertTriangle}
          text="Skills could not be loaded."
          action={
            <Button variant="outline" size="sm" onClick={props.onRetry}>
              Retry
            </Button>
          }
        />
      </div>
    )
  }

  if (filters.view === "proposed") {
    return (
      <div className="p-4 md:p-5">
        <ProposedPane proposed={props.proposed} crews={crews} canReview={props.canReview} busy={props.reviewBusy} onReview={props.onReview} />
      </div>
    )
  }

  const pristine =
    filters.view === "all" && !filters.agentId && !filters.crewId && !filters.domain && !filters.query && popoverFilterCount(filters) === 0

  // What needs a person, most severe first.
  const attention: AttentionItem[] = []
  if (pristine) {
    const flagged = skills.filter((s) => skillTrust(s).level === "flagged" && (s.installed_on?.length ?? 0) > 0)
    if (flagged[0]) {
      attention.push({
        key: "flagged",
        tone: "danger",
        icon: AlertTriangle,
        title: `${skillName(flagged[0])} is flagged`,
        detail: `${flagged[0].description_quality || "The import scan flagged its instructions"}. ${flagged[0].installed_on?.length} agent${flagged[0].installed_on?.length === 1 ? " has" : "s have"} it.`,
        verb: "Inspect",
        onAct: () => props.onOpen(flagged[0].id),
      })
    }
    if (props.proposed[0]) {
      const p = props.proposed[0]
      attention.push({
        key: "proposed",
        tone: "blue",
        icon: Sparkles,
        title: props.proposed.length === 1 ? `“${p.name}” was proposed` : `${props.proposed.length} skills were proposed`,
        detail: crews.find((c) => c.id === p.crew_id)?.name ? `By ${crews.find((c) => c.id === p.crew_id)?.name}, waiting for a review.` : "Waiting for a review.",
        verb: "Review",
        onAct: () => onChange({ view: "proposed" }),
      })
    }
    const missing = skills.filter((s) => agentsMissingCredentials(s).length > 0)
    if (missing[0]) {
      const first = agentsMissingCredentials(missing[0])[0]
      attention.push({
        key: "credentials",
        tone: "warn",
        icon: KeyRound,
        title: `${missing.length} skill${missing.length === 1 ? "" : "s"} missing a credential`,
        detail: `${first.agent_name} has ${skillName(missing[0])} but no ${first.missing_credentials?.[0]}.`,
        verb: "Fix",
        onAct: () => props.onOpen(missing[0].id, "agents"),
      })
    }
  }

  const agent = filters.agentId ? agents.find((a) => a.id === filters.agentId) : undefined
  const chips: { key: string; label: string; remove: () => void }[] = []
  if (agent) chips.push({ key: "agent", label: agent.name, remove: () => onChange({ agentId: null }) })
  else if (filters.crewId) {
    const crew = crews.find((c) => c.id === filters.crewId)
    chips.push({ key: "crew", label: crew?.name ?? "Crew", remove: () => onChange({ crewId: null }) })
  }
  if (filters.domain) chips.push({ key: "domain", label: domainMeta(filters.domain).label, remove: () => onChange({ domain: null }) })
  filters.sources.forEach((v) => chips.push({ key: `src:${v}`, label: sourceLabel(v), remove: () => onChange({ sources: filters.sources.filter((x) => x !== v) }) }))
  filters.trust.forEach((v) => chips.push({ key: `trust:${v}`, label: TRUST_LABEL[v], remove: () => onChange({ trust: filters.trust.filter((x) => x !== v) }) }))
  filters.maturities.forEach((v) =>
    chips.push({ key: `mat:${v}`, label: MATURITY_LABEL[v] ?? v, remove: () => onChange({ maturities: filters.maturities.filter((x) => x !== v) }) }),
  )
  if (filters.needsCredential) chips.push({ key: "needs", label: "Needs a credential", remove: () => onChange({ needsCredential: false }) })

  return (
    <div className="flex flex-col gap-3 p-3 pb-24 sm:p-4 md:p-5">
      {pristine && <AttentionStrip items={attention} />}
      {pristine && skills.length > 0 && <UsageTiles skills={skills} />}
      {agent && (
        <AgentHeader
          agent={agent}
          crew={crews.find((c) => c.id === agent.crew_id)}
          skills={visible}
          onAddSkills={() => props.onAddSkillsToAgent(agent.id)}
        />
      )}

      <div className="flex flex-wrap items-center gap-2">
        {chips.map((c) => (
          <span key={c.key} className="inline-flex items-center gap-1 rounded-full bg-primary/[0.14] py-0.5 pl-2.5 pr-1.5 text-label text-primary-hover">
            {c.label}
            <button type="button" onClick={c.remove} aria-label={`Remove ${c.label}`} className="rounded-full p-0.5 hover:bg-primary/20">
              <X className="h-3 w-3" />
            </button>
          </span>
        ))}
        {chips.length > 1 && (
          <button type="button" onClick={props.onClearAll} className="text-label text-primary-hover hover:underline">
            Clear all
          </button>
        )}
        <span className="flex-1" />
        <span className="text-label tabular-nums text-muted-foreground">
          {visible.length} of {skills.length}
        </span>
        <select
          aria-label="Sort skills"
          value={sort}
          onChange={(e) => props.onSort(e.target.value as SkillSort)}
          className="h-8 rounded-md border border-control-border bg-card px-2 text-label"
        >
          <option value="used">Most used</option>
          <option value="agents">Most agents</option>
          <option value="name">Name</option>
        </select>
        <div className="hidden rounded-md border border-border p-0.5 md:flex" role="group" aria-label="Layout">
          {(
            [
              ["grid", LayoutGrid, "Cards"],
              ["list", List, "Table"],
            ] as const
          ).map(([id, Icon, label]) => (
            <button
              key={id}
              type="button"
              aria-pressed={props.layout === id}
              aria-label={label}
              onClick={() => props.onLayout(id)}
              className={cn(
                "flex h-6 w-7 items-center justify-center rounded text-muted-foreground transition-colors",
                props.layout === id ? "bg-foreground/[0.07] text-foreground" : "hover:text-foreground",
              )}
            >
              <Icon className="h-3.5 w-3.5" />
            </button>
          ))}
        </div>
      </div>

      {visible.length === 0 ? (
        <InlineEmpty
          icon={Search}
          text={
            skills.length === 0
              ? "No skills yet. Import a SKILL.md or write one with New skill; agents use the skills they are given."
              : agent
                ? `${agent.name} has no skills that match. Give it some with “Skills for ${agent.name}”.`
                : "No skills match these filters."
          }
          action={
            skills.length > 0 ? (
              <Button variant="outline" size="sm" onClick={props.onClearAll}>
                Clear filters
              </Button>
            ) : undefined
          }
        />
      ) : props.layout === "list" ? (
        <SkillsTable rows={visible} onOpen={props.onOpen} />
      ) : (
        <div className="grid gap-3 [grid-template-columns:repeat(auto-fill,minmax(min(100%,290px),1fr))]">
          {visible.map((s, i) => (
            <SkillCard key={s.id} skill={s} index={i} onOpen={props.onOpen} />
          ))}
        </div>
      )}
    </div>
  )
}
