"use client"

import { useEffect, useMemo, useState } from "react"
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

export interface GiveToPickerProps {
  /** The skills being given; null closes the picker. */
  skills: SkillRow[] | null
  workspaceId: string
  agents: SkillsAgent[]
  crews: SkillsCrew[]
  onClose: () => void
}

export function GiveToPicker({ skills, workspaceId, agents, crews, onClose }: GiveToPickerProps) {
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
    <CreateSurface
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      size="sm"
      dirty={diff.length > 0 && failed.length === 0}
      discardLabel="these changes"
      onSubmit={() => {
        if (busy) return
        if (failed.length) void run(failed)
        else if (diff.length) void run(diff)
      }}
    >
      <CreateSurfaceHeader
        concept="skills"
        title={title}
        description={list.length === 1 ? "Pick agents or a whole crew. They load the skill on their next run." : "Checked agents get every selected skill on their next run."}
        onClose={() => !busy && onClose()}
      />
      <CreateSurfaceBody padded={false} data-slot="give-to-picker">
        <div className="sticky top-0 z-10 border-b border-border bg-card px-4 py-2.5 sm:px-5">
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search crews and agents…"
            aria-label="Search crews and agents"
            className={CREATE_SURFACE_INPUT}
          />
        </div>
        {flagged && (
          <div className="px-4 pt-3 sm:px-5">
            <CreateSurfaceNotice tone="warn" icon={KeyRound}>
              The import scan flagged {list.length === 1 ? "this skill" : "a selected skill"}. Read its SKILL.md before giving it.
            </CreateSurfaceNotice>
          </div>
        )}
        {groups.length === 0 && <p className="px-5 py-6 text-center text-label text-muted-foreground">{agents.length ? "No crew or agent matches." : "This workspace has no agents yet."}</p>}
        {groups.map(({ crew, members }) => {
          const on = members.filter((a) => draft.get(a.id) === true).length
          const some = members.some((a) => draft.get(a.id) !== false)
          const state: Check = on === members.length ? true : some ? "indeterminate" : false
          const name = crew?.name ?? "No crew"
          return (
            <div key={crew?.id ?? "none"} className="border-b border-border last:border-0" role="group" aria-label={name}>
              <label className="flex min-h-11 cursor-pointer items-center gap-2.5 px-4 py-2 text-control font-semibold sm:px-5">
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
                <span className="ml-auto font-mono text-micro font-normal tabular-nums text-muted-foreground-soft">
                  {on}/{members.length}
                </span>
              </label>
              {members.map((a) => {
                const value = draft.get(a.id) ?? false
                const missing = list.flatMap((s) => (s.installed_on ?? []).find((h) => h.agent_id === a.id)?.missing_credentials ?? [])
                return (
                  <label key={a.id} className="row-hover flex min-h-11 cursor-pointer items-center gap-2.5 py-1.5 pl-10 pr-4 text-control sm:pr-5">
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
      </CreateSurfaceBody>
      <CreateSurfaceRefusal
        message={failed.length ? `${failed.length} ${failed.length === 1 ? "change" : "changes"} failed for ${failedNames.join(", ")}. The others were applied.` : null}
      />
      <CreateSurfaceFooter
        lead={
          <span className="mr-auto text-label tabular-nums text-muted-foreground">
            {added} added · {removed} removed
          </span>
        }
        hint={null}
        onCancel={onClose}
        guardCancel={failed.length === 0}
        primaryLabel={failed.length ? "Retry" : busy ? "Applying…" : "Apply"}
        onPrimary={() => void run(failed.length ? failed : diff)}
        primaryDisabled={failed.length === 0 && diff.length === 0}
        busy={busy}
      />
    </CreateSurface>
  )
}
