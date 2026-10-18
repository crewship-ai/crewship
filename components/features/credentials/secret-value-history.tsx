"use client"

/**
 * A secret's Value history tab (Harbor 2): replace the value, see every
 * replacement, and end a running grace period early — after which the old
 * value is gone from Crewship for good.
 *
 * "Rotate at the service" and a replace reminder are in the design but have
 * no backend yet; they are not drawn.
 */

import * as React from "react"
import { History, RefreshCw } from "lucide-react"

import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { StatusPill } from "@/components/ui/status-pill"
import { DetailCard } from "@/components/ui/detail"
import { apiFetch } from "@/lib/api-fetch"
import { formatDateTime } from "@/lib/time"
import { toast } from "@/lib/toast"

export interface RotationRow {
  id: string
  grace_seconds: number
  rotated_at: string
  expires_at: string
  rotated_by: string
  status: string
  old_value_gone?: boolean
}

export function graceLabel(seconds: number): string {
  if (seconds <= 0) return "no grace"
  if (seconds % 86400 === 0) return `grace ${seconds / 86400} d`
  if (seconds % 3600 === 0) return `grace ${seconds / 3600} h`
  return `grace ${Math.round(seconds / 60)} min`
}

export function SecretValueHistory({
  workspaceId,
  credentialId,
  canRotate,
  onReplace,
  revision = 0,
}: {
  workspaceId: string
  credentialId: string
  canRotate: boolean
  onReplace: () => void
  /** Bump to reload after a replacement. */
  revision?: number
}) {
  const [rows, setRows] = React.useState<RotationRow[] | null>(null)
  const [error, setError] = React.useState(false)
  const [ending, setEnding] = React.useState<RotationRow | null>(null)
  const [reload, setReload] = React.useState(0)

  React.useEffect(() => {
    if (!canRotate) return
    let cancelled = false
    setError(false)
    apiFetch(`/api/v1/credentials/${encodeURIComponent(credentialId)}/rotations?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then(async (r) => {
        if (!r.ok) throw new Error()
        const body = await r.json()
        if (!Array.isArray(body)) throw new Error()
        if (!cancelled) setRows(body as RotationRow[])
      })
      .catch(() => { if (!cancelled) setError(true) })
    return () => { cancelled = true }
  }, [workspaceId, credentialId, canRotate, revision, reload])

  const active = rows?.find((r) => r.status === "ACTIVE") ?? null

  const endGrace = async () => {
    if (!ending) return
    const res = await apiFetch(`/api/v1/credential-rotations/${encodeURIComponent(ending.id)}?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "DELETE" }).catch(() => null)
    if (!res || !res.ok) {
      toast.error("Couldn't end the grace period.", { description: "The old value still works until the grace period ends." })
      throw new Error("cancel failed")
    }
    toast.success("Grace period ended · the old value no longer works")
    setReload((n) => n + 1)
  }

  return (
    <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
      <DetailCard title="Replace the value" icon={RefreshCw}>
        <p className="text-body text-foreground/85">
          Create the new key at the service, then paste it here. The old value keeps working for the grace period, so running work does not break.
        </p>
        {canRotate ? (
          <div className="mt-3 flex flex-wrap items-center gap-2">
            <Button size="sm" onClick={onReplace}>
              <RefreshCw className="size-3.5" aria-hidden /> Replace value…
            </Button>
            {active && (
              <Button size="sm" variant="outline" onClick={() => setEnding(active)}>
                End grace early
              </Button>
            )}
          </div>
        ) : (
          <p className="mt-3 text-label text-muted-foreground">Replacing the value needs the rotate permission.</p>
        )}
        {active && (
          <p className="mt-2 text-label text-muted-foreground">
            The old value works until {formatDateTime(active.expires_at)}.
          </p>
        )}
      </DetailCard>

      <DetailCard title="History" icon={History} subtitle={rows && rows.length > 0 ? String(rows.length) : undefined}>
        {!canRotate ? (
          <p className="text-label text-muted-foreground">Value history is shown to people who can replace the value.</p>
        ) : error ? (
          <p role="alert" className="text-label text-warn">History could not be loaded.</p>
        ) : rows === null ? (
          <p role="status" className="text-label text-muted-foreground">Loading…</p>
        ) : rows.length === 0 ? (
          <p className="text-label text-muted-foreground">The value has not been replaced since it was added.</p>
        ) : (
          <ul className="flex flex-col gap-2">
            {rows.map((r) => (
              <li key={r.id} className="flex flex-wrap items-center gap-2 text-label">
                <span className="text-foreground">Replaced · {graceLabel(r.grace_seconds)}</span>
                {r.status === "ACTIVE" && <StatusPill className="font-sans tracking-normal" tone="warn" label="Old value still works" />}
                {r.status === "CANCELLED" && <StatusPill className="font-sans tracking-normal" tone="muted" label="Grace ended early" />}
                <span className="ml-auto tabular-nums text-muted-foreground">{formatDateTime(r.rotated_at)}</span>
              </li>
            ))}
          </ul>
        )}
      </DetailCard>

      <ConfirmDialog
        open={ending !== null}
        onOpenChange={(o) => { if (!o) setEnding(null) }}
        title="End the grace period now?"
        consequences={[
          { tone: "lost", text: "The old value is removed from Crewship and stops working at once." },
          { tone: "warn", text: "Runs still using the old value fail at their next request." },
          { tone: "kept", text: "The new value keeps working." },
        ]}
        confirmLabel="End grace early"
        destructive
        onConfirm={endGrace}
      />
    </div>
  )
}
