"use client"

/**
 * Assign a provider login to one agent.
 *
 * "Which agent pays with this seat" is an AGENT-scope row in
 * credential_bindings (PRD provider-logins §5.4: four subscriptions → four
 * agent bindings, nothing new). The console had no way to write one — the
 * wizard only ever claims WORKSPACE and CREW slots — so this is the missing
 * door, and it writes exactly what `crewship credential binding create
 * --scope AGENT` writes: `{credential_id, scope, agent_id, slot}`.
 *
 * The slot is suggested from the seat's delivery (the variable the CLI reads,
 * or the seat's name slot for file delivery) and editable, because two seats
 * of one provider on the same scope must share a slot to form a pool — and
 * that is a decision the operator makes by naming, not one the dialog can
 * make for them.
 */

import * as React from "react"
import { Check, ChevronsUpDown } from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import {
  Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList,
} from "@/components/ui/command"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { apiFetch } from "@/lib/api-fetch"
import { isValidEnvVarName } from "@/lib/env-var-name"
import { adapterLabel, adapterProvider, loginBindingSlot, type LoginCredential } from "@/lib/credentials/provider-logins"
import { getBrand } from "@/lib/credential-providers/registry"
import { cn } from "@/lib/utils"

interface AgentRow {
  id: string
  name: string
  slug?: string
  cli_adapter?: string | null
  crew_id?: string | null
  crew?: { name?: string } | null
}

export interface AssignLoginDialogProps {
  workspaceId: string
  credential: LoginCredential
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Called after the binding lands, with the agent it went to. */
  onAssigned?: (agent: { id: string; name: string }) => void
}

export function AssignLoginDialog({ workspaceId, credential, open, onOpenChange, onAssigned }: AssignLoginDialogProps) {
  const [agents, setAgents] = React.useState<AgentRow[]>([])
  const [loading, setLoading] = React.useState(false)
  const [agentId, setAgentId] = React.useState<string | null>(null)
  const [pickerOpen, setPickerOpen] = React.useState(false)
  const [slot, setSlot] = React.useState("")
  const [submitting, setSubmitting] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)

  const login = credential.login ?? null
  const provider = (login?.provider ?? credential.provider).toUpperCase()
  const brand = getBrand(provider)

  React.useEffect(() => {
    if (!open) return
    setAgentId(null)
    setError(null)
    setSlot(loginBindingSlot(login) ?? "")
    let cancelled = false
    setLoading(true)
    apiFetch(`/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((data: AgentRow[]) => {
        if (!cancelled) setAgents(Array.isArray(data) ? data.filter((a) => typeof a?.id === "string") : [])
      })
      .catch(() => !cancelled && setAgents([]))
      .finally(() => !cancelled && setLoading(false))
    return () => { cancelled = true }
    // `login` is derived from `credential`, which is the identity that matters.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, workspaceId, credential.id])

  const chosen = agents.find((a) => a.id === agentId) ?? null
  // An agent whose CLI cannot pay with this provider is still offered — the
  // operator may be about to switch the adapter — but the mismatch is said
  // out loud before the binding is written, not discovered as a 401 in a run.
  const mismatch =
    chosen && adapterProvider(chosen.cli_adapter) && adapterProvider(chosen.cli_adapter) !== provider
      ? `${adapterLabel(chosen.cli_adapter)} cannot pay with ${brand.label} — switch the agent's adapter first, or pick another agent.`
      : null
  const slotOk = isValidEnvVarName(slot.trim())

  async function submit() {
    if (!chosen) return
    setSubmitting(true)
    setError(null)
    try {
      const res = await apiFetch(`/api/v1/credentials/bindings?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          credential_id: credential.id,
          scope: "AGENT",
          crew_id: "",
          agent_id: chosen.id,
          slot: slot.trim(),
        }),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        setError(
          res.status === 409
            ? `${chosen.name} already pays with another seat under ${slot.trim()} — remove that binding first, or use a different slot to make a pool.`
            : typeof data.error === "string" ? data.error : `Couldn't assign the seat (HTTP ${res.status}).`,
        )
        return
      }
      onAssigned?.({ id: chosen.id, name: chosen.name })
      onOpenChange(false)
    } catch {
      setError("Network error while assigning the seat.")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Assign to agent</DialogTitle>
          <DialogDescription>
            <span className="font-medium text-foreground">{credential.name}</span> becomes the seat this agent pays
            with. The agent&apos;s own binding wins over its crew&apos;s and the workspace&apos;s.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          <div className="space-y-1.5">
            <Label className="type-section text-muted-foreground">Agent</Label>
            <Popover open={pickerOpen} onOpenChange={setPickerOpen}>
              <PopoverTrigger asChild>
                <Button
                  variant="outline"
                  role="combobox"
                  aria-expanded={pickerOpen}
                  aria-label="Pick an agent"
                  className="h-9 w-full justify-between font-normal text-sm"
                  disabled={loading}
                >
                  {chosen ? (
                    <span className="inline-flex min-w-0 items-center gap-2">
                      <AgentAvatar seed={chosen.id} className="h-4 w-4 shrink-0" alt="" />
                      <span className="truncate">{chosen.name}</span>
                    </span>
                  ) : loading ? "Loading agents…" : "Pick an agent…"}
                  <ChevronsUpDown className="ml-2 h-3.5 w-3.5 shrink-0 opacity-50" />
                </Button>
              </PopoverTrigger>
              <PopoverContent className="w-[--radix-popover-trigger-width] p-0" align="start">
                <Command>
                  <CommandInput placeholder="Search agents…" />
                  <CommandList>
                    <CommandEmpty>No agents found.</CommandEmpty>
                    <CommandGroup>
                      {agents.map((a) => {
                        const on = a.id === agentId
                        const wrong = adapterProvider(a.cli_adapter) !== null && adapterProvider(a.cli_adapter) !== provider
                        return (
                          <CommandItem
                            key={a.id}
                            value={`${a.name} ${a.crew?.name ?? ""}`}
                            onSelect={() => { setAgentId(a.id); setPickerOpen(false) }}
                          >
                            <Check className={cn("mr-2 h-4 w-4", on ? "opacity-100" : "opacity-0")} />
                            <AgentAvatar seed={a.id} className="mr-2 h-4 w-4 shrink-0" alt="" />
                            <span className="min-w-0 flex-1 truncate">{a.name}</span>
                            <span className={cn("ml-2 shrink-0 type-meta", wrong ? "text-warn" : "text-muted-foreground-soft")}>
                              {adapterLabel(a.cli_adapter)}
                            </span>
                          </CommandItem>
                        )
                      })}
                    </CommandGroup>
                  </CommandList>
                </Command>
              </PopoverContent>
            </Popover>
            {mismatch && <p className="type-meta text-warn">{mismatch}</p>}
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="assign-slot" className="type-section text-muted-foreground">Slot</Label>
            <Input
              id="assign-slot"
              value={slot}
              onChange={(e) => setSlot(e.target.value)}
              className="h-9 font-mono"
              placeholder="CLAUDE_CODE_OAUTH_TOKEN"
            />
            <p className="type-meta text-muted-foreground-soft">
              {login?.delivery?.kind === "file"
                ? `Delivered as a file (${login.delivery.target}); the slot is only the seat's name in the pool.`
                : "The variable the CLI reads. A second seat of the same provider under the same slot makes a pool."}
            </p>
          </div>

          {error && <p className="type-meta text-destructive">{error}</p>}
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={submitting}>Cancel</Button>
          <Button onClick={submit} disabled={!chosen || !slotOk || submitting}>
            {submitting ? "Assigning…" : "Assign"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
