"use client"

/**
 * Give to agents — a secret page's primary action (Harbor 2). Picks agents,
 * crews or the whole workspace and the variable they get it under, and
 * writes one binding per target: the rows `crewship credential bind --slot`
 * writes. The value never passes through this dialog.
 *
 * Targets that already have this secret are shown as such and not written
 * again. A failure keeps the dialog open with what did not land; what did
 * land is kept.
 */

import * as React from "react"
import { Bot, Building2, Users } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceRefusal,
} from "@/components/layout/create-surface"
import type { CredentialCrewRef } from "@/hooks/use-credential-readiness"
import { getBrand } from "@/lib/credential-providers/registry"
import { defaultEnvVarName } from "@/lib/credential-provider"
import { isValidEnvVarName } from "@/lib/env-var-name"
import { toast } from "@/lib/toast"
import { cn } from "@/lib/utils"
import { agentsByCrew, bindingKey, createBinding, targetKey, useBindingTargets, type BindingTarget } from "./bindings"

type Who = "AGENT" | "CREW" | "WORKSPACE"

export interface GiveToAgentsDialogProps {
  workspaceId: string
  credential: { id: string; name: string; provider: string }
  open: boolean
  onOpenChange: (open: boolean) => void
  crewsById: Record<string, CredentialCrewRef>
  onGiven: () => void
}

export function GiveToAgentsDialog({ workspaceId, credential, open, onOpenChange, crewsById, onGiven }: GiveToAgentsDialogProps) {
  const { agents, bindings, error: loadError, loading } = useBindingTargets(workspaceId, credential.id, open)
  const [who, setWho] = React.useState<Who>("AGENT")
  const [picked, setPicked] = React.useState<Set<string>>(new Set())
  const [query, setQuery] = React.useState("")
  const [slot, setSlot] = React.useState("")
  const [slotTouched, setSlotTouched] = React.useState(false)
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)

  React.useEffect(() => {
    if (!open) { setWho("AGENT"); setPicked(new Set()); setQuery(""); setSlot(""); setSlotTouched(false); setError(null) }
  }, [open])

  // The variable it already goes by, else the service's usual one.
  const suggested = React.useMemo(() => {
    const own = (bindings ?? []).find((b) => b.credential_id === credential.id)?.slot
    return own ?? defaultEnvVarName(getBrand(credential.provider))
  }, [bindings, credential.id, credential.provider])
  React.useEffect(() => { if (!slotTouched) setSlot(suggested) }, [suggested, slotTouched])

  const has = React.useMemo(() => new Set((bindings ?? []).filter((b) => b.credential_id === credential.id).map(bindingKey)), [bindings, credential.id])
  const crewName = (id: string) => crewsById[id]?.name
  const q = query.trim().toLowerCase()
  const groups = agentsByCrew((agents ?? []).filter((a) => !q || a.name.toLowerCase().includes(q) || (a.crew_id && (crewName(a.crew_id) ?? "").toLowerCase().includes(q))), crewName)
  const crews = Object.values(crewsById).filter((c) => !q || c.name.toLowerCase().includes(q)).sort((a, b) => a.name.localeCompare(b.name))

  const targets: BindingTarget[] =
    who === "WORKSPACE"
      ? has.has("WORKSPACE") ? [] : [{ scope: "WORKSPACE" }]
      : [...picked]
          .filter((k) => k.startsWith(`${who}:`))
          .map((k) => (who === "AGENT" ? { scope: "AGENT" as const, agentId: k.slice(6) } : { scope: "CREW" as const, crewId: k.slice(5) }))
  const slotOk = isValidEnvVarName(slot.trim())
  const canSubmit = targets.length > 0 && slotOk && !loading && !loadError

  const toggle = (key: string) => setPicked((s) => { const n = new Set(s); if (n.has(key)) n.delete(key); else n.add(key); return n })

  const submit = async () => {
    if (!canSubmit || busy) return
    setBusy(true)
    setError(null)
    const failed: string[] = []
    const landed: string[] = []
    for (const t of targets) {
      const err = await createBinding(workspaceId, credential.id, t, slot.trim())
      if (err) failed.push(`${label(t)}: ${err}`)
      else landed.push(targetKey(t))
    }
    setBusy(false)
    if (landed.length > 0) onGiven()
    if (failed.length > 0) {
      setPicked((s) => { const n = new Set(s); for (const k of landed) n.delete(k); return n })
      setError(`${landed.length > 0 ? `Given to ${landed.length}. ` : ""}Not given — ${failed.join("; ")}`)
      return
    }
    toast.success(who === "WORKSPACE" ? `${credential.name} is given to the whole workspace` : `${credential.name} given to ${landed.length} ${who === "AGENT" ? "agent" : "crew"}${landed.length === 1 ? "" : "s"}`)
    onOpenChange(false)
  }

  function label(t: BindingTarget): string {
    if (t.scope === "WORKSPACE") return "the workspace"
    if (t.scope === "CREW") return crewName(t.crewId) ?? "a crew"
    return (agents ?? []).find((a) => a.id === t.agentId)?.name ?? "an agent"
  }

  return (
    <CreateSurface open={open} onOpenChange={(o) => { if (!busy) onOpenChange(o) }} size="md" dirty={picked.size > 0} discardLabel="this assignment" onSubmit={() => void submit()}>
      <CreateSurfaceHeader
        icon={Bot}
        context="Vault"
        title="Give to agents"
        description={<><span className="font-medium text-foreground">{credential.name}</span> reaches them through the crew proxy or as a variable. Nobody sees the value.</>}
        onClose={() => { if (!busy) onOpenChange(false) }}
      />
      <CreateSurfaceBody className="space-y-4">
        <div role="radiogroup" aria-label="Who gets it" className="grid grid-cols-3 gap-1 rounded-md border border-border bg-muted/40 p-0.5">
          {([["AGENT", "Agents", Bot], ["CREW", "Crews", Users], ["WORKSPACE", "Whole workspace", Building2]] as const).map(([v, text, Icon]) => (
            <button key={v} type="button" role="radio" aria-checked={who === v} onClick={() => setWho(v)}
              className={cn("kit-tap inline-flex h-8 items-center justify-center gap-1.5 rounded text-label", who === v ? "bg-card font-medium text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground")}>
              <Icon className="size-3.5" aria-hidden /> {text}
            </button>
          ))}
        </div>

        {loadError ? (
          <p role="alert" className="text-label text-warn">Agents or assignments could not be loaded. Close and try again.</p>
        ) : loading ? (
          <p role="status" className="text-label text-muted-foreground">Loading agents…</p>
        ) : who === "WORKSPACE" ? (
          <p className="text-label text-muted-foreground">
            {has.has("WORKSPACE") ? "Every agent in the workspace already gets it." : "Every agent in the workspace gets it, unless its crew or the agent has its own under the same variable."}
          </p>
        ) : (
          <>
            <Input value={query} onChange={(e) => setQuery(e.target.value)} placeholder={who === "AGENT" ? "Search agents or crews…" : "Search crews…"} aria-label={who === "AGENT" ? "Search agents" : "Search crews"} className="h-8 text-control" />
            <div className="max-h-72 space-y-3 overflow-y-auto" aria-label={who === "AGENT" ? "Agents" : "Crews"} role="group">
              {who === "AGENT" ? (
                groups.length === 0 ? <p className="text-label text-muted-foreground">No agents found.</p> : groups.map((g) => (
                  <fieldset key={g.crewId ?? "none"} className="space-y-0.5">
                    <legend className="px-1 text-micro font-semibold uppercase tracking-wide text-muted-foreground">{g.name}</legend>
                    {g.agents.map((a) => {
                      const key = `AGENT:${a.id}`
                      const already = has.has(key)
                      return (
                        <label key={a.id} className={cn("flex items-center gap-2 rounded-md px-1 py-1 text-control", already ? "opacity-70" : "cursor-pointer hover:bg-muted/60")}>
                          <Checkbox checked={already || picked.has(key)} disabled={already} onCheckedChange={() => toggle(key)} aria-label={`Give to ${a.name}`} />
                          <AgentAvatar seed={a.id} alt="" className="size-5 shrink-0 rounded-full" />
                          <span className="min-w-0 flex-1 truncate">{a.name}</span>
                          {already && <span className="text-label text-muted-foreground">Already has it</span>}
                        </label>
                      )
                    })}
                  </fieldset>
                ))
              ) : crews.length === 0 ? (
                <p className="text-label text-muted-foreground">No crews found.</p>
              ) : (
                crews.map((c) => {
                  const key = `CREW:${c.id}`
                  const already = has.has(key)
                  return (
                    <label key={c.id} className={cn("flex items-center gap-2 rounded-md px-1 py-1 text-control", already ? "opacity-70" : "cursor-pointer hover:bg-muted/60")}>
                      <Checkbox checked={already || picked.has(key)} disabled={already} onCheckedChange={() => toggle(key)} aria-label={`Give to ${c.name}`} />
                      <span className="min-w-0 flex-1 truncate">{c.name}</span>
                      {already && <span className="text-label text-muted-foreground">Already has it</span>}
                    </label>
                  )
                })
              )}
            </div>
          </>
        )}

        <CreateSurfaceField label="Variable name" htmlFor="give-slot">
          <Input id="give-slot" value={slot} onChange={(e) => { setSlotTouched(true); setSlot(e.target.value) }} className="h-8 font-mono text-control" aria-invalid={!slotOk || undefined} autoComplete="off" spellCheck={false} />
          <p className="mt-1 text-label text-muted-foreground">{slotOk ? "What the agent's environment calls it. An existing assignment is never overwritten." : "Use letters, digits and underscores, for example GH_TOKEN."}</p>
        </CreateSurfaceField>
      </CreateSurfaceBody>
      <CreateSurfaceRefusal message={error} />
      <CreateSurfaceFooter
        onCancel={() => onOpenChange(false)}
        primaryLabel={busy ? "Giving…" : who === "WORKSPACE" ? "Give to the workspace" : `Give to ${targets.length || ""} ${who === "AGENT" ? "agent" : "crew"}${targets.length === 1 ? "" : "s"}`.replace("  ", " ")}
        onPrimary={() => void submit()}
        primaryDisabled={!canSubmit}
        busy={busy}
      />
    </CreateSurface>
  )
}
