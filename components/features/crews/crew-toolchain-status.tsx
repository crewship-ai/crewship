"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { RefreshCw } from "lucide-react"
import { z } from "zod"
import { Button } from "@/components/ui/button"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"

const tool = z.object({ binary: z.string().max(80), version: z.string().max(128).optional(), status: z.string().max(80) })
const evidence = z.object({
  cached_image: z.string().nullable().optional(),
  toolchain: z.object({
    requested: z.array(z.object({ binary: z.string().max(80), source: z.string().max(80), selector: z.string().max(128).optional(), exact: z.boolean() })).max(128).nullish(),
    built: z.object({
      schema_version: z.number(), status: z.string(), image_id: z.string().optional(),
      tools: z.array(tool).max(128),
      qualification: z.object({ status: z.string(), image_id: z.string(), tools: z.array(z.object({ binary: z.string().max(80), status: z.string().max(80) })).max(128) }).nullish(),
    }).nullish(),
  }).nullish(),
})
type Evidence = z.infer<typeof evidence>
type State = { scope: string; loading: boolean; data?: Evidence; error?: boolean }

export function CrewToolchainStatus({ crewId, workspaceId, refreshKey = "" }: { crewId: string; workspaceId: string; refreshKey?: string }) {
  const scope = `${workspaceId}/${crewId}`
  const [state, setState] = useState<State>({ scope, loading: true })
  const sequence = useRef(0)
  const active = useRef<AbortController | null>(null)
  const refresh = useCallback(async () => {
    const request = ++sequence.current
    active.current?.abort()
    const controller = new AbortController()
    active.current = controller
    setState({ scope, loading: true })
    try {
      const response = await apiFetch(`/api/v1/crews/${encodeURIComponent(crewId)}/provision?workspace_id=${encodeURIComponent(workspaceId)}`, { signal: controller.signal })
      if (!response.ok) throw new Error("Unavailable")
      const data = evidence.parse(await response.json())
      if (sequence.current === request) setState({ scope, loading: false, data })
    } catch {
      if (sequence.current === request && !controller.signal.aborted) setState({ scope, loading: false, error: true })
    }
  }, [crewId, workspaceId, scope])
  const cancel = useCallback(() => { ++sequence.current; active.current?.abort() }, [])
  useEffect(() => {
    void refresh()
    return cancel
  }, [refresh, refreshKey, cancel])
  useRealtimeEventSafe("provision.completed", (event) => { if (event.payload.crew_id === crewId) void refresh() })
  useRealtimeEventSafe("provision.failed", (event) => { if (event.payload.crew_id === crewId) void refresh() })
  useRealtimeEventSafe("realtime.reconnected", () => { void refresh() })

  const current: State = state.scope === scope ? state : { scope, loading: true }
  const data = current.data
  const built = data?.toolchain?.built
  const bound = built?.schema_version === 1 && built.status === "recorded" && Boolean(built.image_id) && built.image_id === data?.cached_image
  const checked = bound && built && built.qualification?.image_id === built.image_id ? built.qualification : null
  const requested = data?.toolchain?.requested ?? []
  const observed = bound ? built?.tools ?? [] : []
  const names = [...new Set([...requested.map((t) => t.binary), ...observed.map((t) => t.binary)])]
  const overall = checked?.status === "passed" ? "Startup checks passed" : !checked || checked.status === "unavailable" ? "Startup checks not available" : "Startup checks did not pass"

  return (
    <section className="rounded-lg border p-3 space-y-3" aria-label="Built AI tools">
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-sm font-medium">AI tools in selected build</h3>
        <Button type="button" variant="ghost" size="sm" className="h-8 coarse:h-12 coarse:min-w-12" onClick={() => void refresh()} disabled={current.loading} aria-label="Refresh built tool versions">
          <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">Versions recorded in the selected image. A running environment may still use an older build or modified files.</p>
      {current.loading ? <p className="text-xs" role="status">Loading tool versions…</p> : current.error ? <p className="text-xs text-muted-foreground" role="status">Could not load tool versions. Try refreshing.</p> : (
        <>
          <p className="text-xs" role="status">{overall}. Provider sign-in is tested separately.</p>
          {names.length === 0 ? <p className="text-xs text-muted-foreground">No AI CLI version information recorded.</p> : (
            <div className="overflow-x-auto">
              <table className="w-full text-xs text-left">
                <caption className="sr-only">Requested and built AI CLI versions</caption>
                <thead><tr className="border-b"><th className="py-2 pr-3 font-medium" scope="col">Tool</th><th className="py-2 pr-3 font-medium" scope="col">Requested</th><th className="py-2 font-medium" scope="col">Built version</th></tr></thead>
                <tbody>{names.map((name) => {
                  const want = requested.find((t) => t.binary === name)
                  const got = observed.find((t) => t.binary === name)
                  const selector = want?.selector ? `${want.selector}${want.exact ? " (pinned)" : ""}` : want?.source === "devcontainer_feature" ? "Devcontainer feature" : want?.source === "installer" ? "Installer" : "Not specified"
                  return <tr key={name} className="border-b last:border-0"><th scope="row" className="py-2 pr-3 font-mono font-normal">{name}</th><td className="py-2 pr-3">{selector}</td><td className="py-2">{got?.status === "observed" && got.version ? got.version : "Not recorded"}</td></tr>
                })}</tbody>
              </table>
            </div>
          )}
        </>
      )}
    </section>
  )
}
