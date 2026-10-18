"use client"

import { useEffect, useMemo, useState, type ReactNode } from "react"
import { KeyRound, Users } from "lucide-react"

import { toast } from "@/lib/toast"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Checkbox } from "@/components/ui/checkbox"
import { CrewIcon } from "@/components/ui/crew-icon"
import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceNotice,
  CreateSurfaceRefusal,
} from "@/components/layout/create-surface"
import { Input } from "@/components/ui/input"
import { useSetSkillAssignment } from "@/hooks/use-skills"
import { cn } from "@/lib/utils"
import { skillName, skillTrust, type SkillRow, type SkillsAgent, type SkillsCrew } from "./skills-model"

// "Give to" (Harbor 2 #3396): the one picker that gives one skill or several
// to agents and crews — from the skill page and from the library's bulk bar.
// It edits a draft and Apply sends only the difference; a partial failure keeps
// the dialog open with the agents that failed and a Retry.

export interface AssignmentPair {
  agentId: string
  skillId: string
  on: boolean
}

type Check = boolean | "indeterminate"

/** Agent → how it holds the skills now: all, some or none of them. */
export function holdingState(skills: SkillRow[], agentId: string): Check {
  const n = skills.filter((s) => (s.installed_on ?? []).some((a) => a.agent_id === agentId)).length
  return n === 0 ? false : n === skills.length ? true : "indeterminate"
}

/** The writes that turn what agents hold into what the picker says. */
export function assignmentDiff(skills: SkillRow[], initial: Map<string, Check>, next: Map<string, Check>): AssignmentPair[] {
  const out: AssignmentPair[] = []
  for (const [agentId, want] of next) {
    if (want === "indeterminate" || want === initial.get(agentId)) continue
    for (const s of skills) {
      const has = (s.installed_on ?? []).some((a) => a.agent_id === agentId)
      if (has !== want) out.push({ agentId, skillId: s.id, on: want })
    }
  }
  return out
}

/** The chrome both directions share: surface, search, refusal, "N added · M removed" and Apply/Retry. */
function GiveToShell({
  title, description, context, search, notice, children, added, removed, diffCount, failedCount, failedMessage, busy, onClose, onRun,
}: {
  title: string
  description: string
  context?: string
  search: { value: string; onChange: (v: string) => void; placeholder: string; label: string }
  notice?: ReactNode
  children: ReactNode
  added: number
  removed: number
  diffCount: number
  failedCount: number
  failedMessage: string | null
  busy: boolean
  onClose: () => void
  /** Submit: retries what failed, else applies the diff. */
  onRun: () => void
}) {
  return (
    <CreateSurface
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      size="sm"
      dirty={diffCount > 0 && failedCount === 0}
      discardLabel="these changes"
      onSubmit={onRun}
    >
      <CreateSurfaceHeader concept="skills" context={context} title={title} description={description} onClose={() => !busy && onClose()} />
      <CreateSurfaceBody padded={false} data-slot="give-to-picker">
        <div className="sticky top-0 z-10 border-b border-border bg-card px-4 py-2.5 sm:px-5">
          <Input
            value={search.value}
            onChange={(e) => search.onChange(e.target.value)}
            placeholder={search.placeholder}
            aria-label={search.label}
            className={CREATE_SURFACE_INPUT}
          />
        </div>
        {notice}
        {children}
      </CreateSurfaceBody>
      <CreateSurfaceRefusal message={failedMessage} />
      <CreateSurfaceFooter
        lead={
          <span className="mr-auto text-label tabular-nums text-muted-foreground">
            {added} added · {removed} removed
          </span>
        }
        hint={null}
        onCancel={onClose}
        guardCancel={failedCount === 0}
        primaryLabel={failedCount ? "Retry" : busy ? "Applying…" : "Apply"}
        onPrimary={onRun}
        primaryDisabled={failedCount === 0 && diffCount === 0}
        busy={busy}
      />
    </CreateSurface>
  )
}

export interface GiveToPickerProps {
  /** Skills → agents (default). */
  direction?: "skills-to-agents"
  /** The skills being given; null closes the picker. */
  skills: SkillRow[] | null
  workspaceId: string
  agents: SkillsAgent[]
  crews: SkillsCrew[]
  onClose: () => void
}

function GiveToAgentsPicker({ skills, workspaceId, agents, crews, onClose }: GiveToPickerProps) {
  const assign = useSetSkillAssignment(workspaceId)
  const list = useMemo(() => skills ?? [], [skills])
  const initial = useMemo(() => new Map(agents.map((a) => [a.id, holdingState(list, a.id)])), [agents, list])
  const [draft, setDraft] = useState<Map<string, Check>>(initial)
  const [query, setQuery] = useState("")
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState<AssignmentPair[]>([])
  const open = skills !== null && skills.length > 0

  useEffect(() => {
    if (!open) return
    setDraft(new Map(initial))
    setQuery("")
    setFailed([])
    // Reset per opening, not on every refetch of the holders.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, skills])

  const diff = useMemo(() => assignmentDiff(list, initial, draft), [list, initial, draft])
  const changedAgents = (on: boolean) => new Set(diff.filter((p) => p.on === on).map((p) => p.agentId)).size
  const added = changedAgents(true)
  const removed = changedAgents(false)

  const q = query.trim().toLowerCase()
  const groups = useMemo(() => {
    const visible = (a: SkillsAgent) => !q || a.name.toLowerCase().includes(q)
    return [
      ...crews.map((c) => ({ crew: c as SkillsCrew | null, members: agents.filter((a) => a.crew_id === c.id) })),
      { crew: null, members: agents.filter((a) => !a.crew_id || !crews.some((c) => c.id === a.crew_id)) },
    ]
      .map((g) => ({ ...g, members: g.crew && q && g.crew.name.toLowerCase().includes(q) ? g.members : g.members.filter(visible) }))
      .filter((g) => g.members.length > 0)
  }, [agents, crews, q])

  if (!open) return null

  const set = (ids: string[], v: boolean) =>
    setDraft((d) => {
      const next = new Map(d)
      for (const id of ids) next.set(id, v)
      return next
    })

  const run = async (pairs: AssignmentPair[]) => {
    setBusy(true)
    const results = await Promise.allSettled(pairs.map((p) => assign.mutateAsync(p)))
    setBusy(false)
    const bad = pairs.filter((_, i) => results[i].status === "rejected")
    if (bad.length > 0) {
      setFailed(bad)
      return
    }
    setFailed([])
    const what = list.length === 1 ? skillName(list[0]) : `${list.length} skills`
    toast.success(
      added && removed
        ? `${what}: given to ${added} and taken from ${removed} ${removed === 1 ? "agent" : "agents"}`
        : removed
          ? `${what} taken from ${removed} ${removed === 1 ? "agent" : "agents"}`
          : `${what} given to ${added} ${added === 1 ? "agent" : "agents"} · from their next run`,
    )
    onClose()
  }

  const failedNames = [...new Set(failed.map((p) => agents.find((a) => a.id === p.agentId)?.name ?? p.agentId))]
  const flagged = list.some((s) => skillTrust(s).level === "flagged")
  const needs = [...new Set(list.flatMap((s) => s.needs_credentials ?? []))]
  const title = list.length === 1 ? `Give ${skillName(list[0])} to agents` : `Give ${list.length} skills to agents`

  return (
    <GiveToShell
      title={title}
      description={list.length === 1 ? "Pick agents or a whole crew. They load the skill on their next run." : "Checked agents get every selected skill on their next run."}
      search={{ value: query, onChange: setQuery, placeholder: "Search crews and agents…", label: "Search crews and agents" }}
      notice={
        flagged ? (
          <div className="px-4 pt-3 sm:px-5">
            <CreateSurfaceNotice tone="warn" icon={KeyRound}>
              The import scan flagged {list.length === 1 ? "this skill" : "a selected skill"}. Read its SKILL.md before giving it.
            </CreateSurfaceNotice>
          </div>
        ) : null
      }
      added={added}
      removed={removed}
      diffCount={diff.length}
      failedCount={failed.length}
      failedMessage={failed.length ? `${failed.length} ${failed.length === 1 ? "change" : "changes"} failed for ${failedNames.join(", ")}. The others were applied.` : null}
      busy={busy}
      onClose={onClose}
      onRun={() => {
        if (busy) return
        if (failed.length) void run(failed)
        else if (diff.length) void run(diff)
      }}
    >
        {groups.length === 0 && <p className="px-5 py-6 text-center text-label text-muted-foreground">{agents.length ? "No crew or agent matches." : "This workspace has no agents yet."}</p>}
        {groups.map(({ crew, members }) => {
          const on = members.filter((a) => draft.get(a.id) === true).length
          const some = members.some((a) => draft.get(a.id) !== false)
          const state: Check = on === members.length ? true : some ? "indeterminate" : false
          const name = crew?.name ?? "No crew"
          return (
            <div key={crew?.id ?? "none"} className="border-b border-border last:border-0" role="group" aria-label={name}>
              <label className="flex min-h-[44px] cursor-pointer items-center gap-2.5 px-4 py-2 text-control font-semibold sm:px-5">
                <Checkbox
                  checked={state}
                  aria-label={`Give to the whole ${name} crew`}
                  onCheckedChange={() => set(members.map((a) => a.id), state !== true)}
                />
                {crew ? (
                  <CrewIcon icon={crew.icon ?? "users"} color={crew.color} size="sm" className="h-5 w-5 rounded-md [&>svg]:h-3 [&>svg]:w-3" />
                ) : (
                  <Users className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />
                )}
                {name}
                <span className="ml-auto text-micro font-normal tabular-nums text-muted-foreground-soft">
                  {on}/{members.length}
                </span>
              </label>
              {members.map((a) => {
                const value = draft.get(a.id) ?? false
                const missing = list.flatMap((s) => (s.installed_on ?? []).find((h) => h.agent_id === a.id)?.missing_credentials ?? [])
                return (
                  <label key={a.id} className="row-hover flex min-h-[44px] cursor-pointer items-center gap-2.5 py-1.5 pl-10 pr-4 text-control sm:pr-5">
                    <Checkbox checked={value} aria-label={a.name} onCheckedChange={() => set([a.id], value !== true)} />
                    <AgentAvatar
                      seed={a.avatar_seed ?? a.slug}
                      style={a.avatar_style}
                      avatarUrl={a.avatar_url}
                      alt=""
                      width={20}
                      height={20}
                      className="h-5 w-5 rounded-full bg-foreground/[0.04]"
                    />
                    <span className="min-w-0 truncate">{a.name}</span>
                    <span className="flex-1" />
                    {missing.length > 0 ? (
                      <span className="inline-flex items-center gap-1 text-micro text-warn">
                        <KeyRound className="h-3 w-3" aria-hidden />
                        no {[...new Set(missing)].join(", ")}
                      </span>
                    ) : (
                      value === false &&
                      needs.length > 0 && <span className={cn("hidden text-micro text-muted-foreground-soft sm:inline")}>needs {needs.join(", ")}</span>
                    )}
                  </label>
                )
              })}
            </div>
          )
        })}
    </GiveToShell>
  )
}

// ── Direction "agent-to-skills" (CRW-03, #3481) ──────────────────────────────
// "Give skills to {agent}" from a crew's Tools and access: one agent, the
// workspace's skills. Same draft, diff-only Apply and Retry as above; the
// caller owns the library and the writes (the agent API addresses a removal by
// assignment id, not skill id).

export interface LibrarySkill {
  id: string
  name: string
  display_name?: string | null
  category?: string | null
  description?: string | null
}

export type HeldSkillChange = { kind: "add"; skillId: string } | { kind: "remove"; skillId: string; assignmentId: string }

/** skill id → assignment id for what the agent holds; `draft` is what should be held. */
export function heldSkillsDiff(held: Map<string, string>, draft: Set<string>) {
  return {
    add: [...draft].filter((id) => !held.has(id)),
    remove: [...held].filter(([id]) => !draft.has(id)).map(([skillId, assignmentId]) => ({ skillId, assignmentId })),
  }
}

export interface GiveToSkillsProps {
  direction: "agent-to-skills"
  agent: { id: string; name: string }
  /** The workspace's skills; null while loading. */
  library: LibrarySkill[] | null
  loadFailed?: boolean
  /** skill id → assignment id for what the agent holds now. */
  held: Map<string, string>
  /** One write; rejects on failure. */
  write: (change: HeldSkillChange) => Promise<void>
  /** After any write landed, so the caller reloads the agent's skills. */
  onApplied?: () => void
  onClose: () => void
}

const skillLabel = (s: { name: string; display_name?: string | null }) => s.display_name || s.name

function GiveToSkillsPicker({ agent, library, loadFailed, held, write, onApplied, onClose }: GiveToSkillsProps) {
  const [draft, setDraft] = useState<Set<string>>(() => new Set(held.keys()))
  const [query, setQuery] = useState("")
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState<HeldSkillChange[]>([])

  const diff = useMemo(() => heldSkillsDiff(held, draft), [held, draft])
  const changes: HeldSkillChange[] = useMemo(
    () => [...diff.add.map((skillId) => ({ kind: "add" as const, skillId })), ...diff.remove.map((r) => ({ kind: "remove" as const, ...r }))],
    [diff],
  )
  const q = query.trim().toLowerCase()
  const shown = (library ?? []).filter((s) => !q || [s.name, s.display_name, s.category].some((f) => f?.toLowerCase().includes(q)))
  const nameOf = (id: string) => {
    const s = library?.find((x) => x.id === id)
    return s ? skillLabel(s) : id
  }
  const toggle = (id: string) =>
    setDraft((d) => {
      const next = new Set(d)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  const run = async (batch: HeldSkillChange[]) => {
    if (batch.length === 0) return
    setBusy(true)
    const results = await Promise.allSettled(batch.map(write))
    setBusy(false)
    const bad = batch.filter((_, i) => results[i].status === "rejected")
    if (bad.length < batch.length) onApplied?.()
    if (bad.length > 0) {
      setFailed(bad)
      return
    }
    setFailed([])
    const added = batch.filter((c) => c.kind === "add").length
    const removed = batch.length - added
    toast.success(
      added && removed
        ? `${agent.name}: ${added} given, ${removed} taken away · from its next run`
        : removed
          ? `${removed} ${removed === 1 ? "skill" : "skills"} taken from ${agent.name}`
          : `${added} ${added === 1 ? "skill" : "skills"} given to ${agent.name} · from its next run`,
    )
    onClose()
  }

  const failedNames = [...new Set(failed.map((c) => nameOf(c.skillId)))]

  return (
    <GiveToShell
      title={`Give skills to ${agent.name}`}
      context={agent.name}
      description="Checked skills load on its next run. A skill is instructions; it never runs anything by itself."
      search={{ value: query, onChange: setQuery, placeholder: "Search skills…", label: "Search skills" }}
      added={diff.add.length}
      removed={diff.remove.length}
      diffCount={changes.length}
      failedCount={failed.length}
      failedMessage={failed.length ? `${failed.length} ${failed.length === 1 ? "change" : "changes"} failed for ${failedNames.join(", ")}. The others were applied.` : null}
      busy={busy}
      onClose={onClose}
      onRun={() => {
        if (!busy) void run(failed.length ? failed : changes)
      }}
    >
      {loadFailed ? (
        <p role="alert" className="px-5 py-6 text-center text-label text-muted-foreground">The workspace’s skills could not be loaded.</p>
      ) : library === null ? (
        <p role="status" className="px-5 py-6 text-center text-label text-muted-foreground">Loading skills…</p>
      ) : shown.length === 0 ? (
        <p className="px-5 py-6 text-center text-label text-muted-foreground">{library.length ? "No skill matches." : "The workspace has no skills yet. Add them on Skills."}</p>
      ) : (
        <div role="group" aria-label="Skills">
          {shown.map((s) => (
            <label key={s.id} className="row-hover flex min-h-[44px] cursor-pointer items-center gap-2.5 border-b border-border px-4 py-1.5 text-control last:border-0 sm:px-5">
              <Checkbox checked={draft.has(s.id)} aria-label={skillLabel(s)} onCheckedChange={() => toggle(s.id)} />
              <span className="min-w-0 flex-1">
                <span className="block truncate">{skillLabel(s)}</span>
                {(s.category || s.description) && (
                  <span className="block truncate text-micro text-muted-foreground">{[s.category, s.description].filter(Boolean).join(" · ")}</span>
                )}
              </span>
            </label>
          ))}
        </div>
      )}
    </GiveToShell>
  )
}

/** The one Give to picker: skills → agents by default, or one agent → skills. */
export function GiveToPicker(props: GiveToPickerProps | GiveToSkillsProps) {
  return props.direction === "agent-to-skills" ? <GiveToSkillsPicker {...props} /> : <GiveToAgentsPicker {...props} />
}
