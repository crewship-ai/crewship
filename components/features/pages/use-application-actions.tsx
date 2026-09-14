"use client"

import { useCallback, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import type { PageRequestHandler } from "@/lib/pages/preview-runtime"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"

interface DeclaredAction { routine_changed_since_publication?: boolean; id: string; kind: string; label: string; routine?: string; confirm?: { title?: string; body?: string; confirm_label?: string; cancel_label?: string } }
interface Prompt { action: DeclaredAction; inputs: Record<string, unknown>; finish: (confirmed: boolean) => void }
function identifier(value: unknown): string {
  if (typeof value !== "string" || !value || value.length > 128) throw new Error("Invalid action identifier.")
  return encodeURIComponent(value)
}
export function useApplicationActions(workspace: string, slug: string, publication: number) {
  const [prompt, setPrompt] = useState<Prompt | null>(null)
  const handleRequest = useCallback<PageRequestHandler>(async (request, signal) => {
    signal.throwIfAborted()
    const endpoint = `/api/v1/pages/${encodeURIComponent(slug)}`
    const query = new URLSearchParams({ workspace_id: workspace })
    const read = async (response: Response) => {
      const body = await response.json()
      if (!response.ok) throw new Error(body?.error ?? "Page action failed.")
      return body
    }
    if (request.method === "getPanelHistory") {
      const limit = request.params.limit ?? 20
      const before = request.params.before
      if (!Number.isSafeInteger(limit) || Number(limit) < 1 || Number(limit) > 20) throw new Error("History limit must be between 1 and 20.")
      if (before !== undefined && (!Number.isSafeInteger(before) || Number(before) < 1)) throw new Error("Invalid history cursor.")
      query.set("publication", String(publication))
      query.set("limit", String(limit))
      if (before !== undefined) query.set("before", String(before))
      return read(await apiFetch(`${endpoint}/application/panels/${identifier(request.params.panelId)}/history?${query}`, { signal }))
    }
    if (request.method === "getActionStatus") {
      return read(await apiFetch(`${endpoint}/application/actions/${identifier(request.params.pendingId)}?${query}`, { signal }))
    }
    const panel = identifier(request.params.panelId)
    const actionId = identifier(request.params.actionId)
    const key = request.params.idempotencyKey
    if (typeof key !== "string" || !key || new TextEncoder().encode(key).length > 128 || /[^\x20-\x7e]/.test(key)) throw new Error("Invalid idempotency key.")
    const inputs = request.params.inputs ?? {}
    if (typeof inputs !== "object" || inputs === null || Array.isArray(inputs)) throw new Error("Action inputs must be an object.")
    query.set("publication", String(publication))
    const definition = await read(await apiFetch(`${endpoint}/panels/${panel}/actions?${query}`, { signal }))
    const action: DeclaredAction | undefined = definition.actions?.find((value: DeclaredAction) => value.id === request.params.actionId && value.kind === "call")
    if (!action) throw new Error("This Page does not declare that routine action.")
    signal.throwIfAborted()
    const confirmed = await new Promise<boolean>(resolve => {
      let settled = false
      const finish = (value: boolean) => {
        if (settled) return
        settled = true
        clearTimeout(timer)
        signal.removeEventListener("abort", cancel)
        setPrompt(current => current?.finish === finish ? null : current)
        resolve(value)
      }
      const cancel = () => finish(false)
      const timer = setTimeout(cancel, 60_000)
      signal.addEventListener("abort", cancel, { once: true })
      setPrompt({ action, inputs: inputs as Record<string, unknown>, finish })
    })
    if (!confirmed) throw new Error("Action cancelled; no request was submitted.")
    signal.throwIfAborted()
    try {
      return await read(await apiFetch(`${endpoint}/application/actions/${panel}/${actionId}?${query}`, {
        method: "POST", signal, headers: { "Content-Type": "application/json", "Idempotency-Key": key },
        body: JSON.stringify({ publication, inputs }),
      }))
    } catch (error) {
      if (error instanceof TypeError || signal.aborted) throw new Error("Could not confirm the outcome. The action may have been queued; check activity before retrying with the same idempotency key.")
      throw error
    }
  }, [workspace, slug, publication])
  const confirmation = <Dialog open={!!prompt} onOpenChange={open => { if (!open) prompt?.finish(false) }}>
    {prompt && <DialogContent>
      <DialogHeader>
        <DialogTitle>{prompt.action.confirm?.title ?? prompt.action.label}</DialogTitle>
        <DialogDescription>{prompt.action.confirm?.body ?? "This application is asking Crewship to queue a routine."}</DialogDescription>
      </DialogHeader>
      <p className="text-sm">Page {slug} · Version {publication} · Routine {prompt.action.routine}</p>
      {prompt.action.routine_changed_since_publication && <p role="alert" className="text-sm">The routine definition has changed since this application was published.</p>}
      <p className="text-sm text-muted-foreground">The routine uses its current definition and scripts. Publishing this Page does not freeze the routine.</p>
      <pre className="max-h-64 overflow-auto whitespace-pre-wrap rounded bg-muted p-3 text-xs">{JSON.stringify(prompt.inputs, null, 2)}</pre>
      <div className="flex justify-end gap-2">
        <Button variant="outline" onClick={() => prompt.finish(false)}>{prompt.action.confirm?.cancel_label ?? "Cancel"}</Button>
        <Button onClick={() => prompt.finish(true)}>{prompt.action.confirm?.confirm_label ?? "Run routine"}</Button>
      </div>
    </DialogContent>}
  </Dialog>
  return { handleRequest, confirmation }
}
