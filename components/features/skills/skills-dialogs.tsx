"use client"

import { useEffect, useMemo, useState } from "react"
import { toast } from "sonner"
import { KeyRound, Users } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { CrewIcon } from "@/components/ui/crew-icon"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { StatusPill } from "@/components/ui/status-pill"
import { useSetSkillAssignment } from "@/hooks/use-skills"
import { SkillTile } from "./skill-card"
import { skillName, skillTrust, type SkillRow, type SkillsAgent, type SkillsCrew } from "./skills-model"

// The Skills page's two pickers (#3033). Both pickers
// edit a draft and apply the difference on Apply: a list of checkboxes is a
// form, unlike the Agents tab, whose switches save one at a time.

async function applyDiff(
  pairs: { agentId: string; skillId: string; on: boolean }[],
  run: (p: { agentId: string; skillId: string; on: boolean }) => Promise<void>,
): Promise<number> {
  const results = await Promise.allSettled(pairs.map(run))
  return results.filter((r) => r.status === "rejected").length
}

export type AssignTarget = { mode: "skill"; skill: SkillRow } | { mode: "agent"; agent: SkillsAgent }

export function AssignDialog({
  target,
  workspaceId,
  skills,
  agents,
  crews,
  onClose,
}: {
  target: AssignTarget | null
  workspaceId: string
  skills: SkillRow[]
  agents: SkillsAgent[]
  crews: SkillsCrew[]
  onClose: () => void
}) {
  const assign = useSetSkillAssignment(workspaceId)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)

  // What is held now: agents holding the skill, or skills the agent holds.
  const initial = useMemo(() => {
    if (!target) return new Set<string>()
    if (target.mode === "skill") return new Set((target.skill.installed_on ?? []).map((a) => a.agent_id))
    return new Set(skills.filter((s) => (s.installed_on ?? []).some((a) => a.agent_id === target.agent.id)).map((s) => s.id))
  }, [target, skills])
  useEffect(() => setSelected(new Set(initial)), [initial])

  if (!target) return null
  const added = [...selected].filter((x) => !initial.has(x))
  const removed = [...initial].filter((x) => !selected.has(x))
  const toggle = (id: string) =>
    setSelected((s) => {
      const next = new Set(s)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  const apply = async () => {
    setBusy(true)
    const pairs =
      target.mode === "skill"
        ? [...added.map((agentId) => ({ agentId, skillId: target.skill.id, on: true })), ...removed.map((agentId) => ({ agentId, skillId: target.skill.id, on: false }))]
        : [...added.map((skillId) => ({ agentId: target.agent.id, skillId, on: true })), ...removed.map((skillId) => ({ agentId: target.agent.id, skillId, on: false }))]
    const failed = await applyDiff(pairs, (p) => assign.mutateAsync(p))
    setBusy(false)
    if (failed) {
      toast.error(`${failed} of ${pairs.length} changes failed. The others were applied.`)
      return
    }
    toast.success(
      target.mode === "skill"
        ? `${skillName(target.skill)} is on ${selected.size} ${selected.size === 1 ? "agent" : "agents"}`
        : `${target.agent.name} has ${selected.size} ${selected.size === 1 ? "skill" : "skills"}`,
    )
    onClose()
  }

  const needs = target.mode === "skill" ? (target.skill.needs_credentials ?? []) : []
  const missingFor = (agentId: string) => (target.mode === "skill" ? (target.skill.installed_on ?? []).find((a) => a.agent_id === agentId)?.missing_credentials ?? [] : [])

  return (
    <Dialog open onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent className="flex max-h-[min(640px,calc(100dvh-48px))] flex-col gap-0 p-0 sm:max-w-lg">
        <DialogHeader className="px-5 pb-3 pt-5">
          <DialogTitle>{target.mode === "skill" ? `Assign ${skillName(target.skill)}` : `Skills for ${target.agent.name}`}</DialogTitle>
          <DialogDescription>
            {target.mode === "skill"
              ? "Pick agents or a whole crew. They load the skill on their next run."
              : `Checked skills load on ${target.agent.name}'s next run.`}
            {target.mode === "skill" && skillTrust(target.skill).level === "flagged" && (
              <span className="mt-1 block text-destructive">The import scan flagged this skill. Read its SKILL.md first.</span>
            )}
          </DialogDescription>
        </DialogHeader>
        <div className="min-h-0 flex-1 overflow-y-auto border-t border-border">
          {target.mode === "skill"
            ? [...crews.map((c) => ({ crew: c as SkillsCrew | null, members: agents.filter((a) => a.crew_id === c.id) })), { crew: null, members: agents.filter((a) => !a.crew_id) }]
                .filter((g) => g.members.length)
                .map(({ crew, members }) => {
                  const n = members.filter((a) => selected.has(a.id)).length
                  const state: boolean | "indeterminate" = n === 0 ? false : n === members.length ? true : "indeterminate"
                  return (
                    <div key={crew?.id ?? "none"} className="border-b border-border last:border-0">
                      <label className="flex min-h-11 cursor-pointer items-center gap-2.5 px-5 py-2 text-control font-semibold">
                        <Checkbox
                          checked={state}
                          onCheckedChange={() =>
                            setSelected((s) => {
                              const next = new Set(s)
                              const all = members.every((a) => next.has(a.id))
                              for (const a of members) {
                                if (all) next.delete(a.id)
                                else next.add(a.id)
                              }
                              return next
                            })
                          }
                        />
                        {crew ? (
                          <CrewIcon icon={crew.icon ?? "users"} color={crew.color} size="sm" className="h-[18px] w-[18px] rounded-md [&>svg]:h-3 [&>svg]:w-3" />
                        ) : (
                          <Users className="h-3.5 w-3.5 text-muted-foreground" />
                        )}
                        {crew?.name ?? "No crew"}
                        <span className="ml-auto font-mono text-micro font-normal tabular-nums text-muted-foreground-soft">
                          {n}/{members.length}
                        </span>
                      </label>
                      {members.map((a) => {
                        const missing = selected.has(a.id) ? missingFor(a.id) : []
                        return (
                          <label key={a.id} className="row-hover flex min-h-11 cursor-pointer items-center gap-2.5 py-1.5 pl-10 pr-5 text-control">
                            <Checkbox checked={selected.has(a.id)} onCheckedChange={() => toggle(a.id)} />
                            <AgentAvatar
                              seed={a.avatar_seed ?? a.slug}
                              style={a.avatar_style}
                              avatarUrl={a.avatar_url}
                              alt=""
                              width={20}
                              height={20}
                              className="h-5 w-5 rounded-full bg-foreground/[0.04]"
                            />
                            {a.name}
                            <span className="flex-1" />
                            {missing.length > 0 ? (
                              <span className="inline-flex items-center gap-1 text-micro text-warn">
                                <KeyRound className="h-3 w-3" />
                                no {missing.join(", ")}
                              </span>
                            ) : (
                              !selected.has(a.id) && needs.length > 0 && <span className="text-micro text-muted-foreground-soft">needs {needs.join(", ")}</span>
                            )}
                          </label>
                        )
                      })}
                    </div>
                  )
                })
            : [...skills]
                .sort((a, b) => skillName(a).localeCompare(skillName(b)))
                .map((s) => {
                  const t = skillTrust(s)
                  return (
                    <label key={s.id} className="row-hover flex min-h-11 cursor-pointer items-center gap-2.5 border-b border-border/50 px-5 py-1.5 text-control last:border-0">
                      <Checkbox checked={selected.has(s.id)} onCheckedChange={() => toggle(s.id)} />
                      <SkillTile category={s.category} icon={s.icon} size="sm" />
                      <span className="min-w-0 flex-1 truncate">{skillName(s)}</span>
                      <StatusPill tone={t.tone} label={t.label} />
                    </label>
                  )
                })}
        </div>
        <DialogFooter className="flex-row items-center gap-2 border-t border-border px-5 py-3">
          <span className="mr-auto text-label tabular-nums text-muted-foreground">
            {added.length} added · {removed.length} removed
          </span>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button onClick={apply} disabled={busy || (added.length === 0 && removed.length === 0)}>
            {busy ? "Applying…" : "Apply"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
