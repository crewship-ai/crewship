"use client"

// Issue #1378 — surface the workspace `allow_privileged_credentials` toggle in
// the UI. It was CLI-only (cmd_workspace.go), a fail-closed isolation-boundary
// override with no UI, so a workspace owner couldn't even see its state.
//
//   GET   /api/v1/workspaces/{id}  → { allow_privileged_credentials }  (member+)
//   PATCH /api/v1/workspaces/{id}  ← { allow_privileged_credentials }  (OWNER/ADMIN)
//
// Default OFF (fail-closed, #1032): a privileged crew's sidecar CredStore is
// reachable from any process in the container (the UID 1001/1002 boundary is
// gone under --privileged), so credentials are NOT loaded into privileged
// crews unless the workspace explicitly opts in here.

import React, { useCallback, useEffect, useMemo, useState } from "react"
import { toast } from "sonner"
import { ShieldAlert } from "lucide-react"
import { Switch } from "@/components/ui/switch"
import { Skeleton } from "@/components/ui/skeleton"
import { Button } from "@/components/ui/button"
import { SettingsCard, SettingsRow } from "@/components/features/settings/shared"
import { useAbilities } from "@/hooks/use-abilities"
import { apiFetch } from "@/lib/api-fetch"

interface WorkspaceResponse {
  allow_privileged_credentials?: boolean
}

export interface PrivilegedCredentialsCardProps {
  workspaceId: string
}

export const PrivilegedCredentialsCard = React.memo(function PrivilegedCredentialsCard({
  workspaceId,
}: PrivilegedCredentialsCardProps) {
  // PATCH /workspaces/{id} is roleManage (OWNER/ADMIN) server-side; only those
  // roles get "manage" on Workspace, so the greyed-out switch lines up exactly.
  // The server stays authoritative — this is a UX hint, not a security gate.
  const { abilities } = useAbilities()
  const canEdit = useMemo(() => abilities.can("manage", "Workspace"), [abilities])

  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [allow, setAllow] = useState(false)

  const load = useCallback(async (signal?: AbortSignal) => {
    setLoading(true)
    setErr(null)
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}?workspace_id=${encodeURIComponent(workspaceId)}`,
        { signal },
      )
      if (signal?.aborted) return
      if (!res.ok) {
        setErr(`Failed to load workspace security settings (HTTP ${res.status})`)
        return
      }
      const ws = (await res.json()) as WorkspaceResponse
      if (signal?.aborted) return
      setAllow(ws.allow_privileged_credentials ?? false)
    } catch (e) {
      if (e instanceof DOMException && e.name === "AbortError") return
      setErr(e instanceof Error ? e.message : "Failed to load workspace security settings")
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal)
    return () => controller.abort()
  }, [load])

  const setAllowPrivileged = useCallback(
    async (next: boolean) => {
      // Optimistic flip so the Switch feels responsive; roll back on failure.
      const prev = allow
      setAllow(next)
      setSaving(true)
      try {
        const res = await apiFetch(
          `/api/v1/workspaces/${workspaceId}?workspace_id=${encodeURIComponent(workspaceId)}`,
          {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ allow_privileged_credentials: next }),
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
          setAllow(prev)
          toast.error(`Failed to update privileged credentials: ${msg}`)
          return
        }
        const body = (await res.json()) as WorkspaceResponse
        setAllow(body.allow_privileged_credentials ?? next)
        toast.success(
          next
            ? "Privileged crews may now load credentials"
            : "Privileged crews will no longer load credentials",
        )
      } catch (e) {
        setAllow(prev)
        toast.error(e instanceof Error ? e.message : "Failed to update privileged credentials")
      } finally {
        setSaving(false)
      }
    },
    [allow, workspaceId],
  )

  if (loading) {
    return <Skeleton className="h-[96px] rounded-xl" data-testid="privileged-credentials-loading" />
  }

  if (err) {
    return (
      <SettingsCard title="Privileged credentials" description="Workspace isolation-boundary override">
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

  return (
    <SettingsCard
      title="Privileged credentials"
      description="An isolation-boundary override. Off by default (fail-closed)."
    >
      <SettingsRow
        label={
          <span className="inline-flex items-center gap-2">
            <ShieldAlert className="h-3.5 w-3.5 text-amber-500" />
            Load credentials into privileged crews
          </span>
        }
        description="Privileged crews run without the UID 1001/1002 sidecar boundary, so any process in the container can reach the CredStore. Leave OFF unless you understand the exposure."
        border={false}
      >
        <Switch
          checked={allow}
          onCheckedChange={(checked) => { void setAllowPrivileged(checked) }}
          disabled={!canEdit || saving}
          data-testid="privileged-credentials-switch"
          aria-label="Toggle loading credentials into privileged crews"
        />
      </SettingsRow>
    </SettingsCard>
  )
})
