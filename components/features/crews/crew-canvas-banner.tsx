"use client"

import { useCallback, useEffect, useState } from "react"
import { AlertTriangle, ChevronDown } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { toast } from "sonner"

// ProvisioningBanner is the canvas-level fallback that surfaces
// container-image build state (needs_provision / running / failed) when
// the toolbar popover isn't open. Collapsible is a tiny details/summary
// wrapper used by the Settings tab. Extracted from crew-canvas.tsx.

function ProvisioningBanner({ crewId, crewSlug, workspaceId }: { crewId: string; crewSlug: string; workspaceId: string }) {
  const [state, setState] = useState<{ status: string; error?: string; cached?: string | null; hasConfig: boolean } | null>(null)
  const [triggering, setTriggering] = useState(false)

  const refresh = useCallback(async () => {
    try {
      // wsCtx middleware mandates workspace_id; without it the endpoint
      // 400s and the polling loop re-renders forever.
      const r = await fetch(`/api/v1/crews/${crewId}/provision?workspace_id=${encodeURIComponent(workspaceId)}`)
      if (!r.ok) return
      const data = await r.json()
      setState({
        status: data.status ?? "idle",
        error: data.error,
        cached: data.cached_image,
        hasConfig: Boolean(data.devcontainer_config),
      })
    } catch { /* tolerate */ }
  }, [crewId, workspaceId])

  useEffect(() => { void refresh() }, [refresh])

  // Poll fast while a build is in flight, slowly when idle/healthy.
  useEffect(() => {
    const isBusy = state?.status === "running"
    const interval = isBusy ? 3000 : 30000
    const id = setInterval(() => { void refresh() }, interval)
    return () => clearInterval(id)
  }, [state?.status, refresh])

  const trigger = useCallback(async () => {
    setTriggering(true)
    try {
      const r = await fetch(`/api/v1/crews/${crewId}/provision?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "POST" })
      if (!r.ok) {
        const text = await r.text()
        toast.error(`Provision failed to start: ${text}`)
      } else {
        toast.success(`Provisioning started for ${crewSlug}`)
        void refresh()
      }
    } catch (err) {
      toast.error(`Provision failed: ${err instanceof Error ? err.message : err}`)
    } finally {
      setTriggering(false)
    }
  }, [crewId, crewSlug, workspaceId, refresh])

  if (!state) return null

  const needsProvision = state.hasConfig && !state.cached && state.status === "idle"
  if (state.status === "completed" || (!needsProvision && state.status !== "running" && state.status !== "failed")) {
    return null
  }

  if (state.status === "running") {
    return (
      <div className="rounded-xl border border-blue-500/30 bg-blue-500/5 px-4 py-3 flex items-center gap-3">
        <Spinner className="h-4 w-4 text-blue-300 shrink-0" />
        <div className="flex-1">
          <div className="text-sm text-blue-200">Building container image…</div>
          <div className="text-xs text-muted-foreground">
            Devcontainer features are installing. Agents in this crew will become runnable as soon as the image is ready (usually 30-90 s).
          </div>
        </div>
      </div>
    )
  }

  if (state.status === "failed") {
    return (
      <div className="rounded-xl border border-red-500/40 bg-red-500/5 px-4 py-3 flex items-start gap-3">
        <AlertTriangle className="h-4 w-4 text-red-300 shrink-0 mt-0.5" />
        <div className="flex-1 min-w-0">
          <div className="text-sm text-red-200">Last provision failed</div>
          {state.error && (
            <pre className="text-[11px] text-muted-foreground mt-1 whitespace-pre-wrap font-mono break-words max-h-24 overflow-y-auto">
              {state.error}
            </pre>
          )}
          <div className="text-xs text-muted-foreground mt-1.5">
            Fix the runtime config (Settings → Container image &amp; features) and try again.
          </div>
        </div>
        <button
          type="button"
          onClick={trigger}
          disabled={triggering}
          className="text-xs px-2.5 py-1.5 rounded bg-red-500/20 hover:bg-red-500/30 text-red-200 border border-red-500/40 shrink-0"
        >
          {triggering ? "Starting…" : "Retry"}
        </button>
      </div>
    )
  }

  // needs_provision (idle, hasConfig, no cached_image)
  return (
    <div className="rounded-xl border border-amber-500/40 bg-amber-500/5 px-4 py-3 flex items-center gap-3">
      <AlertTriangle className="h-4 w-4 text-amber-300 shrink-0" />
      <div className="flex-1 min-w-0">
        <div className="text-sm text-amber-200">Container image needs rebuild</div>
        <div className="text-xs text-muted-foreground">
          Runtime config changed — agents in this crew can&apos;t start until the image is rebuilt. Use the toolbar Build button or rebuild here.
        </div>
      </div>
      <button
        type="button"
        onClick={trigger}
        disabled={triggering}
        className="text-xs px-2.5 py-1.5 rounded bg-amber-500/25 hover:bg-amber-500/35 text-amber-200 border border-amber-500/40 shrink-0"
      >
        {triggering ? "Starting…" : "Build now"}
      </button>
    </div>
  )
}


function Collapsible({ title, summary, children }: {
  title: string
  summary: string
  children: React.ReactNode
}) {
  return (
    <details className="rounded-xl border border-white/8 bg-card overflow-hidden group">
      <summary className="px-4 py-3 flex items-center gap-2 text-sm cursor-pointer hover:bg-white/[0.02] list-none">
        <ChevronDown className="h-3 w-3 text-muted-foreground transition-transform group-open:rotate-0 -rotate-90" />
        <span className="text-foreground font-medium">{title}</span>
        <span className="text-xs text-muted-foreground truncate">{summary}</span>
      </summary>
      <div className="px-4 py-3 border-t border-white/5">
        {children}
      </div>
    </details>
  )
}


export { ProvisioningBanner, Collapsible }
