"use client"

import { useMemo, useRef, useState } from "react"
import Link from "next/link"
import { Streamdown } from "streamdown"
import {
  Activity,
  AlertTriangle,
  Archive,
  ArchiveRestore,
  Bot,
  Check,
  ChevronLeft,
  Copy,
  CopyPlus,
  Download,
  ExternalLink,
  FileText,
  KeyRound,
  Layers,
  Lock,
  MoreHorizontal,
  Pencil,
  Terminal,
  Trash2,
  Users,
} from "lucide-react"

import { toast } from "@/lib/toast"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { CrewIcon } from "@/components/ui/crew-icon"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { StatusPill } from "@/components/ui/status-pill"
import { Switch } from "@/components/ui/switch"
import { PageError, PageLoading, PageNotFound } from "@/components/layout/page-state"
import { SubBar, SubBarIconButton, SubBarPrimary, SubBarSecondary } from "@/components/layout/sub-bar"
import { useJournalList } from "@/hooks/use-journal-list"
import { activityUrl } from "@/lib/activity-url"
import { useDeleteSkill, useSetSkillAssignment, useSetSkillLifecycle, useSkillDetail } from "@/hooks/use-skills"
import { entityHref } from "@/lib/entity-links"
import { formatRelativeShort } from "@/lib/time"
import { cn } from "@/lib/utils"
import { skillMarkdown, type SkillMeta } from "./skill-doc"
import type { SkillEditorTarget } from "./skill-editor"
import { SkillSourceMark } from "./skill-marks"
import {
  SKILL_TABS,
  attentionReasons,
  domainMeta,
  isArchived,
  isMadeHere,
  runtimeLabel,
  scanState,
  skillName,
  skillSource,
  skillTags,
  skillTrust,
  type SkillDetail,
  type SkillRow,
  type SkillTab,
  type SkillsAgent,
  type SkillsCrew,
} from "./skills-model"

// One skill (Harbor 2 #3396): "‹ Skills", the header with its source mark
// and facts, then Overview · SKILL.md · Used by N · Security · Version. The
// actions sit in the top bar: Activity, the ⋯ menu and Give to agents.

const TAB_LABEL: Record<SkillTab, string> = { overview: "Overview", md: "SKILL.md", used: "Used by", security: "Security", version: "Version" }

/** Where each runner finds an assigned skill (internal/orchestrator/skills_writer.go). */
export const RUNNERS: [string, string, string][] = [
  ["Claude Code", ".claude/skills/<slug>/SKILL.md", "Loads the body when a task needs it"],
  ["Codex CLI", ".agents/skills/<slug>/SKILL.md", "Folder convention of the open format"],
  ["OpenCode", ".opencode/skills/<slug>/SKILL.md", "Also reads .agents/skills"],
  ["Factory Droid", ".factory/skills/<slug>/SKILL.md", ""],
  ["Cursor", ".cursor/rules/<slug>.mdc", "As a rule file"],
  ["Gemini CLI", "only in the prompt", "No skill folder convention yet"],
]

function Card({ title, icon: Icon, right, className, children }: { title: string; icon?: typeof Layers; right?: React.ReactNode; className?: string; children: React.ReactNode }) {
  return (
    <section className={cn("min-w-0 overflow-hidden rounded-card border border-border bg-card", className)} aria-label={title}>
      <header className="flex items-center gap-2.5 px-4 pb-2 pt-3.5">
        {Icon && (
          <span className="icon-tile flex size-7.5 shrink-0 items-center justify-center rounded-md" aria-hidden>
            <Icon className="size-3.5" />
          </span>
        )}
        <h2 className="text-control font-semibold">{title}</h2>
        {right && <div className="ml-auto">{right}</div>}
      </header>
      {children}
    </section>
  )
}

function KV({ rows }: { rows: [string, React.ReactNode][] }) {
  return (
    <dl className="grid grid-cols-[minmax(7rem,auto)_minmax(0,1fr)] gap-x-4 gap-y-2 px-4 pb-4 text-control">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="min-w-0 [overflow-wrap:anywhere]">{v}</dd>
        </div>
      ))}
    </dl>
  )
}

function Chip({ icon: Icon, children }: { icon: typeof Layers; children: React.ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1.5 rounded-full border border-border bg-card/70 py-0.5 pl-1.5 pr-2 text-label text-muted-foreground">
      <Icon className="size-3.5" aria-hidden />
      {children}
    </span>
  )
}

function Good({ children }: { children: React.ReactNode }) {
  return (
    <span className="inline-flex items-start gap-1.5">
      <Check className="mt-0.5 size-3.5 shrink-0 text-success" aria-hidden />
      <span>{children}</span>
    </span>
  )
}

function Bad({ children }: { children: React.ReactNode }) {
  return (
    <span className="inline-flex items-start gap-1.5 text-destructive">
      <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden />
      <span>{children}</span>
    </span>
  )
}

/** SKILL.md as the CLI's `skill export` writes it: frontmatter + body. */
function exportMeta(s: SkillDetail): SkillMeta {
  return {
    name: s.slug,
    display_name: s.display_name,
    description: s.description,
    version: s.version,
    author: s.author,
    license: s.spdx_license || s.license,
    category: s.category,
    icon: s.icon,
    credential_requirements: s.needs_credentials ?? [],
    tags: skillTags(s),
  }
}

export function skillFile(s: SkillDetail): string {
  return skillMarkdown(exportMeta(s), s.content ?? "")
}

function downloadSkill(s: SkillDetail) {
  const blob = new Blob([skillFile(s)], { type: "text/markdown" })
  const url = URL.createObjectURL(blob)
  const a = document.createElement("a")
  a.href = url
  a.download = `${s.slug}.SKILL.md`
  a.click()
  URL.revokeObjectURL(url)
}

const ACTIVITY_TYPES = "skill.invoked,skill.assigned,skill.unassigned,skill.imported"

/** Activity narrowed to the work that called this skill (?walk=skill:<id>). */
export function skillActivityHref(id: string, label: string): string {
  return activityUrl("/activity", { path: { stops: [{ kind: "skill", id, label }], dropped: 0 }, lens: "workflows" })
}

function useSkillJournal(skill: SkillRow, workspaceId: string) {
  const { entries, loading, error } = useJournalList({ workspaceId, params: { entry_type: ACTIVITY_TYPES }, limit: 200 })
  const mine = useMemo(
    () =>
      entries.filter((e) => {
        const p = (e.payload ?? {}) as Record<string, unknown>
        return p.skill_id === skill.id || p.skill_slug === skill.slug || (e.entry_type === "skill.imported" && e.summary.endsWith(`: ${skill.slug}`))
      }),
    [entries, skill.id, skill.slug],
  )
  return { entries: mine, loading, error }
}

// ── Overview ─────────────────────────────────────────────────────────────

function OverviewTab({ skill, onTab }: { skill: SkillRow; onTab: (t: SkillTab) => void }) {
  const reasons = attentionReasons(skill)
  const holders = skill.installed_on ?? []
  const lacking = holders.filter((a) => (a.missing_credentials?.length ?? 0) > 0)
  const needs = skill.needs_credentials ?? []
  const usage = skill.usage
  return (
    <div className="flex flex-col gap-4">
      {reasons.length > 0 && (
        <Card title="Needs attention" icon={AlertTriangle}>
          <ul>
            {reasons.map((r) => (
              <li key={r.kind} className="flex min-h-12 items-center gap-3 border-t border-border px-4 py-2.5">
                <div className="min-w-0 flex-1">
                  <div className="text-control font-medium">{r.title}</div>
                  <div className="text-label text-muted-foreground">{r.detail}</div>
                </div>
                {r.kind === "credential" ? (
                  <Button asChild size="sm">
                    <Link href={entityHref({ kind: "credentials" })}>Assign</Link>
                  </Button>
                ) : (
                  <Button size="sm" variant="outline" onClick={() => onTab("security")}>
                    Review
                  </Button>
                )}
              </li>
            ))}
          </ul>
        </Card>
      )}
      <div className="grid items-start gap-4 lg:grid-cols-3">
        <Card title="When an agent uses it" icon={FileText} className="lg:col-span-2">
          <div className="flex flex-col gap-2 px-4 pb-4">
            <p className="text-body text-pretty" data-slot="skill-trigger">
              {skill.description ? `“${skill.description}”` : "This skill has no description, so agents cannot tell when to load it."}
            </p>
            <p className="text-label text-muted-foreground">
              This is the <span className="font-mono">description</span> from SKILL.md. Agents read it to decide when the skill applies; write it as “Use when…”.
            </p>
          </div>
        </Card>
        <Card title="Last 30 days" icon={Activity}>
          <KV
            rows={[
              ["Uses", <span key="u" className="tabular-nums">{usage?.uses_30d ?? usage?.uses_7d ?? 0}</span>],
              ["Last used", usage?.last_used_at ? formatRelativeShort(usage.last_used_at) : "Never"],
              ["Agents", String(holders.length)],
              ["Errors", <span key="e" className={cn("tabular-nums", (usage?.errors_30d ?? usage?.errors_7d) ? "text-destructive" : undefined)}>{usage?.errors_30d ?? usage?.errors_7d ?? 0}</span>],
            ]}
          />
        </Card>
      </div>
      <div className="grid items-start gap-4 lg:grid-cols-3">
        <Card title="How it reaches the agent" icon={Bot} className="lg:col-span-2">
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-control">
              <thead>
                <tr className="border-y border-border bg-muted/50 text-left text-label text-muted-foreground">
                  <th className="px-4 py-2 font-medium">Runner</th>
                  <th className="px-4 py-2 font-medium">Where the skill lands</th>
                  <th className="px-4 py-2 font-medium">
                    <span className="sr-only">Note</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {RUNNERS.map(([runner, where, note]) => (
                  <tr key={runner} className="border-b border-border last:border-0">
                    <td className="whitespace-nowrap px-4 py-2">{runner}</td>
                    <td className="px-4 py-2 font-mono text-label">{where.replace("<slug>", skill.slug)}</td>
                    <td className="px-4 py-2 text-label text-muted-foreground">{note}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
        <Card title="Needs" icon={KeyRound}>
          {needs.length === 0 ? (
            <p className="px-4 pb-4 text-label text-muted-foreground">No credentials. Uses only what the agent already has.</p>
          ) : (
            <ul className="pb-2">
              {needs.map((c) => {
                const missing = lacking.filter((a) => a.missing_credentials?.includes(c))
                return (
                  <li key={c} className="flex items-start gap-2.5 border-t border-border px-4 py-2.5">
                    <KeyRound className={cn("mt-0.5 size-3.5 shrink-0", missing.length ? "text-warn" : "text-muted-foreground")} aria-hidden />
                    <div className="min-w-0">
                      <div className="font-mono text-label">{c}</div>
                      <div className={cn("text-label", missing.length ? "text-warn" : "text-muted-foreground")}>
                        {missing.length ? (
                          <>
                            Missing for {missing.map((a) => a.agent_name).join(", ")} ·{" "}
                            <Link href={entityHref({ kind: "credentials" })} className="text-primary-hover hover:underline">
                              Vault
                            </Link>
                          </>
                        ) : holders.length ? (
                          "Configured for the agents that use it"
                        ) : (
                          "Give agents the credential with the skill"
                        )}
                      </div>
                    </div>
                  </li>
                )
              })}
            </ul>
          )}
        </Card>
      </div>
    </div>
  )
}

// ── SKILL.md ─────────────────────────────────────────────────────────────

function MdTab({ skill, canEdit, onEdit }: { skill: SkillDetail; canEdit: boolean; onEdit: () => void }) {
  const [mode, setMode] = useState<"preview" | "source">("preview")
  const [copied, setCopied] = useState(false)
  const full = skillFile(skill)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(full)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      toast.error("The browser blocked the clipboard. Download the file instead.")
    }
  }
  return (
    <section className="min-w-0 overflow-hidden rounded-card border border-border bg-card" aria-label="SKILL.md" data-slot="skill-md">
      <div className="flex flex-wrap items-center gap-2 border-b border-border px-3 py-2">
        <div role="radiogroup" aria-label="Show as" className="inline-flex rounded-md bg-foreground/[0.05] p-0.5">
          {(["preview", "source"] as const).map((m) => (
            <button
              key={m}
              type="button"
              role="radio"
              aria-checked={mode === m}
              onClick={() => setMode(m)}
              className={cn("kit-tap rounded px-2.5 py-1 text-label transition-colors", mode === m ? "bg-card text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")}
            >
              {m === "preview" ? "Preview" : "Source"}
            </button>
          ))}
        </div>
        <span className="text-micro tabular-nums text-muted-foreground-soft">{full.split("\n").length} lines</span>
        <span className="flex-1" />
        <Button variant="ghost" size="sm" onClick={copy}>
          {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
          {copied ? "Copied" : "Copy"}
        </Button>
        <Button variant="ghost" size="sm" onClick={() => downloadSkill(skill)}>
          <Download className="size-3.5" />
          Download
        </Button>
        {canEdit && (
          <Button variant="outline" size="sm" onClick={onEdit}>
            <Pencil className="size-3.5" />
            Edit
          </Button>
        )}
      </div>
      {mode === "source" ? (
        <pre className="overflow-x-auto whitespace-pre-wrap break-words px-4 py-3 font-mono text-label leading-relaxed text-foreground/85" data-slot="skill-md-source">
          {full}
        </pre>
      ) : (
        <div
          data-slot="skill-md-preview"
          className="max-w-[80ch] px-4 py-4 text-control leading-relaxed [&_h1]:mb-2 [&_h1]:text-heading [&_h1]:font-semibold [&_h2]:mb-2 [&_h2]:mt-5 [&_h2]:text-body [&_h2]:font-semibold [&_h3]:mb-1.5 [&_h3]:mt-4 [&_h3]:font-semibold [&_pre]:overflow-x-auto"
        >
          {skill.content ? <Streamdown>{skill.content}</Streamdown> : <p className="text-muted-foreground">This skill has no instructions.</p>}
        </div>
      )}
      {!canEdit && (
        <p className="border-t border-border px-4 py-2.5 text-label text-muted-foreground">
          From {skillSource(skill).label}; to change it, {skill.source === "BUNDLED" ? "copy it into this workspace" : "change the source or copy it into this workspace"}.
        </p>
      )}
    </section>
  )
}

// ── Used by ──────────────────────────────────────────────────────────────

const ACTIVITY_WORDS: Record<string, string> = {
  "skill.invoked": "used it",
  "skill.assigned": "was given it",
  "skill.unassigned": "lost it",
  "skill.imported": "Imported",
}

function RecentUses({ skill, workspaceId, agents }: { skill: SkillRow; workspaceId: string; agents: SkillsAgent[] }) {
  const { entries, loading, error } = useSkillJournal(skill, workspaceId)
  const name = (id?: string) => agents.find((a) => a.id === id)?.name ?? "An agent"
  return (
    <Card
      title="Recent uses"
      icon={Activity}
      right={
        <Link href={entityHref({ kind: "journal" })} className="text-label text-primary-hover hover:underline">
          Open Journal
        </Link>
      }
    >
      {loading ? (
        <p className="px-4 pb-4 text-label text-muted-foreground">Loading…</p>
      ) : error ? (
        <p className="px-4 pb-4 text-label text-muted-foreground">The journal could not be read.</p>
      ) : entries.length === 0 ? (
        <p className="px-4 pb-4 text-label text-muted-foreground">Nothing recorded yet. Giving, taking and every use by an agent appear here.</p>
      ) : (
        <ul data-slot="skill-recent-uses">
          {entries.slice(0, 30).map((e) => {
            const p = (e.payload ?? {}) as Record<string, unknown>
            const who = e.entry_type === "skill.imported" ? "" : `${name((p.agent_id as string) ?? e.agent_id)} `
            const failed = e.entry_type === "skill.invoked" && Number(p.exit_code ?? 0) !== 0
            return (
              <li key={e.id} className="grid grid-cols-[5.5rem_minmax(0,1fr)_auto] items-center gap-3 border-t border-border px-4 py-2 text-control">
                <span className="font-mono text-micro text-muted-foreground-soft">{formatRelativeShort(e.ts)}</span>
                <span className="min-w-0 truncate">
                  {who}
                  {ACTIVITY_WORDS[e.entry_type] ?? e.entry_type}
                </span>
                {failed ? <StatusPill className="font-sans tracking-normal" tone="danger" label="Error" /> : <span />}
              </li>
            )
          })}
        </ul>
      )}
    </Card>
  )
}

function UsedByTab({
  skill,
  agents,
  crews,
  workspaceId,
  canGive,
  recentRef,
}: {
  skill: SkillRow
  agents: SkillsAgent[]
  crews: SkillsCrew[]
  workspaceId: string
  canGive: boolean
  recentRef: React.RefObject<HTMLDivElement | null>
}) {
  const assign = useSetSkillAssignment(workspaceId)
  const [pending, setPending] = useState<Record<string, boolean>>({})
  const [failedCrew, setFailedCrew] = useState<{ crew: string; agents: SkillsAgent[]; on: boolean } | null>(null)
  const holders = useMemo(() => new Map((skill.installed_on ?? []).map((a) => [a.agent_id, a])), [skill.installed_on])
  const name = skillName(skill)

  const setOne = async (agent: SkillsAgent, on: boolean) => {
    setPending((p) => ({ ...p, [agent.id]: on }))
    try {
      await assign.mutateAsync({ agentId: agent.id, skillId: skill.id, on })
      toast.success(on ? `${agent.name} has ${name} from its next run` : `${agent.name} no longer has ${name}`)
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

  const setCrew = async (crew: string, members: SkillsAgent[], on: boolean) => {
    const todo = members.filter((a) => holders.has(a.id) !== on)
    setPending((p) => ({ ...p, ...Object.fromEntries(todo.map((a) => [a.id, on])) }))
    const results = await Promise.allSettled(todo.map((a) => assign.mutateAsync({ agentId: a.id, skillId: skill.id, on })))
    setPending({})
    const bad = todo.filter((_, i) => results[i].status === "rejected")
    if (bad.length) {
      setFailedCrew({ crew, agents: bad, on })
      toast.error(`${bad.length} of ${todo.length} changes failed: ${bad.map((a) => a.name).join(", ")}`)
    } else {
      setFailedCrew(null)
      toast.success(on ? `${name} given to ${todo.length} ${todo.length === 1 ? "agent" : "agents"} in ${crew}` : `${name} taken from ${crew}`)
    }
  }

  const groups = [
    ...crews.map((c) => ({ crew: c as SkillsCrew | null, members: agents.filter((a) => a.crew_id === c.id) })),
    { crew: null, members: agents.filter((a) => !a.crew_id || !crews.some((c) => c.id === a.crew_id)) },
  ].filter((g) => g.members.length > 0)

  return (
    <div className="flex flex-col gap-4" data-slot="skill-used-by">
      {skillTrust(skill).level === "flagged" && (
        <div className="page-banner page-banner-warn" role="note">
          <AlertTriangle className="size-4 shrink-0" aria-hidden />
          <span>The import scan flagged this skill. Read its SKILL.md before giving it to another agent.</span>
        </div>
      )}
      {failedCrew && (
        <div className="page-banner page-banner-warn" role="alert">
          <AlertTriangle className="size-4 shrink-0" aria-hidden />
          <span>Not changed for {failedCrew.agents.map((a) => a.name).join(", ")}.</span>
          <span className="flex-1" />
          <Button size="sm" variant="outline" onClick={() => void setCrew(failedCrew.crew, failedCrew.agents, failedCrew.on)}>
            Retry
          </Button>
        </div>
      )}
      {groups.length === 0 && <p className="text-control text-muted-foreground">This workspace has no agents yet. Create a crew to give skills to its agents.</p>}
      {groups.map(({ crew, members }) => {
        const on = members.filter((a) => pending[a.id] ?? holders.has(a.id)).length
        const state: boolean | "indeterminate" = on === 0 ? false : on === members.length ? true : "indeterminate"
        const label = crew?.name ?? "No crew"
        return (
          <section key={crew?.id ?? "none"} className="overflow-hidden rounded-card border border-border bg-card" aria-label={label} data-slot="skill-used-by-crew">
            <header className="flex flex-wrap items-center gap-2.5 px-4 py-3">
              {crew ? (
                <CrewIcon icon={crew.icon ?? "users"} color={crew.color} size="sm" className="h-5.5 w-5.5 rounded-md [&>svg]:h-3 [&>svg]:w-3" />
              ) : (
                <Users className="size-4 text-muted-foreground" aria-hidden />
              )}
              <h2 className="text-control font-semibold">{label}</h2>
              <span className="text-micro tabular-nums text-muted-foreground-soft">
                {on}/{members.length}
              </span>
              <span className="flex-1" />
              {canGive && (
                <Button
                  size="sm"
                  variant="ghost"
                  aria-pressed={state === "indeterminate" ? "mixed" : state}
                  onClick={() => void setCrew(label, members, state !== true)}
                >
                  {state === true ? (crew ? "Take from the whole crew" : "Take from all") : crew ? "Give to the whole crew" : "Give to all"}
                </Button>
              )}
            </header>
            <ul>
              {members.map((a) => {
                const held = pending[a.id] ?? holders.has(a.id)
                const missing = holders.get(a.id)?.missing_credentials ?? []
                return (
                  <li key={a.id} className="flex min-h-12 items-center gap-2.5 border-t border-border px-4 py-1.5">
                    <AgentAvatar seed={a.avatar_seed ?? a.slug} style={a.avatar_style} avatarUrl={a.avatar_url} alt="" width={26} height={26} className="h-6.5 w-6.5 shrink-0 rounded-full bg-foreground/[0.04]" />
                    <div className="min-w-0 flex-1">
                      <Link href={entityHref({ kind: "agent", slug: a.slug })} className="block truncate text-control hover:underline">
                        {a.name}
                      </Link>
                      {a.role_title && <span className="block truncate text-label text-muted-foreground">{a.role_title}</span>}
                    </div>
                    {held && missing.length > 0 && (
                      <Link href={entityHref({ kind: "credentials" })} className="inline-flex items-center gap-1 text-label text-warn hover:underline">
                        <KeyRound className="size-3" aria-hidden />
                        no {missing.join(", ")}
                      </Link>
                    )}
                    <Switch
                      checked={held}
                      disabled={!canGive || a.id in pending}
                      onCheckedChange={(v) => void setOne(a, v)}
                      aria-label={`${a.name} has ${name}`}
                    />
                  </li>
                )
              })}
            </ul>
          </section>
        )
      })}
      <p className="text-label text-muted-foreground">Managers and above give or take skills. A change applies from the agent’s next run.</p>
      <div ref={recentRef} className="scroll-mt-4">
        <RecentUses skill={skill} workspaceId={workspaceId} agents={agents} />
      </div>
    </div>
  )
}

// ── Security and Version ─────────────────────────────────────────────────

function SecurityTab({ skill, canGive, onTakeFromAll }: { skill: SkillRow; canGive: boolean; onTakeFromAll: () => void }) {
  const scan = scanState(skill)
  const licence = skill.spdx_license || (skill as Partial<SkillDetail>).license
  const verified = (skill.verification ?? "").toUpperCase() === "VERIFIED"
  const scripts = runtimeLabel(skill.runtime) !== "Instructions"
  const holders = skill.installed_on?.length ?? 0
  return (
    <div className="flex flex-col gap-4" data-slot="skill-security">
      {scan === "flagged" && (
        <div className="page-banner page-banner-warn" role="alert">
          <AlertTriangle className="size-4 shrink-0" aria-hidden />
          <span>
            <b>Flagged skills can still be given to agents today.</b> {holders} {holders === 1 ? "agent has" : "agents have"} it now.
          </span>
          <span className="flex-1" />
          {canGive && holders > 0 && (
            <Button size="sm" onClick={onTakeFromAll}>
              Take it from all agents
            </Button>
          )}
        </div>
      )}
      <Card title="Checks in Crewship" icon={Lock}>
        <KV
          rows={[
            ["License", licence ? <Good key="l">{licence}</Good> : <span key="l" className="text-muted-foreground">{isMadeHere(skill) ? "None (written here)" : "Not stated"}</span>],
            [
              "Injection scan",
              scan === "flagged" ? (
                <Bad key="s">Flagged · {skill.description_quality || "suspicious instructions"}</Bad>
              ) : scan === "clean" ? (
                <Good key="s">Clean</Good>
              ) : (
                <span key="s" className="text-warn">Never scanned</span>
              ),
            ],
            ["Curator review", verified ? <Good key="c">Verified</Good> : <span key="c" className="text-muted-foreground">Not reviewed yet</span>],
            ["Runs code", scripts ? <span key="r" className="text-warn">Yes · {runtimeLabel(skill.runtime).toLowerCase()} run in the crew container</span> : "No · instructions only"],
          ]}
        />
      </Card>
    </div>
  )
}

function VersionTab({ skill }: { skill: SkillRow }) {
  const src = skillSource(skill)
  return (
    <Card title="Version" icon={Layers}>
      <KV
        rows={[
          ["Source", src.url ? <a key="s" href={src.url} target="_blank" rel="noreferrer" className="text-primary-hover hover:underline">{src.label}</a> : src.label],
          ["Version", <span key="v" className="tabular-nums">{skill.version || "—"}</span>],
          ["Updated", skill.updated_at ? formatRelativeShort(skill.updated_at) : "—"],
        ]}
      />
      <p className="border-t border-border px-4 py-2.5 text-label text-muted-foreground">Importing again replaces the skill by its slug.</p>
    </Card>
  )
}

// ── the page ─────────────────────────────────────────────────────────────

export interface SkillPageProps {
  workspaceId: string
  skillId: string
  /** The row from the list, shown while the detail loads. */
  row?: SkillRow
  agents: SkillsAgent[]
  crews: SkillsCrew[]
  tab: SkillTab
  onTab: (t: SkillTab) => void
  onBack: () => void
  /** Opens the Give to picker for this skill. */
  onGive: (skill: SkillRow) => void
  onDeleted: () => void
  onEditor: (t: SkillEditorTarget) => void
  canGive: boolean
  canManage: boolean
}

export function SkillPage({ workspaceId, skillId, row, agents, crews, tab, onTab, onBack, onGive, onDeleted, onEditor, canGive, canManage }: SkillPageProps) {
  const { data, isLoading, isError, error, refetch } = useSkillDetail(workspaceId, skillId)
  const del = useDeleteSkill(workspaceId)
  const lifecycle = useSetSkillLifecycle(workspaceId)
  const assign = useSetSkillAssignment(workspaceId)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const recentRef = useRef<HTMLDivElement>(null)
  const skill: SkillRow | undefined = data ?? row

  const back = (
    <button type="button" onClick={onBack} className="kit-tap inline-flex items-center gap-1 self-start text-label text-muted-foreground hover:text-foreground" data-slot="skill-back">
      <ChevronLeft className="size-3.5" aria-hidden />
      Skills
    </button>
  )

  if (!skill) {
    const missing = isError && /\b404\b/.test(error instanceof Error ? error.message : "")
    return (
      <div className="mx-auto flex w-full max-w-[1240px] flex-col gap-3.5 p-4 md:p-6">
        <SubBar title="Skills" heading={false} />
        {back}
        {isError ? (
          missing ? (
            <PageNotFound what="skill" description="It may have been removed from the library, or the link is wrong." back={{ label: "Back to skills", onClick: onBack }} />
          ) : (
            <PageError title="This skill could not be loaded" description="The library is unchanged." onRetry={() => void refetch()} />
          )
        ) : (
          <PageLoading variant="list" rows={4} />
        )}
      </div>
    )
  }

  const detail = data
  const trust = skillTrust(skill)
  const src = skillSource(skill)
  const builtIn = skill.source === "BUNDLED"
  const canEdit = !builtIn && !!detail
  const holders = skill.installed_on ?? []
  const licence = skill.spdx_license || detail?.license

  const setLifecycle = async (action: "archive" | "restore") => {
    try {
      await lifecycle.mutateAsync({ skillId: skill.id, action })
      toast.success(action === "archive" ? `${skillName(skill)} archived · agents keep it until removed` : `${skillName(skill)} restored to the library`)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Could not change the skill")
    }
  }

  const takeFromAll = async () => {
    const results = await Promise.allSettled(holders.map((a) => assign.mutateAsync({ agentId: a.agent_id, skillId: skill.id, on: false })))
    const bad = results.filter((r) => r.status === "rejected").length
    if (bad) toast.error(`${bad} of ${holders.length} agents still have ${skillName(skill)}`)
    else toast.success(`${skillName(skill)} taken from ${holders.length} ${holders.length === 1 ? "agent" : "agents"}`)
  }

  return (
    <div className="mx-auto flex w-full max-w-[1240px] flex-col gap-3.5 p-4 pb-24 md:p-6" data-slot="skill-page">
      <SubBar
        title="Skills"
        section={`${src.label} › ${skillName(skill)}`}
        heading={false}
        actions={
          <>
            {/* Named apart from the shell's own Activity button in the same bar. */}
            <SubBarSecondary asChild icon={Activity} className="max-sm:hidden">
              <Link href={skillActivityHref(skill.id, skillName(skill))} aria-label="Skill activity">
                Activity
              </Link>
            </SubBarSecondary>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <SubBarIconButton icon={MoreHorizontal} aria-label="Skill options" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-64">
                {canEdit && (
                  <DropdownMenuItem onSelect={() => detail && onEditor({ kind: "edit", skill: detail })}>
                    <Pencil className="size-3.5" />
                    Edit
                  </DropdownMenuItem>
                )}
                <DropdownMenuItem disabled={!detail} onSelect={() => detail && onEditor({ kind: "duplicate", skill: detail })}>
                  <CopyPlus className="size-3.5" />
                  Copy into this workspace
                </DropdownMenuItem>
                <DropdownMenuItem disabled={!detail} onSelect={() => detail && downloadSkill(detail)}>
                  <Download className="size-3.5" />
                  Download SKILL.md
                </DropdownMenuItem>
                {src.url && (
                  <DropdownMenuItem asChild>
                    <a href={src.url} target="_blank" rel="noreferrer">
                      <ExternalLink className="size-3.5" />
                      Open source
                    </a>
                  </DropdownMenuItem>
                )}
                {canManage && (
                  <>
                    <DropdownMenuSeparator />
                    {isArchived(skill) ? (
                      <DropdownMenuItem onSelect={() => setLifecycle("restore")}>
                        <ArchiveRestore className="size-3.5" />
                        Restore to the library
                      </DropdownMenuItem>
                    ) : (
                      <DropdownMenuItem disabled={builtIn} onSelect={() => setLifecycle("archive")}>
                        <Archive className="size-3.5" />
                        Archive
                      </DropdownMenuItem>
                    )}
                    <DropdownMenuItem variant="destructive" disabled={builtIn} onSelect={() => setConfirmDelete(true)}>
                      <Trash2 className="size-3.5" />
                      Remove from library…
                    </DropdownMenuItem>
                    {builtIn && <p className="px-2 pb-1.5 text-micro text-muted-foreground-soft">Built-in skills ship with Crewship. Copy one into this workspace to change it.</p>}
                  </>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
            {canGive && (
              <SubBarPrimary icon={Bot} onClick={() => onGive(skill)}>
                Give to agents
              </SubBarPrimary>
            )}
          </>
        }
      />
      {back}

      <div className="flex items-start gap-4" data-slot="skill-head">
        <SkillSourceMark skill={skill} size={52} />
        <div className="grid min-w-0 gap-1.5">
          <h1 className="text-title font-semibold tracking-tight">{skillName(skill)}</h1>
          {skillName(skill) !== skill.slug && <p className="font-mono text-label text-muted-foreground-soft">{skill.slug}</p>}
          {skill.description && <p className="text-control text-muted-foreground">{skill.description}</p>}
          <div className="flex flex-wrap items-center gap-1.5" data-slot="skill-chips">
            {isMadeHere(skill) && skill.version && <span className="rounded-md bg-muted px-2 py-0.5 text-label font-medium tabular-nums">Published v{skill.version}</span>}
            <StatusPill className="font-sans tracking-normal" tone={trust.tone} label={trust.label} />
            <Chip icon={src.kind === "repo" ? Layers : FileText}>{src.label}</Chip>
            <Chip icon={Layers}>{domainMeta(skill.category).label}</Chip>
            {licence && <Chip icon={Lock}>{licence}</Chip>}
            <Chip icon={runtimeLabel(skill.runtime) === "Instructions" ? FileText : Terminal}>{runtimeLabel(skill.runtime)}</Chip>
          </div>
        </div>
      </div>

      <div role="tablist" aria-label="Skill sections" className="flex gap-5 overflow-x-auto border-b border-border [scrollbar-width:none]">
        {SKILL_TABS.map((t) => (
          <button
            key={t}
            type="button"
            role="tab"
            aria-selected={tab === t}
            onClick={() => onTab(t)}
            className={cn(
              "-mb-px shrink-0 whitespace-nowrap border-b-2 py-2 text-control transition-colors",
              tab === t ? "border-foreground font-medium text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            {TAB_LABEL[t]}
            {t === "used" && ` ${holders.length}`}
          </button>
        ))}
      </div>

      <div role="tabpanel" aria-label={TAB_LABEL[tab]}>
        {tab === "overview" && <OverviewTab skill={skill} onTab={onTab} />}
        {tab === "md" && (detail ? <MdTab skill={detail} canEdit={canEdit} onEdit={() => onEditor({ kind: "edit", skill: detail })} /> : isLoading ? <PageLoading rows={3} /> : null)}
        {tab === "used" && <UsedByTab skill={skill} agents={agents} crews={crews} workspaceId={workspaceId} canGive={canGive} recentRef={recentRef} />}
        {tab === "security" && <SecurityTab skill={skill} canGive={canGive} onTakeFromAll={() => void takeFromAll()} />}
        {tab === "version" && <VersionTab skill={skill} />}
      </div>

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Remove ${skillName(skill)} from the library?`}
        description="It is deleted from this workspace's catalog."
        consequences={[
          { tone: "lost", text: `${holders.length} ${holders.length === 1 ? "agent stops" : "agents stop"} loading it on the next run` },
          { tone: "kept", text: "Past runs and the journal keep their record" },
          { tone: "warn", text: "To get it back, import its SKILL.md again — download it first if you have no copy" },
        ]}
        confirmLabel="Remove"
        destructive
        onConfirm={async () => {
          try {
            await del.mutateAsync(skill.id)
            toast.success(`${skillName(skill)} removed from the library`)
            onDeleted()
          } catch (e) {
            toast.error(e instanceof Error ? e.message : "Could not remove the skill")
            throw e
          }
        }}
      />
    </div>
  )
}
