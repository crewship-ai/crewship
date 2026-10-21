"use client"

/**
 * New account group — the panel's "+" in the AI providers space. A name, the
 * provider and way of paying (from the accounts already connected) and the
 * accounts to start with. The group's own page opens once it exists; its
 * order and the rest are edited there.
 */

import * as React from "react"
import { useQueryClient } from "@tanstack/react-query"
import { Layers } from "lucide-react"

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
import { poolKeys, poolRequest, type ProviderPool } from "@/hooks/use-provider-pools"
import { ownerLabel, type LoginCredential } from "@/lib/credentials/provider-logins"
import { providerLabel } from "./provider-accounts"
import { LoginBrandMark } from "./provider-login-bits"

export function NewAccountGroupDialog({
  workspaceId,
  open,
  onOpenChange,
  accounts,
  onCreated,
}: {
  workspaceId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  accounts: LoginCredential[]
  onCreated: (id: string) => void
}) {
  const client = useQueryClient()
  const [name, setName] = React.useState("")
  const [kind, setKind] = React.useState("")
  const [ids, setIds] = React.useState<string[]>([])
  const [cross, setCross] = React.useState(false)
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)

  React.useEffect(() => {
    if (!open) { setName(""); setKind(""); setIds([]); setCross(false); setError(null) }
  }, [open])

  const kinds = [...new Set(accounts.filter((a) => a.login).map((a) => `${a.login!.provider}:${a.login!.mode}`))]
  const [provider, mode] = kind ? kind.split(":") : ["", ""]
  const eligible = accounts.filter((a) => a.login?.provider === provider && a.login?.mode === mode)
  const valid = name.trim().length > 0 && Boolean(kind) && ids.length > 0 && ids.length <= 100

  const submit = async () => {
    if (!valid || busy) return
    setBusy(true)
    setError(null)
    try {
      const res = await poolRequest(workspaceId, "provider-logins/pools", {
        method: "POST",
        body: JSON.stringify({ name: name.trim(), provider, mode, allow_cross_owner: cross, members: ids.map((id, i) => ({ credential_id: id, priority: i })) }),
      })
      const created = (await res.json().catch(() => null)) as ProviderPool | null
      await client.invalidateQueries({ queryKey: poolKeys.all(workspaceId) })
      onOpenChange(false)
      if (created?.id) onCreated(created.id)
    } catch (err) {
      setError(err instanceof Error ? err.message : "Couldn't create the group.")
    } finally {
      setBusy(false)
    }
  }

  return (
    <CreateSurface open={open} onOpenChange={(o) => { if (!busy) onOpenChange(o) }} size="sm" dirty={Boolean(name || ids.length)} discardLabel="this account group" onSubmit={() => void submit()}>
      <CreateSurfaceHeader
        icon={Layers}
        context="Vault"
        title="New account group"
        description="Accounts of one provider, in an order of preference. Groups do not assign accounts or enable automatic switching yet."
        onClose={() => { if (!busy) onOpenChange(false) }}
      />
      <CreateSurfaceBody className="space-y-4">
        <CreateSurfaceField label="Name" htmlFor="new-group-name">
          <Input id="new-group-name" value={name} maxLength={200} onChange={(e) => setName(e.target.value)} placeholder="Engineering seats" />
        </CreateSurfaceField>
        <CreateSurfaceField label="Provider" htmlFor="new-group-kind">
          <select
            id="new-group-kind"
            value={kind}
            onChange={(e) => { setKind(e.target.value); setIds([]) }}
            className="h-9 w-full rounded-md border border-border bg-background px-2.5 text-control text-foreground outline-none focus:border-primary"
          >
            <option value="">Choose from your connected accounts</option>
            {kinds.map((k) => {
              const [p, m] = k.split(":")
              return <option key={k} value={k}>{providerLabel(p)} · {m === "api_key" ? "API key" : "Subscription"}</option>
            })}
          </select>
        </CreateSurfaceField>
        {kind && (
          <fieldset className="space-y-1">
            <legend className="mb-1 text-label font-medium text-foreground">Accounts · {ids.length} selected</legend>
            {eligible.map((a) => {
              const on = ids.includes(a.id)
              return (
                <label key={a.id} className="flex cursor-pointer items-center gap-2 rounded-md px-1 py-1 text-control hover:bg-muted/60">
                  <Checkbox checked={on} onCheckedChange={() => setIds(on ? ids.filter((x) => x !== a.id) : [...ids, a.id])} aria-label={`Include ${a.name}`} />
                  <LoginBrandMark provider={a.login?.provider ?? a.provider} size="sm" />
                  <span className="min-w-0 flex-1 truncate">{a.name}</span>
                  <span className="truncate text-label text-muted-foreground">{ownerLabel(a)}</span>
                </label>
              )
            })}
          </fieldset>
        )}
        <label className="flex items-start gap-2 text-control">
          <Checkbox checked={cross} onCheckedChange={(v) => setCross(v === true)} aria-label="Allow accounts owned by different people" className="mt-0.5" />
          <span>
            Allow accounts owned by different people
            <span className="block text-label text-muted-foreground">Only with the owners&apos; consent. Provider terms and shared limits still apply.</span>
          </span>
        </label>
      </CreateSurfaceBody>
      <CreateSurfaceRefusal message={error} />
      <CreateSurfaceFooter
        onCancel={() => onOpenChange(false)}
        primaryLabel={busy ? "Creating…" : "Create group"}
        onPrimary={() => void submit()}
        primaryDisabled={!valid}
        busy={busy}
      />
    </CreateSurface>
  )
}
