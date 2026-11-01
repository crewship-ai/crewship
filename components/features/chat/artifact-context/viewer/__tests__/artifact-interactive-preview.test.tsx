import { act, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
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

it("removes the frame when the session ends", () => {
  render(<ArtifactInteractivePreview {...props} />)
  expect(screen.getByTitle("Preview of Poll")).toBeTruthy()
  act(() => { window.dispatchEvent(new Event("auth:session-expired")) })
  expect(screen.queryByTitle("Preview of Poll")).toBeNull()
})
