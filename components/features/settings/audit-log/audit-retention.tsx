"use client"

import { useEffect, useState } from "react"
import { Clock } from "lucide-react"

import { toast } from "@/lib/toast"
import { apiFetch } from "@/lib/api-fetch"
import { refreshWorkspaceSettings, useWorkspace } from "@/hooks/use-workspace"
import { CreateSurface, CreateSurfaceBody, CreateSurfaceFooter, CreateSurfaceHeader, CreateSurfaceNotice } from "@/components/layout/create-surface"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { Segmented } from "@/components/ui/segmented"
import { SettingsCard, SettingsRow } from "../shared"

/**
 * Settings › Audit log › Change how long. The two windows are
 * `audit_log_retention_days` (All changes) and `credential_audit_retention_days`
 * (Secret use) on the workspace; 0 is an explicit "keep forever", and an unset
 * window is the server's default: forever for All changes, 90 days for Secret
 * use. The web already PATCHes /api/v1/workspaces/{id} (ADMIN and up, the same
 * tier as this page); the list returns both values.
 */

export const AUDIT_DEFAULT_DAYS = 0
export const SECRET_DEFAULT_DAYS = 90
const PRESETS = [0, 365, 90] as const

type WithRetention = { audit_log_retention_days?: number | null; credential_audit_retention_days?: number | null }

/** The windows in force, with the server's default for one that is unset. */
export function useAuditRetention(): { audit: number; secret: number } | null {
  const { workspace } = useWorkspace()
  if (!workspace) return null
  const w = workspace as WithRetention
  return { audit: w.audit_log_retention_days ?? AUDIT_DEFAULT_DAYS, secret: w.credential_audit_retention_days ?? SECRET_DEFAULT_DAYS }
}

export function retentionWords(days: number): string {
  if (days === 0) return "forever"
  if (days === 365) return "1 year"
  return `${days} days`
}

const label = (days: number) => (days === 0 ? "Forever" : days === 365 ? "1 y" : `${days} d`)
const optionsFor = (current: number) => [...(PRESETS as readonly number[]).includes(current) ? PRESETS : [...PRESETS, current]].map((d) => ({ value: String(d), label: label(d) }))
/** Shorter than before: older entries go at the next sweep. */
const shortens = (from: number, to: number) => to !== 0 && (from === 0 || to < from)

export function AuditRetentionDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const { workspaceId } = useWorkspace()
  const current = useAuditRetention()
  const [audit, setAudit] = useState(AUDIT_DEFAULT_DAYS)
  const [secret, setSecret] = useState(SECRET_DEFAULT_DAYS)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [confirming, setConfirming] = useState(false)

  useEffect(() => {
    if (!open || !current) return
    setAudit(current.audit)
    setSecret(current.secret)
    setError(null)
    // Opening is the moment the values are read; later refreshes must not move them under the person.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  const dirty = !!current && (audit !== current.audit || secret !== current.secret)
  const loses = !!current && (shortens(current.audit, audit) || shortens(current.secret, secret))

  async function save() {
    if (!current || !workspaceId) return
    setSaving(true)
    setError(null)
    try {
      const body: Record<string, number> = {}
      if (audit !== current.audit) body.audit_log_retention_days = audit
      if (secret !== current.secret) body.credential_audit_retention_days = secret
      const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        const err = await res.json().catch(() => null)
        setError(typeof err?.error === "string" ? err.error : `Couldn’t save (${res.status}).`)
        return
      }
      await refreshWorkspaceSettings()
      toast.success("Retention saved")
      onOpenChange(false)
    } catch {
      setError("Couldn’t reach the server. Nothing was changed.")
    } finally {
      setSaving(false)
    }
  }

  return (
    <>
      <CreateSurface open={open} onOpenChange={onOpenChange} size="sm" dirty={dirty} ariaLabel="How long the record is kept" onSubmit={() => (dirty ? (loses ? setConfirming(true) : void save()) : undefined)}>
        <CreateSurfaceHeader icon={Clock} title="How long the record is kept" onClose={() => onOpenChange(false)} />
        <CreateSurfaceBody className="flex flex-col gap-3">
          <SettingsCard title="Retention" hideHeader>
            <SettingsRow label="All changes" description="Default: forever">
              <Segmented ariaLabel="How long All changes are kept" value={String(audit)} onChange={(v) => setAudit(Number(v))} options={optionsFor(current?.audit ?? AUDIT_DEFAULT_DAYS)} />
            </SettingsRow>
            <SettingsRow label="Secret use" description="Default: 90 days">
              <Segmented ariaLabel="How long Secret use is kept" value={String(secret)} onChange={(v) => setSecret(Number(v))} options={optionsFor(current?.secret ?? SECRET_DEFAULT_DAYS)} />
            </SettingsRow>
          </SettingsCard>
          <p className="text-label text-muted-foreground">The command line does the same: <span className="font-mono">crewship workspace update --audit-log-retention-days</span>.</p>
          {error && <CreateSurfaceNotice tone="error">{error}</CreateSurfaceNotice>}
        </CreateSurfaceBody>
        <CreateSurfaceFooter
          onCancel={() => onOpenChange(false)}
          primaryLabel="Save"
          primaryDisabled={!dirty}
          busy={saving}
          onPrimary={() => (loses ? setConfirming(true) : void save())}
        />
      </CreateSurface>
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title="Keep the record for less time?"
        description="Entries older than the new window are deleted at the next daily sweep."
        consequences={[
          { tone: "lost", text: "Older entries cannot be brought back from here." },
          { tone: "kept", text: "Entries inside the window stay." },
        ]}
        confirmLabel="Shorten and save"
        destructive
        onConfirm={save}
      />
    </>
  )
}
