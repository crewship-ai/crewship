import type { ArtifactFile } from "@/hooks/use-artifact-project"
import { dashboardName, parseDashboardCollection } from "./artifact-dashboard"
import { validatePreview } from "./preview-runtime"

/**
 * Interactive artifacts: a Project's own HTML and scripts run inside the fixed
 * Pages runtime bootstrap on a separate site (opaque sandbox, no Crewship
 * credential). The page reaches its data only through the private MessagePort
 * handed to that frame. This host-side broker validates every request and calls
 * the existing artifact data endpoints with the viewer's own session, so the
 * server's data authority decides each read and write.
 */
export const INTERACTIVE_REQUEST = "crewship.artifact.request/v1"
export const INTERACTIVE_RESPONSE = "crewship.artifact.response/v1"
export const INTERACTIVE_READY = "crewship.artifact.ready/v1"
export const INTERACTIVE_METHODS = ["data.get", "data.list", "data.batch", "viewer"] as const
export type InteractiveMethod = typeof INTERACTIVE_METHODS[number]

/** One batch is 1 MiB of documents on the server; the envelope gets a fixed 8 KiB. */
export const INTERACTIVE_MAX_REQUEST_BYTES = 1024 * 1024 + 8192
/** The server already bounds one collection response to 16 MiB. */
export const INTERACTIVE_MAX_RESPONSE_BYTES = 16 * 1024 * 1024
export const INTERACTIVE_MAX_IN_FLIGHT = 4
export const INTERACTIVE_MAX_BATCH_WRITES = 50
/** The bootstrap refuses code above 2 MiB. */
export const INTERACTIVE_MAX_SOURCE_BYTES = 2 * 1024 * 1024

export interface InteractiveDocument { id: string; version: number; value: Record<string, unknown>; updated_at: string; source_label: string }
export interface InteractiveCollection { collection: string; documents: InteractiveDocument[] }
export interface InteractiveWrite { id: string; ifVersion: number; value: Record<string, unknown> }
export interface InteractiveViewer { name: string }

/** What the broker may do for the page. Every call is the viewer's own server request. */
export interface InteractiveDataAccess {
  get(collection: string, signal: AbortSignal): Promise<InteractiveCollection>
  list(signal: AbortSignal): Promise<{ collections: { name: string; documents: number }[] }>
  batch(collection: string, documents: InteractiveWrite[], signal: AbortSignal): Promise<InteractiveCollection>
  viewer(): InteractiveViewer
}

export class InteractiveRequestError extends Error {
  constructor(message: string, readonly code: "invalid" | "forbidden" | "conflict" | "limit" | "unavailable" = "invalid") { super(message) }
}

const plainObject = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === "object" && !Array.isArray(value) && Object.getPrototypeOf(value) === Object.prototype
const byteLength = (value: unknown) => {
  try { return new TextEncoder().encode(JSON.stringify(value)).length } catch { return Infinity }
}

export interface InteractiveBrokerOptions {
  /** Collections declared by the shown version's dashboard.json. */
  collections: string[]
  /** The viewer's Edit right on the shown version. The server still decides. */
  canWrite: boolean
  onReady?: () => void
}

/**
 * Listens only on its private port, never on window. Requests are an
 * allowlisted, size-bounded RPC; a stalled or hostile page cannot queue more
 * than INTERACTIVE_MAX_IN_FLIGHT server requests.
 */
export class InteractiveArtifactBroker {
  private closed = false
  private inFlight = 0
  private readonly abort = new AbortController()
  constructor(private readonly port: MessagePort, private readonly access: InteractiveDataAccess, private readonly options: InteractiveBrokerOptions) {
    port.onmessage = event => this.receive(event)
    port.start()
  }

  private receive(event: MessageEvent) {
    if (this.closed) return
    const message: unknown = event.data
    if (!plainObject(message)) return
    if (message.type === INTERACTIVE_READY && Object.keys(message).length === 1) { this.options.onReady?.(); return }
    if (message.type !== INTERACTIVE_REQUEST || !Number.isSafeInteger(message.id) || (message.id as number) < 1) return
    const id = message.id as number
    // A page must not hand the host further ports or transferables.
    if (event.ports.length) { this.reply(id, undefined, new InteractiveRequestError("Requests cannot carry ports.")); return }
    void this.handle(id, message)
  }

  private reply(id: number, result: unknown, error?: InteractiveRequestError) {
    if (this.closed) return
    if (!error && byteLength(result) > INTERACTIVE_MAX_RESPONSE_BYTES) error = new InteractiveRequestError("The response exceeds the 16 MiB limit.", "limit")
    this.port.postMessage(error ? { type: INTERACTIVE_RESPONSE, id, error: error.message, code: error.code } : { type: INTERACTIVE_RESPONSE, id, result })
  }

  private validate(message: Record<string, unknown>): { method: InteractiveMethod; collection?: string; documents?: InteractiveWrite[] } {
    if (byteLength(message) > INTERACTIVE_MAX_REQUEST_BYTES) throw new InteractiveRequestError("The request exceeds the 1 MiB batch limit.", "limit")
    const { params } = message
    const method = message.method as InteractiveMethod
    if (Object.keys(message).some(key => !["type", "id", "method", "params"].includes(key))) throw new InteractiveRequestError("Unsupported request.")
    if (typeof method !== "string" || !(INTERACTIVE_METHODS as readonly string[]).includes(method)) throw new InteractiveRequestError("Unsupported request.")
    if (!plainObject(params)) throw new InteractiveRequestError("Request parameters must be an object.")
    const allowed = method === "data.get" ? ["collection"] : method === "data.batch" ? ["collection", "documents"] : []
    if (Object.keys(params).some(key => !allowed.includes(key))) throw new InteractiveRequestError("Unsupported request parameter.")
    if (method === "data.list" || method === "viewer") return { method }
    const collection = params.collection
    if (!dashboardName(collection) || !this.options.collections.includes(collection)) throw new InteractiveRequestError("This collection is not declared in dashboard.json.")
    if (method === "data.get") return { method, collection }
    if (!this.options.canWrite) throw new InteractiveRequestError("You can read this artifact but not change its data.", "forbidden")
    const documents = params.documents
    if (!Array.isArray(documents) || documents.length < 1 || documents.length > INTERACTIVE_MAX_BATCH_WRITES) throw new InteractiveRequestError(`A batch writes 1 to ${INTERACTIVE_MAX_BATCH_WRITES} documents.`)
    const ids = new Set<string>()
    const writes = documents.map(document => {
      if (!plainObject(document) || Object.keys(document).length !== 3 || !dashboardName(document.id) || ids.has(document.id) || !Number.isSafeInteger(document.ifVersion) || (document.ifVersion as number) < 0 || !plainObject(document.value))
        throw new InteractiveRequestError("Each document needs a unique id, an ifVersion (0 for new) and an object value.")
      ids.add(document.id)
      return { id: document.id, ifVersion: document.ifVersion as number, value: document.value }
    })
    return { method, collection, documents: writes }
  }

  private async handle(id: number, message: Record<string, unknown>) {
    let request: ReturnType<InteractiveArtifactBroker["validate"]>
    try {
      if (this.inFlight >= INTERACTIVE_MAX_IN_FLIGHT) throw new InteractiveRequestError("Too many requests at once. Wait for a reply before sending more.", "limit")
      request = this.validate(message)
    } catch (error) { this.reply(id, undefined, error instanceof InteractiveRequestError ? error : new InteractiveRequestError("Invalid request.")); return }
    this.inFlight++
    try {
      const signal = this.abort.signal
      const result = request.method === "data.get" ? await this.access.get(request.collection!, signal)
        : request.method === "data.batch" ? await this.access.batch(request.collection!, request.documents!, signal)
        : request.method === "data.list" ? await this.access.list(signal)
        : { name: this.access.viewer().name }
      this.reply(id, result)
    } catch (error) {
      this.reply(id, undefined, error instanceof InteractiveRequestError ? error : new InteractiveRequestError("The request failed.", "unavailable"))
    } finally { this.inFlight-- }
  }

  close() {
    this.closed = true
    this.abort.abort()
    this.port.onmessage = null
    this.port.close()
  }
}

type Fetcher = (input: string, init?: RequestInit) => Promise<Response>

function serverError(status: number, body: unknown): InteractiveRequestError {
  const code = plainObject(body) ? body.code : undefined
  if (status === 409 && code === "artifact_data_conflict") return new InteractiveRequestError("A document changed. Read it again before writing; nothing was overwritten.", "conflict")
  if (status === 409) return new InteractiveRequestError("This artifact changed. Reload to see the latest version.", "conflict")
  if (status === 413) return new InteractiveRequestError("Dataset limit exceeded. Existing data was retained.", "limit")
  if (status === 401 || status === 403) return new InteractiveRequestError("You do not have permission for this request.", "forbidden")
  if (status === 400) return new InteractiveRequestError("The server refused this request as invalid.")
  return new InteractiveRequestError("Data is unavailable right now.", "unavailable")
}

const projectDocuments = (raw: unknown, collection: string, codeVersion: number): InteractiveCollection => {
  const parsed = parseDashboardCollection(raw, collection, codeVersion)
  if (!parsed) throw new InteractiveRequestError("The server returned invalid data.", "unavailable")
  return { collection, documents: parsed.documents.map(({ id, version, value, updated_at, source_label }) => ({ id, version, value, updated_at, source_label })) }
}

/**
 * The broker's server side: the same endpoints the Dashboard preview and the
 * CLI use, with the viewer's session supplied by `fetcher` (apiFetch).
 */
export function createInteractiveDataAccess({ fetcher, workspaceId, pageId, codeVersion, viewerName }: { fetcher: Fetcher; workspaceId: string; pageId: string; codeVersion: number; viewerName: () => string }): InteractiveDataAccess {
  const base = `/api/v1/pages/${encodeURIComponent("@" + pageId)}/artifact/data`
  const query = () => new URLSearchParams({ workspace_id: workspaceId, version: String(codeVersion) }).toString()
  const read = async (response: Response) => {
    let body: unknown = null
    try { body = await response.json() } catch { /* An empty or non-JSON error body. */ }
    if (!response.ok) throw serverError(response.status, body)
    return body
  }
  return {
    get: async (collection, signal) => projectDocuments(await read(await fetcher(`${base}/${encodeURIComponent(collection)}?${query()}`, { method: "GET", cache: "no-store", signal })), collection, codeVersion),
    list: async signal => {
      const body = await read(await fetcher(`${base}?${query()}`, { method: "GET", cache: "no-store", signal }))
      if (!plainObject(body) || !Array.isArray(body.collections)) throw new InteractiveRequestError("The server returned invalid data.", "unavailable")
      return { collections: body.collections.filter(plainObject).filter(item => dashboardName(item.name)).map(item => ({ name: item.name as string, documents: Number.isSafeInteger(item.documents) ? item.documents as number : 0 })) }
    },
    batch: async (collection, documents, signal) => projectDocuments(await read(await fetcher(`${base}/${encodeURIComponent(collection)}?${new URLSearchParams({ workspace_id: workspaceId })}`, {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ base_version: codeVersion, documents }), signal,
    })), collection, codeVersion),
    viewer: () => ({ name: viewerName().slice(0, 120) }),
  }
}

/**
 * Runs inside the runtime frame before any author script. Keep it closure-free:
 * it is serialized with Function.prototype.toString.
 */
export function installInteractiveRuntime(config: { html: string; requestType: string; responseType: string; readyType: string; maxInFlight: number }): void {
  const scope = window as unknown as { __crewshipPagesPort?: MessagePort; crewship?: unknown }
  const port = scope.__crewshipPagesPort
  delete scope.__crewshipPagesPort
  if (!port) return
  const pending = new Map<number, { resolve: (value: unknown) => void; reject: (error: Error) => void }>()
  let next = 0
  port.addEventListener("message", event => {
    const message = event.data
    if (!message || message.type !== config.responseType) return
    const waiter = pending.get(message.id)
    if (!waiter) return
    pending.delete(message.id)
    if (typeof message.error === "string") waiter.reject(Object.assign(new Error(message.error), { code: message.code }))
    else waiter.resolve(message.result)
  })
  port.start()
  const call = (method: string, params: Record<string, unknown>) => new Promise((resolve, reject) => {
    if (pending.size >= config.maxInFlight) { reject(Object.assign(new Error("Too many requests at once. Wait for a reply before sending more."), { code: "limit" })); return }
    const id = ++next
    pending.set(id, { resolve, reject })
    try { port.postMessage({ type: config.requestType, id, method, params }) } catch (error) { pending.delete(id); reject(error) }
  })
  const data = Object.freeze({
    get: (collection: string) => call("data.get", { collection }),
    list: () => call("data.list", {}),
    batch: (collection: string, documents: unknown) => call("data.batch", { collection, documents }),
  })
  Object.defineProperty(window, "crewship", { value: Object.freeze({ version: 1, data, viewer: () => call("viewer", {}) }), enumerable: false, configurable: false, writable: false })
  const parsed = new DOMParser().parseFromString(config.html, "text/html")
  if (parsed.title) document.title = parsed.title
  for (const style of Array.from(parsed.head.querySelectorAll("style"))) document.head.append(document.importNode(style, true))
  const root = document.getElementById("root")
  if (root) root.remove()
  const imported: Node[] = []
  for (const node of [...Array.from(parsed.head.querySelectorAll("script")), ...Array.from(parsed.body.childNodes)]) {
    const copy = document.importNode(node, true)
    imported.push(copy)
    document.body.append(copy)
  }
  // Imported scripts are inert. Recreate inline ones in document order with the
  // bootstrap nonce; external sources are dropped (the runtime has no network).
  // Only author nodes are touched, never the bootstrap's own script.
  const nonce = (document.currentScript as HTMLScriptElement | null)?.nonce ?? ""
  const scripts: HTMLScriptElement[] = []
  for (const node of imported) {
    if (node instanceof HTMLScriptElement) scripts.push(node)
    else if (node instanceof Element) scripts.push(...Array.from(node.querySelectorAll("script")))
  }
  for (const old of scripts) {
    const type = (old.getAttribute("type") ?? "").trim().toLowerCase()
    if (old.hasAttribute("src") || !["", "text/javascript", "module"].includes(type)) { old.remove(); continue }
    const script = document.createElement("script")
    if (type === "module") script.type = "module"
    script.nonce = nonce
    script.textContent = old.textContent
    old.replaceWith(script)
  }
  port.postMessage({ type: config.readyType })
}

/** The bootstrap artifact for one screen. All CSS files apply, as in the static preview. */
export function interactiveArtifactSource(files: ArtifactFile[], path: string): { format: "crewship-page-preview/v1"; javascript: string; css: string; toolchain: string } {
  const file = files.find(candidate => candidate.path === path && candidate.media_type === "text/html")
  if (!file) throw new InteractiveRequestError("This screen is not part of the artifact.")
  const config = { html: file.content, requestType: INTERACTIVE_REQUEST, responseType: INTERACTIVE_RESPONSE, readyType: INTERACTIVE_READY, maxInFlight: INTERACTIVE_MAX_IN_FLIGHT }
  const javascript = `(${installInteractiveRuntime.toString()})(${JSON.stringify(config).replaceAll("<", "\\u003c")});`
  const css = files.filter(candidate => candidate.media_type === "text/css").map(candidate => candidate.content).join("\n")
  if (new TextEncoder().encode(javascript + css).length > INTERACTIVE_MAX_SOURCE_BYTES) throw new InteractiveRequestError("This screen exceeds the 2 MiB interactive limit.", "limit")
  return { format: "crewship-page-preview/v1", javascript, css, toolchain: "crewship-interactive-artifact/v1" }
}

/** The same separate-site rule as Page applications. */
export function validateInteractiveRuntime(runtimeURL: string, studioURL: string, developmentSameOrigin: boolean) {
  validatePreview({ format: "crewship-page-preview/v1", javascript: "", css: "", toolchain: "" }, runtimeURL, studioURL, developmentSameOrigin)
}

/**
 * One frame, one document, one port. The first load of the runtime gets the
 * boot message and the private port; any later load means the frame navigated
 * away from the verified bootstrap and ends the session.
 */
export class InteractiveFrameSession {
  private broker: InteractiveArtifactBroker | null = null
  private loaded = false
  private ended = false
  constructor(private readonly artifact: ReturnType<typeof interactiveArtifactSource>, private readonly access: InteractiveDataAccess, private readonly options: InteractiveBrokerOptions) {}
  /** Returns false when the session ended (navigation or close). */
  load(target: Window | null | undefined): boolean {
    if (this.ended) return false
    if (this.loaded || !target) { this.close(); return false }
    this.loaded = true
    const channel = new MessageChannel()
    this.broker = new InteractiveArtifactBroker(channel.port1, this.access, this.options)
    // '*' is required for the opaque sandbox origin; the WindowProxy is the exact frame we created.
    target.postMessage({ type: "crewship.pages.boot/v1", artifact: this.artifact }, "*", [channel.port2])
    return true
  }
  close() { this.ended = true; this.broker?.close(); this.broker = null }
}
