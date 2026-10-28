"use client"
import { useCallback, useEffect, useMemo, useState } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useSessionSafe } from "@/hooks/use-auth"
import { apiFetch, AUTH_EVENT } from "@/lib/api-fetch"
import { artifactSlides } from "@/lib/feature-flags"
import { parseArtifactSlides } from "@/lib/pages/artifact-slides"

interface Scope { workspaceId: string; pageId: string; codeVersion: number; artifactKind?: string }
const denied = (error: unknown) => [401, 403, 404, 409].includes(Number((error as { status?: number })?.status))
const aborted = () => new DOMException("Aborted", "AbortError")

async function boundedBody(response: Response, limit: number, signal: AbortSignal): Promise<Uint8Array<ArrayBuffer>> {
  const reader = response.body?.getReader()
  if (!reader) throw new Error("Invalid Slides response")
  const bytes = new Uint8Array(limit)
  let length = 0
  try {
    while (true) {
      const { done, value } = await reader.read()
      if (signal.aborted) throw aborted()
      if (done) return bytes.slice(0, length)
      if (value.byteLength > limit - length) throw new Error("Slides response exceeds the data limit")
      bytes.set(value, length); length += value.byteLength
    }
  } finally { await reader.cancel().catch(() => {}); reader.releaseLock() }
}

/** Every response is fetched under the human's identity and an explicit selected code pin. */
export function useArtifactSlides({ workspaceId, pageId, codeVersion, artifactKind }: Scope) {
  const user = useSessionSafe()?.data?.user?.id ?? "", client = useQueryClient()
  const scope = JSON.stringify([user, workspaceId, pageId, codeVersion, artifactKind])
  const generation = useMemo(() => ({ scope, retired: false, downloads: new Set<AbortController>() }), [scope])
  const [retired, setRetired] = useState<string | null>(null)
  const enabled = artifactSlides() && artifactKind === "slides" && Boolean(user && workspaceId && pageId) && Number.isSafeInteger(codeVersion) && codeVersion > 0 && retired !== scope
  const key = useMemo(() => ["artifact-slides", scope], [scope])
  const retire = useCallback(() => {
    generation.retired = true
    for (const controller of generation.downloads) controller.abort()
    setRetired(scope)
    void client.cancelQueries({ queryKey: key, exact: true })
    client.removeQueries({ queryKey: key, exact: true })
  }, [client, generation, key, scope])
  const query = useQuery({
    queryKey: key, enabled, retry: false, gcTime: 0, staleTime: 0, refetchInterval: 5000, refetchIntervalInBackground: false,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(`/api/v1/pages/${encodeURIComponent("@" + pageId)}/artifact/slides?${new URLSearchParams({ workspace_id: workspaceId, version: String(codeVersion) })}`, { method: "GET", cache: "no-store", signal })
      if (signal.aborted || generation.retired) throw aborted()
      if (!response.ok) throw Object.assign(new Error("Slides preview is unavailable. Reload to check access."), { status: response.status })
      const bytes = await boundedBody(response, 16 << 20, signal)
      if (signal.aborted || generation.retired) throw aborted()
      const projection = parseArtifactSlides(JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes)), pageId)
      if (!projection) throw new Error("This Slides format is unsupported.")
      return projection
    },
  })
  const status = (query.error as { status?: number } | null)?.status
  useEffect(() => { if (denied(query.error)) retire() }, [status, query.error, retire])
  useEffect(() => {
    generation.retired = false
    window.addEventListener(AUTH_EVENT, retire)
    return () => {
      generation.retired = true
      for (const controller of generation.downloads) controller.abort()
      window.removeEventListener(AUTH_EVENT, retire)
      void client.cancelQueries({ queryKey: key, exact: true })
      client.removeQueries({ queryKey: key, exact: true })
    }
  }, [client, generation, key, retire])
  const downloadPDF = async () => {
    if (!enabled || generation.retired || query.error) return null
    const controller = new AbortController()
    generation.downloads.add(controller)
    try {
      const response = await apiFetch(`/api/v1/pages/${encodeURIComponent("@" + pageId)}/artifact/export.pdf?${new URLSearchParams({ workspace_id: workspaceId, version: String(codeVersion) })}`, { method: "GET", cache: "no-store", signal: controller.signal })
      if (!response.ok) throw Object.assign(new Error("PDF is unavailable. Reload to check access."), { status: response.status })
      const bytes = await boundedBody(response, 8 << 20, controller.signal)
      if (controller.signal.aborted || generation.retired) return null
      if (response.headers.get("content-type")?.split(";")[0].trim().toLowerCase() !== "application/pdf" || new TextDecoder().decode(bytes.subarray(0, 5)) !== "%PDF-") throw new Error("Invalid PDF response")
      return bytes
    } catch (error) { if (denied(error)) retire(); throw error }
    finally { generation.downloads.delete(controller) }
  }
  return { scope, enabled, projection: enabled && !query.error && !generation.retired ? query.data : undefined, error: query.error || retired === scope, downloadPDF }
}
