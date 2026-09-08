"use client"

/**
 * "Pays with" — which provider login this agent runs on (PRD provider-logins
 * §6.3, wireframe AgentPaysWith).
 *
 * The seat is an AGENT-scope binding, so picking one here writes the same row
 * `crewship credential binding create --scope AGENT` writes — nothing new in
 * the data model. What is new is the question being asked next to the CLI
 * adapter instead of three screens away: an adapter and a seat that disagree
 * (Codex CLI paying with a Claude login) are caught where the choice is made,
 * not as a 401 in a run.
 *
 * Every seat in the workspace is listed. The ones of the adapter's provider
 * are selectable; the others are visible and disabled — "wrong provider" —
 * because a picker that hides them makes the operator wonder where the seat
 * they just added went.
 */

import * as React from "react"
import { AlertTriangle, Check, ChevronDown, CreditCard, Plus } from "lucide-react"
import Link from "next/link"
import { toast } from "sonner"

import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { useAbilities } from "@/hooks/use-abilities"
import { apiFetch } from "@/lib/api-fetch"
import { getBrand } from "@/lib/credential-providers/registry"
import {
  adapterLabel,
  adapterProvider,
  deriveLoginStatus,
  formatClock,
  formatExpiresIn,
  loginBindingSlot,
  modeLabel,
  planLabel,
  type LoginCredential,
  type ProviderLogin,
} from "@/lib/credentials/provider-logins"
import { LoginBrandMark, LoginStatusPill } from "@/components/features/credentials/provider-login-bits"
import { cn } from "@/lib/utils"

import { ConfigRow } from "../canvas/config-field"
import type { AgentPaysWith } from "./types"

interface BindingRow {
  id: string
  credential_id: string
  scope: string
  agent_id: string | null
  slot: string
}

export interface PaysWithRowProps {
  workspaceId: string
  agentId: string
  agentName: string
  cliAdapter: string
  /** `pays_with` from GET /agents/{id}, when the server sends it. */
  paysWith?: AgentPaysWith | null
  /** After a binding is written — the canvas may want to re-read `pays_with`. */
  onChanged?: () => void
}

export function PaysWithRow({ workspaceId, agentId, agentName, cliAdapter, paysWith, onChanged }: PaysWithRowProps) {
  const { abilities } = useAbilities()
  const canBind = abilities.can("manage", "Credential")
  const [seats, setSeats] = React.useState<LoginCredential[]>([])
  const [bindings, setBindings] = React.useState<BindingRow[]>([])
  const [loaded, setLoaded] = React.useState(false)
  const [loadError, setLoadError] = React.useState<string | null>(null)
  const [open, setOpen] = React.useState(false)
  const [saving, setSaving] = React.useState(false)
  const [snapshotStale, setSnapshotStale] = React.useState(false)
  React.useEffect(() => { setSnapshotStale(false) }, [paysWith])
  const snapshot = snapshotStale ? null : paysWith

  const provider = adapterProvider(cliAdapter)
  const ws = encodeURIComponent(workspaceId)

  // Loaded when the picker opens, not when the tab does. The resting state
  // reads `pays_with` off the agent the tab already has; the list and the
  // agent's bindings are only needed to change it (and to know whether a
  // pool partner exists), so the tab does not fire two requests per agent
  // for a row most visits never touch.
  const load = React.useCallback(async () => {
    setLoadError(null)
    try {
      const [listRes, bindRes] = await Promise.all([
        apiFetch(`/api/v1/credentials?workspace_id=${ws}&kind=provider_login`),
        apiFetch(`/api/v1/credentials/bindings?workspace_id=${ws}&scope=AGENT&agent_id=${encodeURIComponent(agentId)}`),
      ])
      if (!listRes.ok) throw new Error(`HTTP ${listRes.status}`)
      if (!bindRes.ok) throw new Error(`HTTP ${bindRes.status}`)
      const list = await listRes.json()
      const body = (await bindRes.json()) as { bindings?: BindingRow[] }
      setSeats(Array.isArray(list) ? (list as LoginCredential[]).filter((c) => c?.login) : [])
      setBindings(Array.isArray(body?.bindings) ? body.bindings : [])
    } catch (err) {
      setLoadError(err instanceof Error ? err.message : "network error")
    } finally {
      setLoaded(true)
    }
  }, [ws, agentId])

  const openPicker = (next: boolean) => {
    setOpen(next)
    if (next) void load()
  }

  // What the agent pays with now: the server's word when it has one, else the
  // AGENT binding that points at a seat (an older server, or right after a
  // change here and before the agent is re-read).
  const seatById = React.useMemo(() => new Map(seats.map((s) => [s.id, s])), [seats])
  const boundSeat = snapshotStale && loadError ? null : bindings.find((b) => seatById.has(b.credential_id)) ?? null
  const currentId = loaded ? boundSeat?.credential_id ?? snapshot?.credential_id ?? null : snapshot?.credential_id ?? null
  const current = currentId ? seatById.get(currentId) ?? null : null
  const currentLogin: ProviderLogin | null = current?.login ?? snapshot?.login ?? null
  const currentName = current?.name ?? snapshot?.name ?? null

  // The seat as a row, for the status ladder — from the list when loaded,
  // else from what the agent carries.
  const currentRow: LoginCredential | null =
    current ?? (snapshot?.login && snapshot.credential_id && snapshot.name ? { id: snapshot.credential_id, name: snapshot.name, provider: snapshot.login.provider, status: "ACTIVE", login: snapshot.login } : null)
  const partners = currentRow
    ? seats.filter((s) => s.id !== currentRow.id && (s.login?.provider ?? s.provider) === (currentRow.login?.provider ?? currentRow.provider))
    : []
  const atLimit = currentRow ? deriveLoginStatus(currentRow) === "at_limit" : false
  const until = currentRow?.login?.quota?.resets_at ? ` until ${formatClock(currentRow.login.quota.resets_at)}` : ""
  const warning = !atLimit
    ? null
    : loaded && partners.length === 0
      ? `This seat is at its limit${until} and has no pool partner, so the next run of ${agentName} waits. Add a second ${getBrand(currentRow!.login!.provider).label} seat to fail over.`
      : loaded
        ? null
        : `This seat is at its limit${until}. If no other ${getBrand(currentRow!.login!.provider).label} seat is in scope, the next run of ${agentName} waits.`
  const mismatch =
    currentLogin && provider && currentLogin.provider.toUpperCase() !== provider
      ? `${adapterLabel(cliAdapter)} cannot pay with a ${getBrand(currentLogin.provider).label} seat. Pick a ${getBrand(provider).label} seat, or change the adapter.`
      : null

  async function choose(seat: LoginCredential) {
    if (seat.id === currentId) {
      setOpen(false)
      return
    }
    setSaving(true)
    let released: BindingRow | null = null
    try {
      // No PATCH on a binding — the row IS the slot claim. Swapping seats is
      // delete-then-create; the delete goes first so the create cannot 409 on
      // the slot the old seat still holds.
      const slot = loginBindingSlot(seat.login, boundSeat?.slot ?? null) ?? "PROVIDER_LOGIN"
      if (boundSeat) {
        setSnapshotStale(true)
        const del = await apiFetch(`/api/v1/credentials/bindings/${encodeURIComponent(boundSeat.id)}?workspace_id=${ws}`, { method: "DELETE" })
        if (!del.ok && del.status !== 404) throw new Error(`Couldn't release the current seat (HTTP ${del.status}).`)
        if (del.ok) released = boundSeat
      }
      setSnapshotStale(true)
      const res = await apiFetch(`/api/v1/credentials/bindings?workspace_id=${ws}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ credential_id: seat.id, scope: "AGENT", crew_id: "", agent_id: agentId, slot }),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        throw new Error(typeof data.error === "string" ? data.error : `Couldn't assign the seat (HTTP ${res.status}).`)
      }
      toast.success(`Pays with saved`)
      setOpen(false)
      void load()
      onChanged?.()
    } catch (err) {
      const message = err instanceof Error ? err.message : "Could not save"
      let restored = true
      if (released) {
        const back = await apiFetch(`/api/v1/credentials/bindings?workspace_id=${ws}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ credential_id: released.credential_id, scope: "AGENT", crew_id: "", agent_id: agentId, slot: released.slot }),
        }).catch(() => null)
        restored = Boolean(back?.ok)
      }
      toast.error(restored ? message : `${message} The previous seat was released and could not be restored. Check the agent's assignment before running it.`)
      await load()
      onChanged?.()
    } finally {
      setSaving(false)
    }
  }

  if (!canBind) {
    const visibleProvider = paysWith?.provider ?? paysWith?.login?.provider
    return (
      <ConfigRow label="Pays with" hint="Provider accounts are managed by workspace administrators.">
        <span className="flex items-center gap-2 type-row">
          {visibleProvider ? <><LoginBrandMark provider={visibleProvider} size="sm" />{getBrand(visibleProvider).label}</> : "No provider reported"}
        </span>
      </ConfigRow>
    )
  }

  return (
    <>
      <ConfigRow
        label="Pays with"
        hint={
          provider
            ? `The ${getBrand(provider).label} seat this agent runs on. Its own binding wins over the crew's and the workspace's.`
            : "The seat this agent runs on. OpenCode takes any provider's login."
        }
      >
        <Popover open={open} onOpenChange={openPicker}>
          <PopoverTrigger asChild>
            <button
              type="button"
              disabled={!canBind || saving}
              aria-label="Pays with"
              aria-expanded={open}
              className={cn(
                "type-row flex h-7 w-full items-center gap-2 rounded-lg border border-border bg-background px-2 text-left text-foreground outline-none",
                "transition-[border-color,box-shadow] hover:border-foreground/25 focus:border-primary disabled:cursor-default disabled:opacity-70",
              )}
            >
              {currentLogin ? (
                <>
                  <LoginBrandMark provider={currentLogin.provider} size="sm" />
                  <span className="min-w-0 flex-1 truncate">{currentName}</span>
                </>
              ) : (
                <>
                  <CreditCard className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" aria-hidden="true" />
                  <span className="min-w-0 flex-1 truncate text-muted-foreground">
                    {snapshotStale ? "Assignment changed — inherited account not verified" : "Inherits from the crew or workspace"}
                  </span>
                </>
              )}
              <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
            </button>
          </PopoverTrigger>
          <PopoverContent className="w-[360px] p-0" align="end">
            <div className="border-b border-hairline px-3 py-2">
              <span className="type-meta uppercase tracking-wide text-muted-foreground-soft">
                {provider ? `${getBrand(provider).label} seats in scope` : "Seats in scope"}
              </span>
            </div>
            <ul role="listbox" aria-label="Provider logins" className="max-h-72 overflow-y-auto py-1">
              {!loaded && (
                <li className="px-3 py-2 type-meta text-muted-foreground">Loading seats…</li>
              )}
              {loadError && (
                <li className="px-3 py-2 type-meta text-destructive">Couldn&apos;t load the seats ({loadError}).</li>
              )}
              {loaded && !loadError && seats.length === 0 && (
                <li className="px-3 py-2 type-meta text-muted-foreground">No provider login in this workspace yet.</li>
              )}
              {seats.map((seat) => {
                const login = seat.login!
                const wrong = provider !== null && login.provider.toUpperCase() !== provider
                const selected = seat.id === currentId
                return (
                  <li key={seat.id}>
                    <button
                      type="button"
                      role="option"
                      aria-selected={selected}
                      aria-disabled={wrong || undefined}
                      disabled={wrong || saving}
                      title={wrong ? "wrong provider" : undefined}
                      onClick={() => void choose(seat)}
                      className={cn(
                        "flex w-full items-center gap-2.5 px-3 py-2 text-left transition-colors",
                        wrong ? "cursor-not-allowed opacity-50" : "hover:bg-white/[0.04]",
                      )}
                    >
                      <LoginBrandMark provider={login.provider} size="sm" />
                      <span className="min-w-0 flex-1">
                        <span className="block truncate type-row text-foreground">{seat.name}</span>
                        <span className="block truncate type-meta text-muted-foreground">
                          {wrong
                            ? `${getBrand(login.provider).label} — ${adapterLabel(cliAdapter)} cannot pay with this`
                            : [
                                modeLabel(login.mode),
                                planLabel(login),
                                login.expires_at ? `expires ${formatExpiresIn(login.expires_at)}${login.refresh.supported ? " (auto)" : ""}` : null,
                              ]
                                .filter(Boolean)
                                .join(" · ")}
                        </span>
                      </span>
                      {wrong ? (
                        <span className="shrink-0 type-meta text-muted-foreground-soft">wrong provider</span>
                      ) : (
                        <LoginStatusPill credential={seat} />
                      )}
                      {selected && <Check className="h-3.5 w-3.5 shrink-0 text-primary" aria-hidden="true" />}
                    </button>
                  </li>
                )
              })}
            </ul>
            <div className="border-t border-hairline px-3 py-2">
              <Link href="/credentials?tab=providers" className="inline-flex items-center gap-1 type-meta text-primary hover:underline">
                <Plus className="h-3 w-3" />
                {provider ? `Add a second ${getBrand(provider).label} seat to make a pool` : "Add a provider login"}
              </Link>
            </div>
          </PopoverContent>
        </Popover>
      </ConfigRow>
      {(warning || mismatch) && (
        <div
          role="status"
          className={cn(
            "flex items-start gap-2 border-b border-border px-3 py-2 type-meta leading-relaxed last:border-b-0",
            mismatch ? "text-destructive" : "text-warn",
          )}
        >
          <AlertTriangle className="mt-px h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          <span>{mismatch ?? warning}</span>
        </div>
      )}
    </>
  )
}
