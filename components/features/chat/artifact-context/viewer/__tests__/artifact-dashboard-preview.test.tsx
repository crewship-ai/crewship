import { act, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { ArtifactDashboardPreview } from "../artifact-dashboard-preview"
const mock = vi.hoisted(() => ({ send: vi.fn() }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "reader" } } }) }))
vi.mock("@/lib/api-fetch", () => ({ AUTH_EVENT: "auth:session-expired", apiFetch: (...args: unknown[]) => mock.send(...args) }))
const props = { workspaceId: "workspace", pageId: "page", codeVersion: 2, files: [
  { path: "dashboard.json", media_type: "application/json", content: '{"schema_version":1,"collections":["sales"]}' },
  { path: "index.html", media_type: "text/html", content: '<h1 id="artifact-title">Revenue</h1><span data-source="sales" data-row="north" data-field="revenue"></span>' },
], path: "index.html", title: "Dashboard", selectMode: true, onSelect: vi.fn(), onNavigate: vi.fn() }
const data = (value = 12, code_version = 2) => ({ collection: "sales", code_version, documents: [{ id: "north", version: 1, value: { revenue: value }, updated_at: "2026-10-28T08:00:00Z", source_label: "Agent", source_kind: "agent" }] })
function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return { client, ...render(<QueryClientProvider client={client}><textarea aria-label="Comment draft" defaultValue="Keep this draft" /><ArtifactDashboardPreview {...props} /></QueryClientProvider>) }
}
beforeEach(() => { vi.stubEnv("NEXT_PUBLIC_ARTIFACT_DASHBOARDS", "true"); mock.send.mockReset().mockImplementation(() => Promise.resolve(new Response(JSON.stringify(data())))); props.onSelect.mockReset() })
afterEach(() => vi.unstubAllEnvs())
it("keeps the same physical opaque frame and comment draft across a data-only update", async () => {
  const view = mount(), frame = await screen.findByTitle("Dashboard") as HTMLIFrameElement, source = frame.srcdoc
  expect(frame.getAttribute("sandbox")).toBe("allow-scripts"); expect(frame.hasAttribute("credentialless")).toBe(true)
  const post = vi.spyOn(frame.contentWindow!, "postMessage")
  mock.send.mockImplementation(() => Promise.resolve(new Response(JSON.stringify(data(24)))))
  await act(async () => { await view.client.invalidateQueries() })
  await waitFor(() => expect(post).toHaveBeenCalled())
  expect(screen.getByTitle("Dashboard")).toBe(frame); expect(frame.srcdoc).toBe(source)
  expect(screen.getByLabelText("Comment draft")).toHaveValue("Keep this draft")
  expect(post.mock.calls.at(-1)?.[0].collections[0].documents[0].value.revenue).toBe(24)
})
it("accepts only this opaque frame with its current document nonce", async () => {
  mount(); const frame = await screen.findByTitle("Dashboard") as HTMLIFrameElement
  const token = JSON.parse(frame.srcdoc.match(/const config=(.*?);const quote=/)![1]).token
  const send = (source: MessageEventSource | null, origin: string, nonce: string) => act(() => window.dispatchEvent(new MessageEvent("message", { source, origin, data: { token: nonce, selector: "#artifact-title", quote: "Revenue" } })))
  send(window, "null", token); send(frame.contentWindow, "https://evil.test", token); send(frame.contentWindow, "null", "old-nonce")
  expect(props.onSelect).not.toHaveBeenCalled()
  send(frame.contentWindow, "null", token); expect(props.onSelect).toHaveBeenCalledExactlyOnceWith({ selector: "#artifact-title", quote: "Revenue" })
})
it("unmounts private rendered data after the reader pin stops authorizing it, retaining the draft", async () => {
  const view = mount(); await screen.findByTitle("Dashboard")
  mock.send.mockImplementation(() => Promise.resolve(new Response('{}', { status: 409 })))
  await act(async () => { await view.client.invalidateQueries() })
  await waitFor(() => expect(screen.queryByTitle("Dashboard")).toBeNull())
  expect(screen.getByLabelText("Comment draft")).toHaveValue("Keep this draft")
})
it("posts current data on load without accepting any data request from the frame", async () => {
  mount(); const frame = await screen.findByTitle("Dashboard") as HTMLIFrameElement
  const post = vi.spyOn(frame.contentWindow!, "postMessage")
  fireEvent.load(frame); expect(post).toHaveBeenCalled()
  const calls = mock.send.mock.calls.length
  act(() => window.dispatchEvent(new MessageEvent("message", { source: frame.contentWindow, origin: "null", data: { type: "fetch", url: "/private" } })))
  expect(mock.send).toHaveBeenCalledTimes(calls)
})

it("rejects two individually valid collections above the aggregate wire budget without rendering a partial frame", async () => {
  const payload = "x".repeat(8 * 1024 * 1024 - 20)
  mock.send.mockImplementation(url => {
    const collection = String(url).includes("/returns?") ? "returns" : "sales"
    return Promise.resolve(new Response(JSON.stringify({ ...data(), collection, documents: [{ ...data().documents[0], value: { revenue: payload } }] })))
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  const files = props.files.map(file => file.path === "dashboard.json" ? { ...file, content: '{"schema_version":1,"collections":["sales","returns"]}' } : file)
  render(<QueryClientProvider client={client}><ArtifactDashboardPreview {...props} files={files} /></QueryClientProvider>)
  await screen.findByText("Dashboard exceeds the preview data limit")
  expect(screen.queryByTitle("Dashboard")).toBeNull()
  expect(mock.send).toHaveBeenCalledTimes(2)
})
