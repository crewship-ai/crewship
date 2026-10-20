import type { ReactNode } from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"

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
const chatOptions = vi.hoisted(() => ({ current: undefined as undefined | { onOwnMessageSaved?: (content: string, metadata?: Record<string, unknown>) => void } }))
vi.mock("@/hooks/use-chat", () => ({ useChat: (options: typeof chatOptions.current) => { chatOptions.current = options; return chat } }))
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
// The server visibility field does not exist yet, so the real seam is always false. These tests stand in for the day it lands.
const seam = vi.hoisted(() => ({ confirmed: true }))
vi.mock("../artifact-context/use-server-chat-visibility", () => ({ useServerConfirmedPrivate: () => seam.confirmed }))

import { ChatPanel } from "../chat-panel"

const doc = (text: string) => ({ type: "doc", content: [{ type: "paragraph", attrs: { id: "p1" }, content: [{ type: "text", text }] }] })
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "content-type": "application/json" } })
const calls: string[] = []
type Route = (path: string) => Response | "reject" | "hang" | undefined
let overrides: Route[] = []

const SERVED: Route = path => {
  if (path.includes("/work-cards/")) return json({})
  if (path.includes("/work-cards")) return json({ cards: [{ id: "work-1", run_state: "applied", saved: { revision: 2, checkpoint_id: "cp-2" }, freshness: "current", problem: null, page_id: "page-a", access: { title: "Quarterly plan", type: "Document" }, executor: "Riley", on_behalf_of_user_id: "user-1", can: { open: true, retry: true, cancel: true, compare: true } }] })
  if (path.includes("/links")) return json({ links: [
    { link_id: "l1", kind: "artifact", page_id: "page-a", revision: 2, checkpoint_id: "cp-2", name: "Quarterly plan", accessible: true },
    { link_id: "l2", kind: "artifact", page_id: "page-a", name: "Quarterly plan", accessible: true },
  ] })
  if (path.includes("/library")) return json({ items: [{ link_id: "l1", kind: "artifact", page_id: "page-a", name: "Quarterly plan", revision: 2, checkpoint_id: "cp-2" }, { link_id: "l9", restricted: true }] })
  if (path.includes("/artifact-context/preview")) return json({ create: { audience: { kind: "personal" }, result_type: "Document" }, update: { audience: { kind: "personal" }, result_type: "Document", expected: { schema: 3, rev: 5, generation: 1 } } })
  if (path.startsWith("/api/v1/pages/%40page-a/document/checkpoints/cp-2")) return json({ page_id: "page-a", id: "cp-2", rev: 2, schema: 2, content: doc("Body at revision two") })
  if (path.startsWith("/api/v1/pages/%40page-a/document?")) return json({ page_id: "page-a", schema: 2, rev: 5, content: doc("Body at latest") })
  if (path.startsWith("/api/v1/pages/%40page-a?")) return json({ id: "page-a", name: "Quarterly plan", has_document: true })
  return undefined
}

beforeEach(() => {
  calls.length = 0
  seam.confirmed = true
  overrides = []
  chat.sendMessage.mockReset()
  Object.values(toast).forEach(fn => fn.mockReset())
  global.fetch = vi.fn((input: RequestInfo | URL) => {
    const path = String(input)
    calls.push(path)
    for (const route of [...overrides, SERVED]) {
      const hit = route(path)
      if (hit === "reject") return Promise.reject(new TypeError("network"))
      if (hit === "hang") return new Promise<Response>(() => {})
      if (hit) return Promise.resolve(hit)
    }
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
const panel = (sessionId = "sess-1", kind?: "direct" | "issue") => <ChatPanel agentId="agent-1" agentSlug="riley" agentName="Riley" sessionId={sessionId} askForms={null} sessionKind={kind} />
const mount = (kind?: "direct" | "issue") => render(panel("sess-1", kind))
const echo = (content: string) => act(() => chatOptions.current?.onOwnMessageSaved?.(content, { author_user_id: "user-1" }))
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

it("flag off: a send carries no document iteration intent", async () => {
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
  expect(calls.some(path => path.includes("/document/checkpoints/cp-2?workspace_id=ws-test"))).toBe(true)
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
  await waitFor(() => expect(strip).toHaveTextContent("Saved to My space. Workspace owners and admins can still read it."))
  fireEvent.click(screen.getByRole("button", { name: "Send message" }))
  expect(chat.sendMessage).toHaveBeenCalledTimes(1)
  const [text, metadata] = chat.sendMessage.mock.calls[0]
  expect(text).toBe("Please update it")
  expect(metadata.document_iteration_intent).toEqual({ submission_id: expect.any(String), page_id: "page-a", schema: 3, rev: 5, generation: 1 })
  expect(typeof metadata.document_iteration_intent.submission_id).toBe("string")
  // Handing the frame to the socket is not confirmation: the intent stays until the server echoes the message.
  expect(screen.getByRole("region", { name: "Artifact context" })).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Send message" }))
  expect(chat.sendMessage.mock.calls[1][1].document_iteration_intent.submission_id).toBe(metadata.document_iteration_intent.submission_id)
  echo("Please update it")
  await waitFor(() => expect(screen.queryByRole("region", { name: "Artifact context" })).toBeNull())
  // A new intent after the confirmed send.
  expect(chat.sendMessage).toHaveBeenCalledTimes(2)
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

it("flag on: a suggestion chip is not the composer's send; it spends no intent and keeps the draft", async () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "true")
  mount()
  fireEvent.click(await screen.findByRole("button", { name: "Open" }))
  fireEvent.click(await screen.findByRole("button", { name: "Use in message" }))
  const strip = await screen.findByRole("region", { name: "Artifact context" })
  fireEvent.click(screen.getByLabelText("Update attached"))
  await waitFor(() => expect(strip).toHaveTextContent("Saved to My space"))
  fireEvent.click(await screen.findByRole("button", { name: "Tell me more" }))
  await waitFor(() => expect(chat.sendMessage).toHaveBeenCalled())
  expect(chat.sendMessage.mock.calls[0]).toEqual(["Tell me more"])
  expect(screen.getByRole("region", { name: "Artifact context" })).toHaveTextContent("Saved to My space")
  // The composer's own send still carries it, once.
  fireEvent.click(screen.getByRole("button", { name: "Send message" }))
  expect(chat.sendMessage.mock.calls[1][1].document_iteration_intent.page_id).toBe("page-a")
})

it("flag on: a denied or missing pinned revision shows neither the title nor the saved revision, and cannot be attached", async () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "true")
  overrides = [path => path.includes("/document/checkpoints/") ? json({ error: "forbidden" }, 403) : undefined]
  mount()
  fireEvent.click(await screen.findByRole("button", { name: "Open" }))
  const viewer = await screen.findByRole("region", { name: "Document preview" })
  await waitFor(() => expect(viewer).toHaveTextContent("Access to this revision is unavailable."))
  expect(viewer).not.toHaveTextContent("Quarterly plan")
  expect(viewer).not.toHaveTextContent("Saved revision")
  expect(screen.queryByRole("button", { name: "Use in message" })).toBeNull()
})

it("flag on: an item in a shared folder cannot be edited and nothing is sent", async () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "true")
  overrides = [path => path.includes("/artifact-context/preview") ? json({ create: { audience: { kind: "personal" }, result_type: "Document" }, update: { audience: { kind: "folder", name: "Finance", people: 4 }, result_type: "Document", expected: { schema: 3, rev: 5, generation: 1 } } }) : undefined]
  mount()
  fireEvent.click(await screen.findByRole("button", { name: "Open" }))
  fireEvent.click(await screen.findByRole("button", { name: "Use in message" }))
  const strip = await screen.findByRole("region", { name: "Artifact context" })
  await waitFor(() => expect(screen.getByRole("radio", { name: "Update attached" })).toBeDisabled())
  expect(strip).toHaveTextContent("needs a proposal, which is not available yet")
  fireEvent.click(screen.getByLabelText("Reply only"))
  fireEvent.click(screen.getByRole("button", { name: "Send message" }))
  // Reply-only is a plain message: no mandate.
  expect(chat.sendMessage.mock.calls[0]).toEqual(["Please update it"])
})

it("flag on: a reply-only send resets the context too, once the message is echoed", async () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "true")
  mount()
  fireEvent.click(await screen.findByRole("button", { name: "Open" }))
  fireEvent.click(await screen.findByRole("button", { name: "Use in message" }))
  await screen.findByRole("region", { name: "Artifact context" })
  fireEvent.click(screen.getByLabelText("Reply only"))
  fireEvent.click(screen.getByRole("button", { name: "Send message" }))
  expect(chat.sendMessage.mock.calls[0]).toEqual(["Please update it"])
  expect(screen.getByRole("region", { name: "Artifact context" })).toBeInTheDocument()
  echo("an unrelated echo")
  expect(screen.getByRole("region", { name: "Artifact context" })).toBeInTheDocument()
  echo("Please update it")
  await waitFor(() => expect(screen.queryByRole("region", { name: "Artifact context" })).toBeNull())
})

it("flag on: editing is not offered in a conversation that is not a private chat", async () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "true")
  mount("issue")
  fireEvent.click(await screen.findByRole("button", { name: "Open" }))
  fireEvent.click(await screen.findByRole("button", { name: "Use in message" }))
  const strip = await screen.findByRole("region", { name: "Artifact context" })
  expect(screen.getByRole("radio", { name: "Update attached" })).toBeDisabled()
  expect(strip).toHaveTextContent("needs a private chat")
})

async function attachFirstItem() {
  fireEvent.click(await screen.findByRole("button", { name: "Open" }))
  fireEvent.click(await screen.findByRole("button", { name: "Use in message" }))
  return screen.findByRole("region", { name: "Artifact context" })
}
const participants = (user_ids: string[]) => (path: string) => path.includes("/participants") ? json({ participants: user_ids.map(user_id => ({ user_id })) }) : undefined

it.each([
  ["more than one human participant", [participants(["user-1", "user-2"])], "needs a private chat"],
  ["a failed participants read", [(path: string) => path.includes("/participants") ? "reject" as const : undefined], "Could not verify this chat is private"],
  ["a non-ok participants read", [(path: string) => path.includes("/participants") ? json({ error: "boom" }, 500) : undefined], "Could not verify this chat is private"],
  ["a 200 that is not a participants list", [(path: string) => path.includes("/participants") ? json({}) : undefined], "Could not verify this chat is private"],
  ["a 200 whose participants field is odd", [(path: string) => path.includes("/participants") ? json({ participants: "nobody" }) : undefined], "Could not verify this chat is private"],
  ["a participants row without a user id", [(path: string) => path.includes("/participants") ? json({ participants: [{ email: "x@y" }] }) : undefined], "Could not verify this chat is private"],
  ["a participants read that has not answered", [(path: string) => path.includes("/participants") ? "hang" as const : undefined], "Checking chat visibility…"],
])("flag on: Update is not offered with %s", async (_name, routes, reason) => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "true")
  overrides = routes as Route[]
  mount()
  const strip = await attachFirstItem()
  await waitFor(() => expect(strip).toHaveTextContent(reason))
  expect(screen.getByRole("radio", { name: "Update attached" })).toBeDisabled()
})

it("flag on: a restricted group profile never offers Update", async () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "true")
  overrides = [path => path.includes("/execution-profile") ? json({ mode: "restricted", audience: "group" }) : undefined]
  mount()
  await new Promise(resolve => setTimeout(resolve, 100))
  // Protected reads are not verified for a restricted chat, so no artifact surface mounts at all.
  expect(screen.queryByRole("region", { name: "Artifact context" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Library" })).toBeNull()
})

it("flag on: switching session forgets the earlier chat's participants and checks again", async () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "true")
  const view = mount()
  const first = await attachFirstItem()
  await waitFor(() => expect(screen.getByRole("radio", { name: "Update attached" })).toBeEnabled())
  expect(first).toBeInTheDocument()
  overrides = [path => path.includes("/chats/sess-2/participants") ? "hang" : undefined]
  view.rerender(panel("sess-2"))
  await waitFor(() => expect(screen.queryByRole("region", { name: "Artifact context" })).toBeNull())
  const second = await attachFirstItem()
  await waitFor(() => expect(second).toHaveTextContent("Checking chat visibility…"))
  expect(screen.getByRole("radio", { name: "Update attached" })).toBeDisabled()
})

it("flag on, as shipped today (no server visibility signal): Update is disabled with the honest reason and nothing is sent as an edit", async () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "true")
  seam.confirmed = false
  mount()
  const strip = await attachFirstItem()
  await waitFor(() => expect(strip).toHaveTextContent("Updating a document from chat is not available yet: the server does not confirm this chat's audience."))
  expect(screen.getByRole("radio", { name: "Update attached" })).toBeDisabled()
  fireEvent.click(screen.getByLabelText("Reply only"))
  fireEvent.click(screen.getByRole("button", { name: "Send message" }))
  expect(chat.sendMessage.mock.calls[0]).toEqual(["Please update it"])
})
