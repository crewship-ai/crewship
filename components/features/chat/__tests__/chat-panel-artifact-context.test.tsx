import type { ReactNode } from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"

// Mounting of the artifact-context surfaces in the real ChatPanel. Endpoints are
// the proposed contract and are not served by any backend, so fetch is faked.
vi.mock("../virtual-conversation", () => ({ virtualChatEnabled: () => false, VirtualConversation: ({ footer }: { footer?: ReactNode }) => <>{footer}</> }))
const chat = vi.hoisted(() => ({
  turns: [{ id: "saved-turn", role: "assistant", parts: [], timestamp: new Date() }],
  sendMessage: vi.fn(), stopGeneration: vi.fn(), regenerateLastTurn: vi.fn(), editAndResend: vi.fn(),
  loadHistory: vi.fn(), markHistoryUnavailable: vi.fn(), resubscribeSession: vi.fn(),
  isStreaming: false, connectionStatus: "connected",
}))
const toast = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn(), info: vi.fn() }))
vi.mock("@/hooks/use-chat", () => ({ useChat: () => chat }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "user-1" } } }), useSession: () => ({ data: { user: { id: "user-1" } } }) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test" }) }))
vi.mock("../right-rail", () => ({ RightRail: () => null }))
vi.mock("../right-drawer", () => ({ RightDrawer: ({ children }: { children: ReactNode }) => children }))
vi.mock("../right-panel", () => ({ RightPanel: () => null }))
vi.mock("../artifact/artifact-pane", () => ({ ArtifactPane: () => null }))
vi.mock("../composer/slash-palette", () => ({ SlashPalette: () => null }))
vi.mock("../composer/chat-composer", () => ({ ChatComposer: ({ sendMessage }: { sendMessage: (text: string) => unknown }) => <button type="button" onClick={() => sendMessage("Please update it")}>Send message</button> }))
vi.mock("../turn-renderer", () => ({ TurnRenderer: () => null }))
vi.mock("sonner", () => ({ toast }))

import { ChatPanel } from "../chat-panel"

const doc = (text: string) => ({ type: "doc", content: [{ type: "paragraph", attrs: { id: "p1" }, content: [{ type: "text", text }] }] })
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "content-type": "application/json" } })
const calls: string[] = []
type Route = (path: string) => Response | undefined
let overrides: Route[] = []

const SERVED: Route = path => {
  if (path.includes("/work-cards/")) return json({})
  if (path.includes("/work-cards")) return json({ cards: [{ id: "work-1", run_state: "applied", saved: { revision: 2, checkpoint_id: "cp-2" }, freshness: "current", problem: null, page_id: "page-a", access: { title: "Quarterly plan", type: "Document" }, executor: "Riley", on_behalf_of_user_id: "user-1", can: { open: true, retry: true, cancel: true, compare: true } }] })
  if (path.includes("/links")) return json({ links: [
    { link_id: "l1", kind: "artifact", page_id: "page-a", revision: 2, checkpoint_id: "cp-2", name: "Quarterly plan", accessible: true },
    { link_id: "l2", kind: "artifact", page_id: "page-a", name: "Quarterly plan", accessible: true },
  ] })
  if (path.includes("/library")) return json({ items: [{ link_id: "l1", kind: "artifact", page_id: "page-a", name: "Quarterly plan", revision: 2, checkpoint_id: "cp-2" }, { link_id: "l9", restricted: true }] })
  if (path.includes("/artifact-context/preview")) return json({ create: { audience: { kind: "personal" }, result_type: "Document" }, update: { audience: { kind: "folder", name: "Finance", people: 4 }, result_type: "Document", expected: { schema: 2, rev: 5, generation: 1 } } })
  if (path.startsWith("/api/v1/pages/%40page-a/document/checkpoints/cp-2")) return json({ page_id: "page-a", id: "cp-2", rev: 2, schema: 2, content: doc("Body at revision two") })
  if (path.startsWith("/api/v1/pages/%40page-a/document?")) return json({ page_id: "page-a", schema: 2, rev: 5, content: doc("Body at latest") })
  if (path.startsWith("/api/v1/pages/%40page-a?")) return json({ id: "page-a", name: "Quarterly plan", has_document: true })
  return undefined
}

beforeEach(() => {
  calls.length = 0
  overrides = []
  chat.sendMessage.mockReset()
  Object.values(toast).forEach(fn => fn.mockReset())
  global.fetch = vi.fn((input: RequestInfo | URL) => {
    const path = String(input)
    calls.push(path)
    for (const route of [...overrides, SERVED]) { const hit = route(path); if (hit) return Promise.resolve(hit) }
    const value = path.includes("/documents/status") ? { enabled: true }
      : path.includes("/document-results") ? { results: [] }
      : path.includes("/execution-profile") ? { mode: "trusted" }
      : path.includes("/restricted-files") ? { files: [] }
      : path.includes("/messages") ? { messages: [] }
      : path.includes("/participants") ? { participants: [] } : {}
    return Promise.resolve(json(value))
  }) as typeof fetch
})
afterEach(() => { cleanup(); vi.unstubAllEnvs() })

/** Entry animations are time-driven; compare structure, not the frame. */
const settled = (container: HTMLElement, html = container.innerHTML) => html.replace(/ style="[^"]*"/g, "")
const mount = () => render(<ChatPanel agentId="agent-1" agentSlug="riley" agentName="Riley" sessionId="sess-1" askForms={null} />)
const surfaceCalls = () => calls.filter(path => /work-cards|\/links|\/library|artifact-context/.test(path))

it("flag off: no artifact surface and no request, whatever the endpoints serve", async () => {
  const off = mount()
  await screen.findByRole("button", { name: "Send message" })
  await waitFor(() => expect(calls.some(path => path.includes("/execution-profile"))).toBe(true))
  await new Promise(resolve => setTimeout(resolve, 50))
  expect(surfaceCalls()).toEqual([])
  expect(screen.queryByRole("button", { name: "Library" })).toBeNull()
  expect(screen.queryByRole("article")).toBeNull()
  expect(screen.queryByRole("region", { name: "Artifact context" })).toBeNull()
  const served = off.container.innerHTML
  off.unmount()
  overrides = [path => /work-cards|\/links|\/library|artifact-context/.test(path) ? new Response("404 page not found", { status: 404 }) : undefined]
  const missing = mount()
  await screen.findByRole("button", { name: "Send message" })
  await new Promise(resolve => setTimeout(resolve, 50))
  expect(settled(missing.container)).toBe(settled(off.container, served))
})

it("flag off: a send carries no artifact submission", async () => {
  mount()
  fireEvent.click(await screen.findByRole("button", { name: "Send message" }))
  expect(chat.sendMessage).toHaveBeenCalledWith("Please update it")
})

it("flag on: result card opens the exact revision through the checkpoint reader and the send carries the target", async () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "true")
  mount()
  const card = await screen.findByRole("article", { name: "Quarterly plan" })
  expect(card).toHaveTextContent("Saved revision 2")
  expect(card).not.toHaveTextContent("Compare")
  fireEvent.click(screen.getByRole("button", { name: "Open" }))
  // The read-only body rebuilds its DOM on re-render, so assert by polling rather than on a found node.
  await waitFor(() => expect(screen.getByText("Body at revision two")).toBeInTheDocument())
  expect(screen.getByTestId("viewer-target-label")).toHaveTextContent("Revision 2")
  expect(calls.some(path => path.includes("/document/checkpoints/cp-2"))).toBe(true)
  expect(calls.some(path => path.includes("revision="))).toBe(false)
  // The pinned view never falls back to the latest body.
  expect(screen.queryByText("Body at latest")).toBeNull()

  fireEvent.click(screen.getByRole("button", { name: "View latest" }))
  await waitFor(() => expect(screen.getByText("Body at latest")).toBeInTheDocument())

  fireEvent.click(screen.getByRole("button", { name: "Use in message" }))
  const strip = await screen.findByRole("region", { name: "Artifact context" })
  // Nothing runs on the artifact without an explicit choice.
  fireEvent.click(screen.getByRole("button", { name: "Send message" }))
  expect(chat.sendMessage).not.toHaveBeenCalled()
  expect(toast.error).toHaveBeenCalledWith("Choose what the agent should do with the attached item.")
  fireEvent.click(screen.getByLabelText("Update attached"))
  await waitFor(() => expect(strip).toHaveTextContent("Saved to Finance. 4 people can see it."))
  fireEvent.click(screen.getByRole("button", { name: "Send message" }))
  expect(chat.sendMessage).toHaveBeenCalledTimes(1)
  const [text, metadata] = chat.sendMessage.mock.calls[0]
  expect(text).toBe("Please update it")
  expect(metadata.artifact_submission).toMatchObject({ edit_mode: "update", update_page_id: "page-a", context: [{ page_id: "page-a", expected: { schema: 2, rev: 5, generation: 1 } }] })
  expect(typeof metadata.artifact_submission.submission_id).toBe("string")
  // Sent: the context is consumed, the next message is a new intent.
  await waitFor(() => expect(screen.queryByRole("region", { name: "Artifact context" })).toBeNull())
})

it("flag on: a missing revision is unavailable, never the latest", async () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "true")
  overrides = [path => path.includes("/document/checkpoints/") ? json({ error: "not found" }, 404) : undefined]
  mount()
  fireEvent.click(await screen.findByRole("button", { name: "Open" }))
  expect(await screen.findByText("This revision is not available.")).toBeInTheDocument()
  expect(screen.queryByText("Body at latest")).toBeNull()
})

it("flag on: endpoints that are not served show the unavailable state and nothing crashes", async () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "true")
  overrides = [path => /work-cards|\/links|\/library|artifact-context/.test(path) ? new Response("404 page not found", { status: 404 }) : undefined]
  mount()
  fireEvent.click(await screen.findByRole("button", { name: "Library" }))
  expect(await screen.findByText(/The library could not be refreshed/)).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument()
  expect(screen.queryByRole("article")).toBeNull()
  overrides = [path => /work-cards|\/links|\/library|artifact-context/.test(path) ? new Response("", { status: 501 }) : undefined]
  fireEvent.click(screen.getByRole("button", { name: "Try again" }))
  expect(await screen.findByText(/The library could not be refreshed/)).toBeInTheDocument()
})

it("flag on: the library lists linked items and a restricted link without name", async () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "true")
  mount()
  fireEvent.click(await screen.findByRole("button", { name: "Library" }))
  expect(await screen.findByRole("button", { name: "Quarterly plan" })).toBeInTheDocument()
  expect(screen.getByText("Restricted item")).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "All (2)" })).toBeInTheDocument()
})

it("flag on: an item the server answers 404 for is dropped from the composer", async () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "true")
  overrides = [path => path.includes("/artifact-context/preview") ? json({ error: "not found" }, 404) : undefined]
  mount()
  fireEvent.click(await screen.findByRole("button", { name: "Open" }))
  fireEvent.click(await screen.findByRole("button", { name: "Use in message" }))
  await waitFor(() => expect(screen.queryByRole("region", { name: "Artifact context" })).toBeNull())
  expect(screen.queryByText("Quarterly plan", { selector: "li *" })).toBeNull()
})
