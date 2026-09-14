import { normalizePageTheme } from "./theme"
import type { WirePage } from "@/hooks/use-pages"
import type { PreviewArtifact } from "@/hooks/use-page-preview"

export function previewSnapshot(page: WirePage, theme?: unknown) {
  return {
    slug: page.slug ?? "", name: page.name ?? "",
    ...(theme === undefined ? {} : { theme: normalizePageTheme(theme) }),
    panels: (Array.isArray(page.panels) ? page.panels : [])
      .filter(panel => panel.sealed !== true && typeof panel.id === "string")
      .map(panel => ({ id: panel.id!, title: panel.title ?? "", schema: panel.schema ?? "", state: panel.state ?? "unknown", data: panel.data ?? panel.payload ?? null, producedAt: panel.provenance?.produced_at ?? null })),
  }
}

// Code travels by structured clone to a separate-site bootstrap, never through
// srcdoc or an application HTML string. The server additionally validates eTLD+1.
// This is a URL sanity check. The server enforces registrable-site separation
// using the public suffix list before serving an application or preview.
export function validatePreview(artifact: PreviewArtifact, runtimeURL: string, studioURL: string, developmentSameOrigin = false) {
  const runtime = new URL(runtimeURL)
  const studio = new URL(studioURL)
  if (!["http:", "https:"].includes(runtime.protocol) || (runtime.hostname === studio.hostname && !(developmentSameOrigin && runtime.origin === studio.origin)) || runtime.username || runtime.password || runtime.search || runtime.hash || runtime.pathname !== "/api/v1/pages/runtime/bootstrap" || (studio.protocol === "https:" && runtime.protocol !== "https:")) throw new Error("The preview needs a separate application domain.")
  if (artifact.format !== "crewship-page-preview/v1" || new TextEncoder().encode(artifact.javascript + artifact.css).length > 2 * 1024 * 1024) throw new Error("Unsupported or oversized preview artifact")
}

export interface PageRPCRequest {
  id: number
  method: "runAction" | "getActionStatus" | "getPanelHistory"
  params: Record<string, unknown>
}
export type PageRequestHandler = (request: PageRPCRequest, signal: AbortSignal) => Promise<unknown>

// At most one message is in flight and one latest snapshot is retained.
// A stalled application cannot accumulate an unbounded MessagePort queue.
export class PreviewSnapshotChannel {
  private pending: ReturnType<typeof previewSnapshot> | null = null
  private sequence = 0
  private inFlight = 0
  private timer: ReturnType<typeof setTimeout> | null = null
  private closed = false
  private readonly abort = new AbortController()
  private requesting = false
  private lastRequest = 0
  constructor(private readonly port: MessagePort, private readonly handleRequest?: PageRequestHandler, private readonly onRendered?: () => void) {
    port.onmessage = event => {
      if (this.closed) return
      if (event.data?.type === "crewship.pages.rendered/v1") { this.onRendered?.(); return }
      if (event.data?.type === "crewship.pages.request/v1") { void this.request(event.data); return }
      if (!this.inFlight || event.data?.type !== "crewship.pages.snapshot-ack/v1" || event.data.seq !== this.inFlight) return
      this.inFlight = 0
      this.schedule()
    }
    port.start()
  }
  private async request(value: unknown) {
    if (this.closed || !value || typeof value !== "object") return
    const request = value as PageRPCRequest
    if (!Number.isSafeInteger(request.id) || request.id < 1) return
    const reply = (result: unknown, error?: string) => {
      if (!this.closed) this.port.postMessage({ type: "crewship.pages.response/v1", id: request.id, result, error })
    }
    try {
      if (new TextEncoder().encode(JSON.stringify(value)).length > 32 * 1024) throw new Error("Action request exceeds 32 KiB.")
      if (!["runAction", "getActionStatus", "getPanelHistory"].includes(request.method) || !request.params || typeof request.params !== "object" || Array.isArray(request.params)) throw new Error("Unsupported Page request.")
      if (!this.handleRequest) throw new Error("Actions are available only in published applications.")
      if (this.requesting || Date.now() - this.lastRequest < 250) throw new Error("Another request is active. Try again shortly.")
    } catch (error) { reply(null, error instanceof Error ? error.message : "Invalid request."); return }
    this.requesting = true
    this.lastRequest = Date.now()
    try { reply(await this.handleRequest!(request, this.abort.signal)) }
    catch (error) { reply(null, error instanceof Error ? error.message : "Page request failed.") }
    finally { this.requesting = false }
  }
  push(snapshot: ReturnType<typeof previewSnapshot>) {
    if (this.closed) return
    if (new TextEncoder().encode(JSON.stringify(snapshot)).length > 1024 * 1024) throw new Error("Preview data exceeds the 1 MiB limit. Reduce the panel payloads.")
    this.pending = snapshot
    this.schedule()
  }
  private schedule() {
    if (this.closed || this.inFlight || this.timer || !this.pending) return
    this.timer = setTimeout(() => {
      this.timer = null
      if (this.closed || !this.pending) return
      const snapshot = this.pending
      this.pending = null
      this.inFlight = ++this.sequence
      this.port.postMessage({ type: "crewship.pages.snapshot/v1", seq: this.inFlight, snapshot })
    }, 100)
  }
  close() {
    this.closed = true
    this.abort.abort()
    this.pending = null
    if (this.timer) clearTimeout(this.timer)
    this.port.onmessage = null
    this.port.close()
  }
}
