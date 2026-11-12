import { act, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { INTERACTIVE_REQUEST, INTERACTIVE_RESPONSE } from "@/lib/pages/interactive-artifact"
import { viewerPrefsKey } from "@/lib/pages/viewer-prefs"
import { ArtifactInteractivePreview } from "../artifact-interactive-preview"

const state = vi.hoisted(() => ({ user: { id: "alice", name: "Alice" } as { id: string; name: string } | null, send: vi.fn() }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: state.user ? { user: state.user } : null }) }))
vi.mock("@/lib/api-fetch", () => ({ AUTH_EVENT: "auth:session-expired", apiFetch: (...args: unknown[]) => state.send(...args) }))

const files = [{ path: "index.html", media_type: "text/html", content: "<p id=x>Poll</p><script>crewship.data.get('votes')</script>" }]
const runtime = { runtime_url: "https://pages.example.net/api/v1/pages/runtime/bootstrap", development_same_origin: false, collections: ["votes"] }
const props = { workspaceId: "ws", pageId: "pg", codeVersion: 2, files, path: "index.html", title: "Preview of Poll", runtime, canWrite: true }
const agent = navigator.userAgent
beforeEach(() => {
  state.user = { id: "alice", name: "Alice" }
  Object.defineProperty(navigator, "userAgent", { configurable: true, value: "Mozilla/5.0 (X11; Linux x86_64) Chrome/130.0 Safari/537.36" })
})
afterEach(() => Object.defineProperty(navigator, "userAgent", { configurable: true, value: agent }))

it("loads the separate-site runtime in an opaque sandbox, never the author HTML as srcdoc", () => {
  render(<ArtifactInteractivePreview {...props} />)
  const frame = screen.getByTitle("Preview of Poll") as HTMLIFrameElement
  expect(frame.getAttribute("src")).toBe(runtime.runtime_url)
  expect(frame.getAttribute("sandbox")).toBe("allow-scripts")
  expect(frame.hasAttribute("srcdoc")).toBe(false)
  expect(frame.getAttribute("referrerpolicy")).toBe("no-referrer")
  expect(document.body.innerHTML).not.toContain("crewship.data.get")
})

it("hands the private port to the first load only and stops on navigation", () => {
  render(<ArtifactInteractivePreview {...props} />)
  const frame = screen.getByTitle("Preview of Poll") as HTMLIFrameElement
  const post = vi.fn()
  Object.defineProperty(frame, "contentWindow", { configurable: true, value: { postMessage: post } })
  fireEvent.load(frame)
  expect(post).toHaveBeenCalledTimes(1)
  expect(post.mock.calls[0][0]).toMatchObject({ type: "crewship.pages.boot/v1", artifact: { format: "crewship-page-preview/v1" } })
  expect(post.mock.calls[0][2]).toHaveLength(1)
  fireEvent.load(frame)
  expect(post).toHaveBeenCalledTimes(1)
  expect(screen.getByRole("alert").textContent).toContain("navigated away")
  ;(post.mock.calls[0][2] as MessagePort[])[0].close()
})

it.each([
  ["no runtime origin", { ...runtime, runtime_url: "" }, "separate application domain"],
  ["a runtime on the Studio site", { ...runtime, runtime_url: `${window.location.origin}/api/v1/pages/runtime/bootstrap` }, "separate application domain"],
])("refuses %s without rendering a frame", (_, value, message) => {
  render(<ArtifactInteractivePreview {...props} runtime={value} />)
  expect(screen.queryByTitle("Preview of Poll")).toBeNull()
  expect(screen.getByRole("alert").textContent).toContain(message)
})

it("keeps Dashboard filter preferences local to the current reader and closes them for other previews", async () => {
  const aliceKey = viewerPrefsKey("alice", "ws", "pg")
  const bobKey = viewerPrefsKey("bob", "ws", "pg")
  const storage = new Map([[aliceKey, '{"crew":"Alpha"}']])
  const get = vi.spyOn(window.localStorage, "getItem").mockImplementation(key => storage.get(key) ?? null)
  const set = vi.spyOn(window.localStorage, "setItem").mockImplementation((key, value) => { storage.set(key, value) })
  const remove = vi.spyOn(window.localStorage, "removeItem").mockImplementation(key => { storage.delete(key) })
  state.send.mockClear()
  const ports: MessagePort[] = []
  const bind = () => {
    const frame = screen.getByTitle("Preview of Poll") as HTMLIFrameElement
    const post = vi.fn()
    Object.defineProperty(frame, "contentWindow", { configurable: true, value: { postMessage: post } })
    fireEvent.load(frame)
    const port = (post.mock.calls[0][2] as MessagePort[])[0]
    ports.push(port)
    const replies = new Map<number, unknown>()
    port.onmessage = event => { if (event.data?.type === INTERACTIVE_RESPONSE) replies.set(event.data.id, event.data) }
    return async (id: number, method: string, params: object = {}) => {
      port.postMessage({ type: INTERACTIVE_REQUEST, id, method, params })
      await vi.waitFor(() => expect(replies.has(id)).toBe(true))
      return replies.get(id)
    }
  }
  const view = render(<ArtifactInteractivePreview {...props} canWrite={false} rememberViewerPrefs />)
  try {
    const alice = bind()
    expect(await alice(1, "prefs.get")).toMatchObject({ result: { value: { crew: "Alpha" } } })
    state.user = { id: "bob", name: "Bob" }
    view.rerender(<ArtifactInteractivePreview {...props} canWrite={false} rememberViewerPrefs />)
    const bob = bind()
    expect(await bob(1, "prefs.get")).toMatchObject({ result: { value: null } })
    expect(await bob(2, "prefs.set", { value: { crew: "Beta" } })).toMatchObject({ result: { saved: true } })
    expect(window.localStorage.getItem(aliceKey)).toBe('{"crew":"Alpha"}')
    expect(window.localStorage.getItem(bobKey)).toBe('{"crew":"Beta"}')
    view.rerender(<ArtifactInteractivePreview {...props} canWrite={false} />)
    expect(await bind()(1, "prefs.get")).toMatchObject({ code: "unavailable" })
    expect(state.send).not.toHaveBeenCalled()
  } finally {
    view.unmount()
    ports.forEach(port => port.close())
    get.mockRestore(); set.mockRestore(); remove.mockRestore()
  }
})

it("removes the frame when the session ends", () => {
  render(<ArtifactInteractivePreview {...props} />)
  expect(screen.getByTitle("Preview of Poll")).toBeTruthy()
  act(() => { window.dispatchEvent(new Event("auth:session-expired")) })
  expect(screen.queryByTitle("Preview of Poll")).toBeNull()
})
