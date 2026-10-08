"use client"

import { useMemo, useState } from "react"
import { motion, useReducedMotion } from "motion/react"
import Link from "next/link"
import { toast } from "sonner"
import { Streamdown } from "streamdown"
import {
  Activity,
  AlertTriangle,
  BookOpen,
  Download,
  ExternalLink,
  FileText,
  KeyRound,
  Layers,
  Trash2,
  Users,
} from "lucide-react"

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Button } from "@/components/ui/button"
import { CrewIcon } from "@/components/ui/crew-icon"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { Skeleton } from "@/components/ui/skeleton"
import { StatusPill } from "@/components/ui/status-pill"
import { Switch } from "@/components/ui/switch"
import { useJournalList } from "@/hooks/use-journal-list"
import { useDeleteSkill, useSetSkillAssignment, useSkillDetail } from "@/hooks/use-skills"
import { entityHref } from "@/lib/entity-links"
import { formatRelativeShort } from "@/lib/time"
import { cn } from "@/lib/utils"
import { SkillTile } from "./skill-card"
import {
  MATURITY_LABEL,
  domainMeta,
  skillName,
  skillTrust,
  sourceLabel,
  type SkillDetail,
  type SkillRow,
  type SkillsAgent,
  type SkillsCrew,
} from "./skills-model"

// One skill, opened in the content pane beside the explorer (#3033) — the
// Routines detail grammar: identity card with the actions, pill tabs, then
// DetailCard-style sections. No third column.

export type SkillTab = "overview" | "agents" | "content" | "activity"
export const SKILL_TABS: { id: SkillTab; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "agents", label: "Agents" },
  { id: "content", label: "SKILL.md" },
  { id: "activity", label: "Activity" },
]
export function isSkillTab(v: string | null | undefined): v is SkillTab {
  return v === "overview" || v === "agents" || v === "content" || v === "activity"
}

function Section({ icon: Icon, title, meta, children, className }: { icon: typeof Layers; title: string; meta?: React.ReactNode; children: React.ReactNode; className?: string }) {
  return (
    <section className={cn("overflow-hidden rounded-card border border-border bg-card", className)}>
      <div className="flex flex-wrap items-center gap-2 px-4 py-3">
        <Icon className="h-3.5 w-3.5 text-primary-hover" aria-hidden />
        <h3 className="eyebrow">{title}</h3>
        {meta && <div className="ml-auto text-label text-muted-foreground">{meta}</div>}
      </div>
      {children}
    </section>
  )
}

/** SKILL.md as the CLI's `skill export` writes it: frontmatter + body. */
function skillMarkdown(s: SkillDetail): string {
  const lines = ["---", `name: ${s.slug}`]
  if (s.description) lines.push(`description: ${JSON.stringify(s.description)}`)
  if (s.version) lines.push(`version: ${s.version}`)
  if (s.spdx_license || s.license) lines.push(`license: ${s.spdx_license || s.license}`)
  if (s.category) lines.push(`category: ${s.category}`)
  if (s.needs_credentials?.length) lines.push(`credential_requirements: [${s.needs_credentials.join(", ")}]`)
  lines.push("---", "", s.content ?? "")
  return lines.join("\n")
}

function downloadSkill(s: SkillDetail) {
  const blob = new Blob([skillMarkdown(s)], { type: "text/markdown" })
  const url = URL.createObjectURL(blob)
  const a = document.createElement("a")
  a.href = url
  a.download = `${s.slug}.SKILL.md`
  a.click()
  URL.revokeObjectURL(url)
}

function OverviewTab({ skill, onTab }: { skill: SkillDetail; onTab: (t: SkillTab) => void }) {
  const trust = skillTrust(skill)
  const holders = skill.installed_on ?? []
  const lacking = holders.filter((a) => (a.missing_credentials?.length ?? 0) > 0)
  const needs = skill.needs_credentials ?? []
  const usage = skill.usage
  const cells: { title: string; body: React.ReactNode }[] = [
    { title: "What it does", body: skill.description || "No description." },
    {
      title: "Who has it",
      body: (
        <>
          {holders.length ? holders.map((a) => a.agent_name).join(", ") : "No agent yet."}{" "}
          <button type="button" className="text-primary-hover hover:underline" onClick={() => onTab("agents")}>
            Manage →
          </button>
        </>
      ),
    },
    {
      title: "What it needs",
      body: needs.length ? (
        <>
          {needs.map((n) => (
            <code key={n} className="mr-1 rounded bg-foreground/[0.06] px-1 font-mono text-micro">
              {n}
            </code>
          ))}
          {lacking.length > 0 ? (
            <span className="text-warn">
              {" "}
              · {lacking.length} agent{lacking.length === 1 ? "" : "s"} without it
            </span>
          ) : holders.length > 0 ? (
            " · every agent has it"
          ) : null}
        </>
      ) : (
        "Nothing. It runs as instructions only."
      ),
    },
    {
      title: "Trust",
      body:
        trust.level === "flagged"
          ? `Flagged by the import scan: ${skill.description_quality || "suspicious instructions"}.`
          : trust.level === "unscanned"
            ? "Never scanned. Re-import it to run the scan."
            : `Scan clean. ${trust.level === "verified" ? "Curator verified." : "The curator has not verified it."}`,
    },
    {
      title: "Where it came from",
      body: (
        <>
          {sourceLabel(skill.source)}
          {skill.vendor ? ` · ${skill.vendor}` : ""}
          {skill.version ? ` · v${skill.version}` : ""}
          {skill.homepage && (
            <>
              {" · "}
              <a href={skill.homepage} target="_blank" rel="noreferrer" className="text-primary-hover hover:underline">
                Source <ExternalLink className="inline h-3 w-3" />
              </a>
            </>
          )}
        </>
      ),
    },
    { title: "License", body: skill.spdx_license || skill.license || "Not stated" },
  ]
  return (
    <>
      <Section icon={Layers} title="In one look">
        <div className="grid border-t border-border sm:grid-cols-2 xl:grid-cols-3">
          {cells.map((c) => (
            <div key={c.title} className="min-w-0 border-b border-border px-4 py-3 sm:[&:nth-child(odd)]:border-r xl:border-r xl:[&:nth-child(3n)]:border-r-0">
              <h4 className="mb-1 text-micro font-semibold uppercase tracking-wider text-muted-foreground">{c.title}</h4>
              <div className="text-control leading-relaxed">{c.body}</div>
            </div>
          ))}
        </div>
      </Section>
      <Section
        icon={Activity}
        title="Usage"
        meta={
          <span className="font-mono tabular-nums">
            {usage?.uses_7d ?? 0} uses · 7d{usage?.errors_7d ? ` · ${usage.errors_7d} errors` : ""}
          </span>
        }
      >
        <div className="px-4 pb-4">
          {usage && usage.uses_total > 0 ? (
            <p className="text-control text-muted-foreground">
              Used {usage.uses_total} {usage.uses_total === 1 ? "time" : "times"} in this workspace, last {formatRelativeShort(usage.last_used_at)}.{" "}
              <button type="button" className="text-primary-hover hover:underline" onClick={() => onTab("activity")}>
                See activity →
              </button>
            </p>
          ) : (
            <InlineEmpty icon={Activity} text="No agent has used it yet. Uses appear here once an agent calls the skill during a run." />
          )}
        </div>
      </Section>
    </>
  )
}

function AgentsTab({ skill, agents, crews, workspaceId }: { skill: SkillDetail; agents: SkillsAgent[]; crews: SkillsCrew[]; workspaceId: string }) {
  const assign = useSetSkillAssignment(workspaceId)
  const [pending, setPending] = useState<Record<string, boolean>>({})
  const holders = useMemo(() => new Map((skill.installed_on ?? []).map((a) => [a.agent_id, a])), [skill.installed_on])
  const needs = skill.needs_credentials ?? []
  const flagged = skillTrust(skill).level === "flagged"

  // A switch applies at once (README › Saving); it shows the asked-for state
  // while the write is in flight, and a failure flips it back with a toast.
  const setOne = async (agent: SkillsAgent, on: boolean) => {
    setPending((p) => ({ ...p, [agent.id]: on }))
    try {
      await assign.mutateAsync({ agentId: agent.id, skillId: skill.id, on })
      toast.success(on ? `${agent.name} now has ${skillName(skill)}` : `${agent.name} no longer has ${skillName(skill)}`)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Could not change the assignment")
    } finally {
      setPending((p) => {
        const next = { ...p }
        delete next[agent.id]
        return next
      })
    }
  }
  const setCrew = async (members: SkillsAgent[], on: boolean) => {
    const todo = members.filter((a) => holders.has(a.id) !== on)
    for (const a of todo) setPending((p) => ({ ...p, [a.id]: on }))
    const results = await Promise.allSettled(todo.map((a) => assign.mutateAsync({ agentId: a.id, skillId: skill.id, on })))
    setPending({})
    const failed = results.filter((r) => r.status === "rejected").length
    if (failed) toast.error(`${failed} of ${todo.length} changes failed`)
    else toast.success(on ? `${skillName(skill)} given to ${todo.length} agents` : `${skillName(skill)} removed from ${todo.length} agents`)
  }

  const groups = [
    ...crews.map((c) => ({ crew: c as SkillsCrew | null, members: agents.filter((a) => a.crew_id === c.id) })),
    { crew: null, members: agents.filter((a) => !a.crew_id || !crews.some((c) => c.id === a.crew_id)) },
  ].filter((g) => g.members.length > 0)

  return (
    <Section icon={Users} title="Agents with this skill" meta="A switch applies at once; the agent loads the skill on its next run.">
      {flagged && (
        <div className="mx-4 mb-3 flex items-start gap-2 rounded-lg border border-destructive/30 bg-chip-danger-bg px-3 py-2 text-label text-chip-danger-fg">
          <AlertTriangle className="mt-px h-3.5 w-3.5 shrink-0" />
          The import scan flagged this skill. Read its SKILL.md before giving it to another agent.
        </div>
      )}
      {groups.length === 0 && (
        <div className="px-4 pb-4">
          <InlineEmpty icon={Users} text="This workspace has no agents yet. Create a crew to give skills to its agents." />
        </div>
      )}
      {groups.map(({ crew, members }) => {
        const on = members.filter((a) => (pending[a.id] ?? holders.has(a.id))).length
        const all = on === members.length
        return (
          <div key={crew?.id ?? "none"} className="border-t border-border">
            <div className="flex items-center gap-2 px-4 py-2.5">
              {crew ? (
                <CrewIcon icon={crew.icon ?? "users"} color={crew.color} size="sm" className="h-[18px] w-[18px] rounded-md [&>svg]:h-3 [&>svg]:w-3" />
              ) : (
                <Users className="h-3.5 w-3.5 text-muted-foreground" />
              )}
              <span className="text-control font-semibold">{crew?.name ?? "No crew"}</span>
              <span className="font-mono text-micro tabular-nums text-muted-foreground-soft">
                {on}/{members.length}
              </span>
              <span className="flex-1" />
              <button type="button" className="text-label text-primary-hover hover:underline" onClick={() => setCrew(members, !all)}>
                {all ? (crew ? "Remove from crew" : "Remove from all") : crew ? "Give to whole crew" : "Give to all"}
              </button>
            </div>
            {members.map((a) => {
              const held = pending[a.id] ?? holders.has(a.id)
              const missing = holders.get(a.id)?.missing_credentials ?? []
              return (
                <div key={a.id} className="row-hover flex min-h-11 items-center gap-2.5 py-1.5 pl-10 pr-4">
                  <AgentAvatar
                    seed={a.avatar_seed ?? a.slug}
                    style={a.avatar_style}
                    agentId={a.id}
                    avatarUrl={a.avatar_url}
                    alt=""
                    width={20}
                    height={20}
                    className="h-5 w-5 shrink-0 rounded-full bg-foreground/[0.04]"
                  />
                  <Link href={entityHref({ kind: "agent", slug: a.slug })} className="text-control hover:underline">
                    {a.name}
                  </Link>
                  {a.role_title && <span className="hidden truncate text-micro text-muted-foreground-soft sm:inline">{a.role_title}</span>}
                  <span className="flex-1" />
                  {held && missing.length > 0 && (
                    <Link
                      href={entityHref({ kind: "credentials" })}
                      className="inline-flex items-center gap-1 text-micro text-warn hover:underline"
                      title="Give this agent the credential on the Credentials page"
                    >
                      <KeyRound className="h-3 w-3" />
                      no {missing.join(", ")}
                    </Link>
                  )}
                  {!held && needs.length > 0 && <span className="hidden text-micro text-muted-foreground-soft md:inline">needs {needs.join(", ")}</span>}
                  <Switch
                    checked={held}
                    disabled={a.id in pending}
                    onCheckedChange={(v) => setOne(a, v)}
                    aria-label={`${a.name} has ${skillName(skill)}`}
                  />
                </div>
              )
            })}
          </div>
        )
      })}
    </Section>
  )
}

function ContentTab({ skill }: { skill: SkillDetail }) {
  return (
    <Section
      icon={FileText}
      title="SKILL.md"
      meta={
        <Button variant="ghost" size="sm" onClick={() => downloadSkill(skill)}>
          <Download className="h-3.5 w-3.5" />
          Download
        </Button>
      }
    >
      <div className="prose-sm max-w-none border-t border-border px-4 py-4 text-control [&_pre]:overflow-x-auto">
        {skill.content ? <Streamdown>{skill.content}</Streamdown> : <p className="text-muted-foreground">This skill has no instructions.</p>}
      </div>
    </Section>
  )
}

const ACTIVITY_TYPES = "skill.invoked,skill.assigned,skill.unassigned,skill.imported"
const ACTIVITY_WORDS: Record<string, string> = {
  "skill.invoked": "used it",
  "skill.assigned": "was given it",
  "skill.unassigned": "lost it",
  "skill.imported": "Imported",
}

function ActivityTab({ skill, workspaceId, agents }: { skill: SkillDetail; workspaceId: string; agents: SkillsAgent[] }) {
  const { entries, loading, error } = useJournalList({ workspaceId, params: { entry_type: ACTIVITY_TYPES }, limit: 200 })
  const mine = entries.filter((e) => {
    const p = (e.payload ?? {}) as Record<string, unknown>
    return p.skill_id === skill.id || p.skill_slug === skill.slug || (e.entry_type === "skill.imported" && e.summary.endsWith(`: ${skill.slug}`))
  })
  const agentName = (id?: string) => agents.find((a) => a.id === id)?.name ?? "An agent"
  return (
    <Section
      icon={BookOpen}
      title="Journal"
      meta={
        <Link href={entityHref({ kind: "journal" })} className="text-primary-hover hover:underline">
          Open Journal ↗
        </Link>
      }
    >
      {loading ? (
        <div className="flex flex-col gap-2 px-4 pb-4">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-8" />
          ))}
        </div>
      ) : error ? (
        <div className="px-4 pb-4">
          <InlineEmpty icon={AlertTriangle} text="The journal could not be read." />
        </div>
      ) : mine.length === 0 ? (
        <div className="px-4 pb-4">
          <InlineEmpty icon={BookOpen} text="Nothing recorded yet. Assigning, removing and every use by an agent appear here." />
        </div>
      ) : (
        <ul>
          {mine.map((e) => {
            const p = (e.payload ?? {}) as Record<string, unknown>
            const who = e.entry_type === "skill.imported" ? "" : `${agentName((p.agent_id as string) ?? e.agent_id)} `
            const failed = e.entry_type === "skill.invoked" && Number(p.exit_code ?? 0) !== 0
            return (
              <li key={e.id} className="grid grid-cols-[90px_1fr_auto] items-center gap-3 border-t border-border/60 px-4 py-2 text-control">
                <span className="font-mono text-micro text-muted-foreground-soft">{formatRelativeShort(e.ts)}</span>
                <span className="min-w-0 truncate">
                  {who}
                  {ACTIVITY_WORDS[e.entry_type] ?? e.entry_type}
                </span>
                <StatusPill tone={failed ? "danger" : e.entry_type === "skill.invoked" ? "blue" : "muted"} label={failed ? "error" : e.entry_type} />
              </li>
            )
          })}
        </ul>
      )}
    </Section>
  )
}

export interface SkillPageProps {
  workspaceId: string
  skillId: string
  /** The row from the list, shown while the detail loads. */
  row?: SkillRow
  agents: SkillsAgent[]
  crews: SkillsCrew[]
  tab: SkillTab
  onTab: (t: SkillTab) => void
  onAssign: () => void
  onDeleted: () => void
}

export function SkillPage({ workspaceId, skillId, row, agents, crews, tab, onTab, onAssign, onDeleted }: SkillPageProps) {
  const reduce = useReducedMotion()
  const { data, isLoading, isError, refetch } = useSkillDetail(workspaceId, skillId)
  const del = useDeleteSkill(workspaceId)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const skill = data ?? (row as SkillDetail | undefined)

  if (!skill) {
    return (
      <div className="flex flex-col gap-3 p-4 md:p-5">
        {isError ? (
          <InlineEmpty
            icon={AlertTriangle}
            text="This skill could not be loaded. It may have been deleted."
            action={
              <Button variant="outline" size="sm" onClick={() => refetch()}>
                Retry
              </Button>
            }
          />
        ) : (
          <>
            <Skeleton className="h-28 rounded-card" />
            <Skeleton className="h-8 w-80" />
            <Skeleton className="h-48 rounded-card" />
          </>
        )}
      </div>
    )
  }

  const trust = skillTrust(skill)
  const holders = skill.installed_on?.length ?? 0
  const builtIn = skill.source === "BUNDLED"
  const maturity = (skill.maturity ?? "COMMUNITY").toUpperCase()

  return (
    <div className="flex flex-col gap-4 p-3 pb-24 sm:p-4 md:p-5">
      <motion.section
        initial={reduce ? false : { opacity: 0, y: 6 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.25, ease: [0.22, 1, 0.36, 1] }}
        className="flex flex-wrap items-start gap-4 rounded-card border border-border/60 bg-card p-4"
      >
        <SkillTile category={skill.category} size="lg" />
        <div className="min-w-0 flex-1">
          <h2 className="text-heading font-semibold tracking-tight">{skillName(skill)}</h2>
          <div className="mt-1.5 flex flex-wrap items-center gap-1.5 text-micro text-muted-foreground">
            <StatusPill tone={trust.tone} label={trust.label} />
            <StatusPill tone={maturity === "OFFICIAL" ? "blue" : "muted"} label={MATURITY_LABEL[maturity] ?? maturity} />
            {skill.lifecycle_state && skill.lifecycle_state !== "active" && <StatusPill tone="warn" label={skill.lifecycle_state} />}
            {skill.version && <span>v{skill.version}</span>}
            <span aria-hidden>·</span>
            <span>
              {sourceLabel(skill.source)}
              {skill.vendor ? ` from ${skill.vendor}` : ""}
            </span>
            <span aria-hidden>·</span>
            <span>
              on <b className="tabular-nums text-foreground">{holders}</b> {holders === 1 ? "agent" : "agents"}
            </span>
            <span aria-hidden>·</span>
            <span>{domainMeta(skill.category).label}</span>
          </div>
          {trust.level === "flagged" && (
            <div className="mt-3 flex items-start gap-2 rounded-lg border border-destructive/30 bg-chip-danger-bg px-3 py-2 text-label text-chip-danger-fg">
              <AlertTriangle className="mt-px h-3.5 w-3.5 shrink-0" />
              {skill.description_quality || "The import scan flagged its instructions"}. Read the SKILL.md before keeping it on agents.
            </div>
          )}
        </div>
        <div className="flex w-full flex-wrap items-center gap-2 sm:w-auto">
          <Button size="sm" onClick={onAssign} className="flex-1 sm:flex-none">
            <Users className="h-3.5 w-3.5" />
            Assign to agents
          </Button>
          <Button variant="outline" size="sm" onClick={() => data && downloadSkill(data)} disabled={!data}>
            <Download className="h-3.5 w-3.5" />
            Download
          </Button>
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Delete skill"
            disabled={builtIn}
            title={builtIn ? "Built-in skills cannot be deleted; they reinstall on every start." : "Delete skill"}
            onClick={() => setConfirmDelete(true)}
          >
            <Trash2 className="h-3.5 w-3.5" />
          </Button>
          {builtIn && <p className="w-full text-right text-micro text-muted-foreground-soft">Built-in skills cannot be deleted; they reinstall on every start.</p>}
        </div>
      </motion.section>

      <nav role="tablist" aria-label="Skill sections" className="flex gap-2 overflow-x-auto border-b border-border pb-2">
        {SKILL_TABS.map((t) => (
          <button
            key={t.id}
            type="button"
            role="tab"
            aria-selected={tab === t.id}
            onClick={() => onTab(t.id)}
            className={cn(
              "shrink-0 rounded-full px-3 py-1.5 text-label transition-colors",
              tab === t.id ? "bg-muted font-medium text-foreground" : "text-muted-foreground hover:bg-muted/60",
            )}
          >
            {t.label}
            {t.id === "agents" && <span className="ml-1 tabular-nums text-muted-foreground">· {holders}</span>}
          </button>
        ))}
      </nav>

      <motion.div
        key={tab}
        initial={reduce ? false : { opacity: 0, y: 4 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.18 }}
        className="flex flex-col gap-4"
      >
        {tab === "overview" && <OverviewTab skill={skill} onTab={onTab} />}
        {tab === "agents" && <AgentsTab skill={skill} agents={agents} crews={crews} workspaceId={workspaceId} />}
        {tab === "content" &&
          (isLoading && !data ? <Skeleton className="h-64 rounded-card" /> : <ContentTab skill={skill} />)}
        {tab === "activity" && <ActivityTab skill={skill} workspaceId={workspaceId} agents={agents} />}
      </motion.div>

      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete {skillName(skill)}?</AlertDialogTitle>
            <AlertDialogDescription>
              It is removed from the catalog and from {holders} {holders === 1 ? "agent" : "agents"}; they stop loading it on their next run. To get it back,
              import its SKILL.md again — download it first if you have no copy.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep it</AlertDialogCancel>
            <AlertDialogAction
              onClick={async () => {
                try {
                  await del.mutateAsync(skill.id)
                  toast.success(`${skillName(skill)} deleted`)
                  onDeleted()
                } catch (e) {
                  toast.error(e instanceof Error ? e.message : "Could not delete the skill")
                }
              }}
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
