"use client"

// Issue #1001 M0 — Keeper watchdog governance control panel.
//
// Rendered at the top of the admin Keeper tab. Surfaces the per-workspace
// watchdog governance settings backed by internal/api/keeper_governance.go:
//
//   GET /api/v1/admin/keeper/governance  → { configured, enabled,
//        security_contact_user_id, deny_notify_min_risk }        (ADMIN+)
//   PUT /api/v1/admin/keeper/governance  ← { enabled,
//        security_contact_user_id, deny_notify_min_risk }        (OWNER/ADMIN)
//
// "No row" semantics: configured=false means the workspace inherits the
// server-level config (KEEPER_ENABLED) — we surface that inherited state
// inline and pre-set the switch to the effective value. The first save
// creates the explicit row, which wins from then on.
//
// The security contact must be an OWNER/ADMIN workspace member (the
// backend rejects anything else with a 400), so the picker is filtered
// to those roles from GET /workspaces/{id}/members. Empty contact =
// legacy fanout to everyone with the MANAGER role.

import React, { useCallback, useEffect, useMemo, useState } from "react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { SettingsCard, SettingsRow } from "@/components/features/settings/shared"
import { useAbilities } from "@/hooks/use-abilities"
import { apiFetch } from "@/lib/api-fetch"

interface GovernanceResponse {
  configured: boolean
  enabled: boolean
  security_contact_user_id: string
  deny_notify_min_risk: number
}

interface WorkspaceMember {
  id: string
  user_id: string
  role: string
  user?: {
    id: string
    email: string
    full_name: string | null
    avatar_url: string | null
  } | null
}

interface FormState {
  enabled: boolean
  contact: string // "" = everyone with MANAGER role
  risk: string    // kept as string so the number input can be edited freely
}

// Radix Select forbids value="" on items, so the "everyone" option uses a
// sentinel that maps to the backend's empty security_contact_user_id.
const MANAGER_FANOUT = "__managers__"

export interface KeeperGovernancePanelProps {
  workspaceId: string | null | undefined
  /** Server-level keeper enabled flag (GET /system/keeper) — the value an
   *  unconfigured workspace inherits. */
  serverEnabled: boolean
}

export const KeeperGovernancePanel = React.memo(function KeeperGovernancePanel({
  workspaceId,
  serverEnabled,
}: KeeperGovernancePanelProps) {
  // Mirrors AgentLearningToggle: derive edit rights from CASL. The PUT is
  // roleManage (OWNER/ADMIN) server-side; only those roles get "manage" on
  // Workspace, so this lines up exactly. Server stays authoritative — the
  // greyed-out UI is a UX hint, not a security boundary.
  const { abilities } = useAbilities()
  const canEdit = useMemo(() => abilities.can("manage", "Workspace"), [abilities])

  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [configured, setConfigured] = useState(false)
  const [admins, setAdmins] = useState<WorkspaceMember[]>([])
  const [form, setForm] = useState<FormState>({ enabled: false, contact: "", risk: "7" })
  const [baseline, setBaseline] = useState<FormState>({ enabled: false, contact: "", risk: "7" })

  const load = useCallback(async (signal?: AbortSignal) => {
    if (!workspaceId) {
      setLoading(false)
      return
    }
    setLoading(true)
    setErr(null)
    try {
      const [govRes, membersRes] = await Promise.all([
        apiFetch(
          `/api/v1/admin/keeper/governance?workspace_id=${encodeURIComponent(workspaceId)}`,
          { signal },
        ),
        apiFetch(
          `/api/v1/workspaces/${workspaceId}/members?workspace_id=${encodeURIComponent(workspaceId)}`,
          { signal },
        ),
      ])
      if (signal?.aborted) return
      if (!govRes.ok) {
        setErr(`Failed to load governance settings (HTTP ${govRes.status})`)
        return
      }
      const gov = (await govRes.json()) as GovernanceResponse
      if (signal?.aborted) return

      // A members failure only degrades the picker; governance still renders.
      if (membersRes.ok) {
        const members = (await membersRes.json()) as WorkspaceMember[]
        if (signal?.aborted) return
        setAdmins(
          (Array.isArray(members) ? members : []).filter(
            (m) => m.role === "OWNER" || m.role === "ADMIN",
          ),
        )
      } else {
        setAdmins([])
      }

      setConfigured(gov.configured)
      const next: FormState = {
        // Unconfigured workspace → switch reflects the effective (inherited)
        // server state so flipping it saves what the operator actually sees.
        enabled: gov.configured ? gov.enabled : serverEnabled,
        contact: gov.security_contact_user_id ?? "",
        risk: String(gov.deny_notify_min_risk ?? 7),
      }
      setForm(next)
      setBaseline(next)
    } catch (e) {
      // Aborts are expected when workspaceId changes mid-flight.
      if (e instanceof DOMException && e.name === "AbortError") return
      setErr(e instanceof Error ? e.message : "Failed to load governance settings")
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  }, [workspaceId, serverEnabled])

  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal)
    return () => controller.abort()
  }, [load])

  const dirty =
    form.enabled !== baseline.enabled ||
    form.contact !== baseline.contact ||
    form.risk !== baseline.risk

  const save = useCallback(async () => {
    if (!workspaceId) return
    const riskNum = Number(form.risk)
    if (!Number.isInteger(riskNum) || riskNum < 1 || riskNum > 10) {
      toast.error("Risk threshold must be a whole number between 1 and 10")
      return
    }
    setSaving(true)
    try {
      const res = await apiFetch(
        `/api/v1/admin/keeper/governance?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            enabled: form.enabled,
            security_contact_user_id: form.contact,
            deny_notify_min_risk: riskNum,
          }),
        },
      )
      if (!res.ok) {
        let msg = `HTTP ${res.status}`
        try {
          const e = (await res.json()) as { error?: string; detail?: string }
          msg = e.error ?? e.detail ?? msg
        } catch {
          /* keep the status fallback */
        }
        toast.error(`Failed to save governance: ${msg}`)
        return
      }
      const body = (await res.json()) as GovernanceResponse
      setConfigured(body.configured)
      const next: FormState = {
        enabled: body.enabled,
        contact: body.security_contact_user_id ?? "",
        risk: String(body.deny_notify_min_risk ?? riskNum),
      }
      setForm(next)
      setBaseline(next)
      toast.success(body.enabled ? "Watchdog enabled" : "Watchdog governance saved")
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save governance")
    } finally {
      setSaving(false)
    }
  }, [workspaceId, form])

  if (!workspaceId) return null

  if (loading) {
    return <Skeleton className="h-[180px] rounded-xl" data-testid="keeper-governance-loading" />
  }

  if (err) {
    return (
      <SettingsCard
        title="Watchdog governance"
        description="Workspace-level watchdog controls"
      >
        <div className="px-4 py-3 flex items-center justify-between gap-3">
          <span className="text-[11px] text-destructive/90">{err}</span>
          <Button
            variant="outline"
            size="sm"
            className="h-7 px-2.5 text-xs"
            onClick={() => { void load() }}
          >
            Retry
          </Button>
        </div>
      </SettingsCard>
    )
  }

  // Keep the current contact selectable even if that member was demoted or
  // removed since it was saved — otherwise the Select renders blank and a
  // save would silently rewrite the contact.
  const contactInList =
    form.contact === "" || admins.some((m) => m.user_id === form.contact)

  return (
    <SettingsCard
      title="Watchdog governance"
      description="Who the behavioral watchdog reports to, and when. Credential-access enforcement stays server-configured."
      actions={
        canEdit ? (
          <Button
            variant="soft"
            size="sm"
            className="h-7 px-2.5 text-xs"
            onClick={() => { void save() }}
            disabled={saving || !dirty}
            data-testid="keeper-governance-save"
          >
            {saving ? "Saving…" : "Save"}
          </Button>
        ) : undefined
      }
    >
      <SettingsRow
        label="Watchdog enabled"
        description={
          configured
            ? "Explicit workspace setting — overrides the server default."
            : `Not configured for this workspace — inherited from server: ${serverEnabled ? "on" : "off"}. Saving pins an explicit setting.`
        }
      >
        <Switch
          checked={form.enabled}
          onCheckedChange={(checked) => setForm((f) => ({ ...f, enabled: checked }))}
          disabled={!canEdit || saving}
          data-testid="keeper-governance-switch"
          aria-label="Toggle watchdog enabled"
        />
      </SettingsRow>

      <SettingsRow
        label="Security contact"
        description="Findings target this person's inbox in realtime."
      >
        <Select
          value={form.contact === "" ? MANAGER_FANOUT : form.contact}
          onValueChange={(v) =>
            setForm((f) => ({ ...f, contact: v === MANAGER_FANOUT ? "" : v }))
          }
          disabled={!canEdit || saving}
        >
          <SelectTrigger
            className="h-8 text-xs w-[220px]"
            aria-label="Security contact"
            data-testid="keeper-governance-contact"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={MANAGER_FANOUT} className="text-xs">
              Everyone with MANAGER role
            </SelectItem>
            {admins.map((m) => (
              <SelectItem key={m.user_id} value={m.user_id} className="text-xs">
                {m.user?.full_name || m.user?.email || m.user_id}
              </SelectItem>
            ))}
            {!contactInList && (
              <SelectItem value={form.contact} className="text-xs">
                {form.contact} (no longer OWNER/ADMIN)
              </SelectItem>
            )}
          </SelectContent>
        </Select>
      </SettingsRow>

      <SettingsRow
        label="Notify on DENY at risk ≥"
        description="ESCALATE decisions always notify; this additionally surfaces high-risk DENYs."
        border={false}
      >
        <Input
          type="number"
          min={1}
          max={10}
          step={1}
          inputMode="numeric"
          value={form.risk}
          onChange={(e) => setForm((f) => ({ ...f, risk: e.target.value }))}
          disabled={!canEdit || saving}
          className="h-8 w-16 text-xs text-right tabular-nums"
          aria-label="DENY notification risk threshold (1-10)"
          data-testid="keeper-governance-risk"
        />
      </SettingsRow>
    </SettingsCard>
  )
})
