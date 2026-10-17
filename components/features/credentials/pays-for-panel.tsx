"use client"

/**
 * An AI provider account's "Pays for" tab (Harbor 2): the agents that think
 * with this account, by crew, each with a switch. A switch is one AGENT-scope
 * binding under the account's slot; "Pay for the whole crew" is one CREW
 * binding; "Use as workspace default" is the WORKSPACE binding. An agent uses
 * the account bound to it, then its crew's, then the workspace's.
 *
 * Only agents whose runner can use this provider are listed — a Codex agent
 * cannot pay with a Claude subscription, and offering the switch would only
 * produce a failing run.
 */

import * as React from "react"
import { Building2, Users } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Button } from "@/components/ui/button"
import { CrewIcon } from "@/components/ui/crew-icon"
import { Switch } from "@/components/ui/switch"
import type { CredentialCrewRef } from "@/hooks/use-credential-readiness"
import {
  adapterLabel,
  loginBindingSlot,
  providerRunners,
  runnerFits,
  type LoginCredential,
  type ProviderLogin,
} from "@/lib/credentials/provider-logins"
import { toast } from "@/lib/toast"
import { agentsByCrew, bindingKey, createBinding, deleteBinding, targetKey, useBindingTargets, type BindingTarget } from "./bindings"
import { providerLabel } from "./provider-accounts"
import { SettingsCard } from "./secret-settings"

export interface PaysForPanelProps {
  workspaceId: string
  credential: LoginCredential & { login: ProviderLogin }
  /** OWNER/ADMIN — the server's manage_bindings answer. */
  canBind: boolean
  crewsById: Record<string, CredentialCrewRef>
  onChanged: () => void
}

export function PaysForPanel({ workspaceId, credential, canBind, crewsById, onChanged }: PaysForPanelProps) {
  const { login } = credential
  const provider = login.provider ?? credential.provider
  const slot = loginBindingSlot(login)
  const { agents, bindings, setBindings, error, loading, reload } = useBindingTargets(workspaceId, credential.id)
  const [busy, setBusy] = React.useState<Set<string>>(new Set())

  const mine = React.useMemo(() => {
    const m = new Map<string, string>()
    for (const b of bindings ?? []) if (b.credential_id === credential.id) m.set(bindingKey(b), b.id)
    return m
  }, [bindings, credential.id])

  const fit = (agents ?? []).filter((a) => runnerFits(a.cli_adapter, provider))
  const unfit = (agents ?? []).length - fit.length
  const crews = agentsByCrew(fit, (id) => crewsById[id]?.name)

  const toggle = async (target: BindingTarget, label: string) => {
    if (!slot) return
    const key = targetKey(target)
    if (busy.has(key)) return
    setBusy((s) => new Set(s).add(key))
    const existing = mine.get(key)
    const err = existing ? await deleteBinding(workspaceId, existing) : await createBinding(workspaceId, credential.id, target, slot)
    setBusy((s) => { const n = new Set(s); n.delete(key); return n })
    if (err) {
      toast.error(existing ? `Couldn't stop paying for ${label}` : `Couldn't pay for ${label}`, { description: err })
      return
    }
    if (existing) {
      setBindings((rows) => (rows ?? []).filter((b) => b.id !== existing))
    } else {
      // The new row's id comes with the next read; reload rather than guess it.
      reload()
    }
    onChanged()
  }

  if (error) {
    return <p role="alert" className="text-label text-warn">Agents or assignments could not be loaded. Nothing was changed.</p>
  }
  if (loading) return <p role="status" className="text-label text-muted-foreground">Loading agents…</p>
  if (!slot) {
    return <p className="text-label text-muted-foreground">This account does not say how it reaches an agent, so it cannot be assigned here. Use <code className="font-mono">crewship credential bind</code>.</p>
  }

  const ws = mine.has("WORKSPACE")
  return (
    <div className="flex flex-col gap-4" data-slot="pays-for">
      {!canBind && (
        <p role="note" className="text-label text-muted-foreground">Only workspace owners and admins change which agents an account pays for.</p>
      )}
      <SettingsCard title="Workspace default" icon={Building2}>
        <div className="flex flex-wrap items-center gap-3 px-4 py-3">
          <p className="min-w-0 flex-1 text-label text-muted-foreground">
            {ws ? "Pays for every agent that has no account of its own or of its crew." : "Agents without their own or their crew's account do not use it."}
          </p>
          {canBind && (
            <Button size="sm" variant="outline" disabled={busy.has("WORKSPACE")} onClick={() => void toggle({ scope: "WORKSPACE" }, "the workspace")}>
              {ws ? "Stop being the default" : "Use as workspace default"}
            </Button>
          )}
        </div>
      </SettingsCard>

      {crews.length === 0 && (
        <p className="text-label text-muted-foreground">No agent here runs on {providerRunners(provider).join(" or ")}.</p>
      )}
      {crews.map((g) => {
        const crewKey = g.crewId ? `CREW:${g.crewId}` : null
        const wholeCrew = crewKey ? mine.has(crewKey) : false
        const crew = g.crewId ? crewsById[g.crewId] : undefined
        return (
          <section key={g.crewId ?? "none"} aria-label={g.name} className="overflow-hidden rounded-card border border-border bg-card">
            <header className="flex flex-wrap items-center gap-2 border-b border-border px-4 py-2.5">
              {g.crewId ? <CrewIcon icon={crew?.icon ?? ""} color={crew?.color ?? undefined} size="sm" className="!size-5 !rounded shrink-0" /> : <Users className="size-4 text-muted-foreground" aria-hidden />}
              <b className="min-w-0 flex-1 truncate text-control font-semibold text-foreground">{g.name}</b>
              {g.crewId && canBind && (
                <Button size="sm" variant="ghost" disabled={busy.has(crewKey!)} onClick={() => void toggle({ scope: "CREW", crewId: g.crewId! }, g.name)}>
                  {wholeCrew ? "Stop paying for the crew" : "Pay for the whole crew"}
                </Button>
              )}
              {wholeCrew && <span className="text-label text-muted-foreground">Pays for the whole crew</span>}
            </header>
            <ul className="flex flex-col">
              {g.agents.map((a) => {
                const key = `AGENT:${a.id}`
                const on = mine.has(key)
                return (
                  <li key={a.id} className="flex items-center gap-3 border-t border-border px-4 py-2 first:border-t-0">
                    <AgentAvatar seed={a.id} alt="" className="size-6 shrink-0 rounded-full" />
                    <span className="grid min-w-0 flex-1 leading-tight">
                      <span className="truncate text-control text-foreground">{a.name}</span>
                      <small className="truncate text-label text-muted-foreground">{adapterLabel(a.cli_adapter)}</small>
                    </span>
                    <Switch
                      checked={on}
                      disabled={!canBind || busy.has(key)}
                      onCheckedChange={() => void toggle({ scope: "AGENT", agentId: a.id }, a.name)}
                      aria-label={`${credential.name} pays for ${a.name}`}
                    />
                  </li>
                )
              })}
            </ul>
          </section>
        )
      })}
      <p className="text-label text-muted-foreground">
        Only agents whose runner can use {providerLabel(provider)} are listed ({providerRunners(provider).join(", ")}){unfit > 0 ? `; ${unfit} other agent${unfit === 1 ? " is" : "s are"} not` : ""}. An agent uses the account bound to it, then its crew&apos;s, then the workspace default.
      </p>
    </div>
  )
}
