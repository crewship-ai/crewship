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
import { ShieldAlert, Info } from "lucide-react"
import { Switch } from "@/components/ui/switch"
import { Skeleton } from "@/components/ui/skeleton"
import { Button } from "@/components/ui/button"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { SettingsCard, SettingsRow } from "@/components/features/settings/shared"
import { useAbilities } from "@/hooks/use-abilities"
import { apiFetch } from "@/lib/api-fetch"

interface WorkspaceResponse {
  allow_privileged_credentials?: boolean
}

// The row description used to carry the full explanation below and got
// clipped: SettingsRow's label column is `min-w-0 shrink-0`, so a flex
// item with flex-shrink:0 never shrinks below its own max-content
// (unwrapped) width — it just overflows, and SettingsCard's
// `overflow-hidden` silently cropped the excess instead of wrapping it.
// Fix: keep the inline copy to one short sentence (still names the real
// stake, not softened) and move the rest into a tooltip, which lives in
// a portal outside this row's box model entirely.
const SHORT_DESCRIPTION =
  "Turning this on removes the fail-closed boundary between privileged crews and stored credentials."

const FULL_EXPLANATION =
  "Privileged crews run without the UID 1001/1002 sidecar boundary, so any process in the " +
  "container can reach the CredStore. This is OFF by default (fail-closed, #1032) so credentials " +
  "are never loaded into an unisolated container until a workspace owner opts in. Turn it on only " +
  "if this workspace runs privileged crews that genuinely need credentials at runtime, and you " +
  "accept that container-level isolation no longer applies to them."

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
          <span className="inline-flex items-center gap-1.5">
            <ShieldAlert className="h-3.5 w-3.5 text-warn shrink-0" />
            <span>Load credentials into privileged crews</span>
            <TooltipProvider delayDuration={0}>
              <Tooltip>
                <TooltipTrigger asChild>
                  {/* Info affordance carries the full explanation (UID
                      1001/1002 boundary, why OFF by default, when to opt
                      in) that no longer fits inline. `aria-label` gives it
                      an accessible name since the icon alone has none. */}
                  <button
                    type="button"
                    aria-label="More about the privileged-credentials boundary"
                    className="text-muted-foreground hover:text-foreground cursor-help shrink-0"
                  >
                    <Info className="h-3.5 w-3.5" />
                  </button>
                </TooltipTrigger>
                <TooltipContent side="top" className="max-w-xs text-[11px]">
                  {FULL_EXPLANATION}
                </TooltipContent>
              </Tooltip>
            </TooltipProvider>
          </span>
        }
        description={
          // Explicit cap so the sentence wraps well before it could ever
          // approach the row's available width, instead of relying on the
          // (unshrinkable) label column to save us again.
          <span className="block max-w-[22rem] whitespace-normal break-words">
            {SHORT_DESCRIPTION}
          </span>
        }
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
