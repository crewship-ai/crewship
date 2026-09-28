"use client"

import { useCallback, useEffect, useState } from "react"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"

type ServiceState = { name: string; desired_state: string; observed_state: string; version: number; last_error?: string }
type Snapshot = { services: ServiceState[]; supported: boolean }

export function CrewServiceLifecycle({ crewId, workspaceId, canManage }: { crewId: string; workspaceId: string; canManage: boolean }) {
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null)
  const [error, setError] = useState(false)
  const [pending, setPending] = useState<string | null>(null)
  const base = `/api/v1/crews/${encodeURIComponent(crewId)}`
  const query = `?workspace_id=${encodeURIComponent(workspaceId)}`
  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const response = await apiFetch(`${base}/service-states${query}`, { signal })
      if (!response.ok) throw new Error("Could not load services")
      const data: Snapshot = await response.json()
      if (!signal?.aborted) { setSnapshot(data); setError(false) }
    } catch { if (!signal?.aborted) setError(true) }
  }, [base, query])
  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal)
    const timer = setInterval(() => { if (!document.hidden) void load(controller.signal) }, 15000)
    return () => { controller.abort(); clearInterval(timer) }
  }, [load])

  async function change(service: ServiceState, desired: "running" | "stopped") {
    if (!canManage || error || pending) return
    setPending(service.name)
    try {
      const response = await apiFetch(`${base}/services/${encodeURIComponent(service.name)}/state${query}`, {
        method: "PUT", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ desired_state: desired, expected_version: service.version }),
      })
      if (!response.ok) throw new Error(response.status === 409 ? "Service changed or runtime is unavailable. Refresh and retry." : "Could not update service")
      toast.success(desired === "running" ? "Service will be kept running" : "Service stop requested")
      await load()
    } catch (e) { toast.error(e instanceof Error ? e.message : "Could not update service"); await load() }
    finally { setPending(null) }
  }
  return <section className="space-y-3">
    <h2 className="text-lg font-semibold">Services</h2>
    <p className="text-sm text-muted-foreground">Keep a declared application running while agents sleep. Its requested state is restored after Crewship restarts. Stopped services stay stopped until you start them here.</p>
    {error && <p role="alert">Service state could not be verified. <button className="underline" onClick={() => void load()}>Retry</button></p>}
    {!snapshot && !error && <p>Loading services…</p>}
    {snapshot && !snapshot.supported && <p>This runtime does not support managed services.</p>}
    {snapshot?.services.length === 0 && <p className="text-sm text-muted-foreground">No services are declared for this crew.</p>}
    {snapshot?.services.map(service => <div key={service.name} className="rounded-xl border p-4 flex flex-wrap items-center justify-between gap-3">
      <div><h3 className="font-medium">{service.name}</h3><p className="text-sm text-muted-foreground">Requested: {service.desired_state === "on_demand" ? "On demand" : service.desired_state === "running" ? "Keep running" : "Stopped"} · Last reconciliation: {service.observed_state}</p>
        {service.last_error && <p role="status" className="text-sm">{service.last_error === "configuration_or_credentials_unavailable" ? "Check the service configuration and its assigned credentials." : "The container runtime could not apply this change. Crewship will retry."}</p>}
      </div>
      {canManage && snapshot.supported && <div className="flex gap-2">
        <Button size="sm" className="coarse:h-12" disabled={error || pending !== null || service.desired_state === "running"} aria-label={`${service.name}: Keep running`} onClick={() => void change(service, "running")}>Keep running</Button>
        <Button size="sm" variant="outline" className="coarse:h-12" disabled={error || pending !== null || service.desired_state === "stopped"} aria-label={`${service.name}: Stop`} onClick={() => void change(service, "stopped")}>Stop</Button>
      </div>}
    </div>)}
  </section>
}
