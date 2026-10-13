/** Client availability fence, never a substitute for server authorization. */
let workspaceId: string | null = null
let sessionId: string | null = null
let available = true
let epoch = 0
const pending = new Map<AbortController, number>()

export class WorkspaceUnavailableError extends Error {
  readonly code = "WORKSPACE_UNAVAILABLE"
  constructor() { super("The selected workspace is unavailable. Retry or choose a workspace."); this.name = "WorkspaceUnavailableError" }
}
function advance() {
  epoch++
  for (const controller of pending.keys()) controller.abort()
  pending.clear()
}
export function setWorkspaceRequestSession(id: string | null) {
  if (sessionId === id) return
  sessionId = id
  available = id === null
  advance()
}
export function setWorkspaceRequestAvailability(id: string | null, ready: boolean) {
  if (workspaceId === id && available === ready) return
  workspaceId = id
  available = ready
  advance()
}
function independent(input: RequestInfo | URL, init?: RequestInit) {
  const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url
  const path = new URL(raw, "http://workspace.invalid").pathname
  const method = (init?.method ?? (input instanceof Request ? input.method : "GET")).toUpperCase()
  if (!path.startsWith("/api/")) return true
  if (method === "GET" && path === "/api/v1/workspaces") return true
  const self: Record<string, readonly string[]> = {
    "/api/auth/csrf": ["GET"], "/api/auth/session": ["GET"],
    "/api/auth/callback/credentials": ["POST"], "/api/auth/signout": ["POST"],
    "/api/auth/token/refresh": ["POST"], "/api/v1/auth/config": ["GET"],
    "/api/v1/auth/sessions": ["GET"], "/api/v1/auth/cli-tokens": ["GET", "POST"],
    "/api/v1/users/me": ["GET", "PATCH", "PUT"], "/api/v1/users/me/password": ["POST"],
  }
  return self[path]?.includes(method) === true
    || method === "POST" && /^\/api\/v1\/auth\/sessions\/[^/]+\/revoke$/.test(path)
    || method === "DELETE" && /^\/api\/v1\/auth\/cli-tokens\/[^/]+$/.test(path)
}
export function captureWorkspaceRequest(input: RequestInfo | URL, init?: RequestInit): number | null {
  if (independent(input, init)) return null
  if (!available) throw new WorkspaceUnavailableError()
  return epoch
}
export function assertWorkspaceRequest(captured: number | null) {
  if (captured !== null && (captured !== epoch || !available)) throw new WorkspaceUnavailableError()
}

function track(controller: AbortController) {
  pending.set(controller, (pending.get(controller) ?? 0) + 1)
  let done = false
  return () => {
    if (done) return
    done = true
    const remaining = (pending.get(controller) ?? 1) - 1
    if (remaining > 0) pending.set(controller, remaining)
    else pending.delete(controller)
  }
}
function fencedResponse(response: Response, captured: number, abort: AbortController): Response {
  let body: ReadableStream<Uint8Array> | null | undefined
  return new Proxy(response, {get(target, property) {
    const value = Reflect.get(target, property, target)
    if (property === "clone") return () => {
      assertWorkspaceRequest(captured)
      return fencedResponse(target.clone(), captured, abort)
    }
    if (property === "body" && value) {
      assertWorkspaceRequest(captured)
      if (body === undefined) {
        const reader = (value as ReadableStream<Uint8Array>).getReader()
        const finish = track(abort)
        body = new ReadableStream<Uint8Array>({
          async pull(output) {
            try {
              assertWorkspaceRequest(captured)
              const next = await reader.read()
              assertWorkspaceRequest(captured)
              if (next.done) { finish(); output.close() }
              else output.enqueue(next.value)
            } catch (error) {
              finish()
              void reader.cancel().catch(() => {})
              try { assertWorkspaceRequest(captured) } catch (unavailable) { error = unavailable }
              output.error(error)
            }
          },
          async cancel(reason) { try { await reader.cancel(reason) } finally { finish() } },
        }, {highWaterMark:0})
      }
      return body
    }
    if (["json", "text", "blob", "arrayBuffer", "formData"].includes(String(property)) && typeof value === "function") {
      return async (...args: unknown[]) => {
        assertWorkspaceRequest(captured)
        const finish = track(abort)
        try {
          const result = await value.apply(target, args)
          assertWorkspaceRequest(captured)
          return result
        } catch (error) { assertWorkspaceRequest(captured); throw error }
        finally { finish() }
      }
    }
    return typeof value === "function" ? value.bind(target) : value
  }})
}

/** Fence dispatch, clones, active streams and late body parsing. A request
 * already admitted by the server cannot be undone by this client boundary. */
export async function workspaceFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const captured = captureWorkspaceRequest(input, init)
  if (captured === null) return fetch(input, init)
  const controller = new AbortController()
  const callerSignal = init?.signal ?? (input instanceof Request ? input.signal : undefined)
  const signal = callerSignal ? AbortSignal.any([controller.signal, callerSignal]) : controller.signal
  const finish = track(controller)
  try {
    const response = await fetch(input, {...init, signal})
    assertWorkspaceRequest(captured)
    return fencedResponse(response, captured, controller)
  } catch (error) { assertWorkspaceRequest(captured); throw error }
  finally { finish() }
}
