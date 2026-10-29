import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { ArtifactComment, ArtifactProjectSnapshot } from "../../projects/types"
import type { ArtifactViewerTarget } from "../../projects/artifact-viewer-context"

// The library detail (variant "library"): Latest / Shared in the header, the
// comment queue beside Preview, Send to agent, and the error states of #15.
const state = vi.hoisted(() => ({
  target: null as ArtifactViewerTarget | null,
  close: vi.fn(),
  snapshot: { data: undefined as unknown, isLoading: false, error: null as unknown, refetch: vi.fn() },
  versionArg: [] as (number | undefined)[],
  restore: vi.fn(), share: vi.fn(), comment: vi.fn(), dispatch: vi.fn(),
}))

vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "user-me" } } }) }))
vi.mock("@/hooks/use-artifact-agent-dispatch", () => ({ useArtifactAgentDispatch: () => ({ dispatch: state.dispatch, ready: true }) }))
vi.mock("../../projects/artifact-viewer-context", () => ({
  useArtifactViewer: () => ({ workspaceId: "ws1", target: state.target, open: vi.fn(), close: state.close }),
}))
vi.mock("../../projects/use-artifact-project-data", () => ({
  useArtifactProjectSnapshot: (_ws: string, _slug: string | null, version?: number) => { state.versionArg.push(version); return state.snapshot },
  useArtifactProjectMutations: () => ({
    restore: { mutate: state.restore, isPending: false },
    share: { mutate: state.share, isPending: false },
    comment: { mutate: state.comment, isPending: false },
  }),
}))

import { ArtifactViewerPanel } from "../artifact-viewer-panel"

const page = (name: string) => ({ path: name, content: `<body><h1 id="hero">Big idea</h1>${name}</body>`, media_type: "text/html" })
function snap(patch: Partial<ArtifactProjectSnapshot> = {}): ArtifactProjectSnapshot {
  return {
    page_id: "p1", name: "Launch site", slug: "launch", can_write: true, can_share:true, can_restore:true, can_send_to_agent:true, latest_version: 5, shared_version: 4,
    origin: { chat_id: "chat-1", agent_id: "agent-1" },
    manifest: { version: 5, entrypoint: "home.html", files: [page("home.html")] },
    versions: [{ version: 5, message: "Five" }, { version: 4, message: "Four" }, { version: 3, message: "Three" }],
    comments: [],
    ...patch,
  }
}
const comment = (patch: Partial<ArtifactComment>): ArtifactComment => ({ id: "c", version: 5, path: "home.html", selector: "#hero", quote: "Big idea", text: "t", ...patch })
const waiting = (id: string, text: string) => comment({ id, text, task_id: `task-${id}`, status: "pending", author: "user-me", delivery_retryable: true })
const err = (status: number, code: string) => Object.assign(new Error(code), { status, code })

beforeEach(() => {
  state.target = { slug: "launch", tab: "preview" }
  state.snapshot = { data: snap(), isLoading: false, error: null, refetch: vi.fn(async () => ({ data: state.snapshot.data, error: null })) }
  state.versionArg = []
  for (const fn of [state.restore, state.share, state.comment, state.close]) fn.mockReset()
  state.dispatch.mockReset().mockResolvedValue(true)
})
afterEach(cleanup)

const renderLibrary = () => render(<ArtifactViewerPanel variant="library" />)
const header = () => within(screen.getByRole("region", { name: "Artifact viewer" }).querySelector("header")!)
function selectHero() {
  fireEvent.click(screen.getByRole("button", { name: "Select" }))
  const frame = screen.getByTitle(/Preview of/) as HTMLIFrameElement
  act(() => { window.dispatchEvent(new MessageEvent("message", { source: frame.contentWindow, data: { type: "crewship-artifact-select", selector: "#hero", quote: "Big idea" } })) })
}

it("puts Latest and Shared with their version numbers in the header", () => {
  renderLibrary()
  const versions = header().getByRole("radiogroup", { name: "Version" })
  expect(within(versions).getAllByRole("radio").map(radio => radio.textContent)).toEqual(["Latest v5", "Shared v4"])
  expect(within(versions).getByRole("radio", { name: "Latest v5" })).toHaveAttribute("aria-checked", "true")
  fireEvent.click(within(versions).getByRole("radio", { name: "Shared v4" }))
  expect(state.versionArg.at(-1)).toBe(4)
})

it("keeps Shared in the switch but unavailable when nothing is shared", () => {
  state.snapshot.data = snap({ shared_version: 0 })
  renderLibrary()
  expect(header().getByRole("radio", { name: "Shared —" })).toBeDisabled()
})

it("counts versions and waiting comments on the tabs without changing their names", () => {
  state.snapshot.data = snap({ comments: [waiting("a", "One"), comment({ id: "b", status: "working", text: "Two" })] })
  renderLibrary()
  expect(screen.getByRole("tab", { name: "History" })).toHaveTextContent("History3")
  expect(screen.getByRole("tab", { name: "Comments" })).toHaveTextContent("Comments1")
  expect(screen.getByText(/restore makes a new version, nothing is deleted/)).toBeInTheDocument()
})

it("shows the queue beside Preview with Waiting, Agent working and Done · vN", () => {
  state.snapshot.data = snap({ comments: [
    waiting("a", "Shorter title"),
    comment({ id: "b", text: "Bigger button", task_id: "t-b", status: "working" }),
    comment({ id: "c", text: "Fix FAQ", task_id: "t-c", status: "answered", answered_version: 5 }),
  ] })
  renderLibrary()
  const rail = screen.getByRole("complementary", { name: "Comments beside preview" })
  expect(within(rail).getByText("Waiting")).toBeInTheDocument()
  expect(within(rail).getByText("Agent working")).toBeInTheDocument()
  expect(within(rail).getByText("Done · v5")).toBeInTheDocument()
  // The same queue is the Comments tab, where the rail is gone (narrow widths hide the rail).
  fireEvent.click(screen.getByRole("tab", { name: "Comments" }))
  expect(screen.queryByRole("complementary", { name: "Comments beside preview" })).toBeNull()
  expect(within(screen.getByRole("region", { name: "Comment queue" })).getByText("Done · v5")).toBeInTheDocument()
})

it("adds a comment to the queue without starting the agent", () => {
  renderLibrary()
  selectHero()
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "Shorter" } })
  fireEvent.click(screen.getByRole("button", { name: "Add to queue" }))
  expect(state.comment).toHaveBeenCalledWith({ version: 5, path: "home.html", selector: "#hero", quote: "Big idea", text: "Shorter", send_to_agent: true }, expect.anything())
  act(() => state.comment.mock.calls[0][1].onSuccess(waiting("n", "Shorter")))
  expect(state.dispatch).not.toHaveBeenCalled()
  expect(screen.getByText("Added to the queue. The agent starts when you send it.")).toBeInTheDocument()
})

it("sends every waiting comment with Send to agent and never calls a queued one a failure", async () => {
  const one = waiting("a", "Shorter title"), two = waiting("b", "Bigger button")
  state.snapshot.data = snap({ comments: [one, two] })
  renderLibrary()
  expect(screen.queryByText("The agent did not start")).toBeNull()
  const rail = within(screen.getByRole("complementary", { name: "Comments beside preview" }))
  expect(rail.getByText("A comment starts the agent only when you send it.")).toBeInTheDocument()
  fireEvent.click(rail.getByRole("button", { name: "Send 2 to agent" }))
  await waitFor(() => expect(state.dispatch).toHaveBeenCalledTimes(2))
  expect(state.dispatch).toHaveBeenNthCalledWith(1, one)
  expect(state.dispatch).toHaveBeenNthCalledWith(2, two)
  expect(state.snapshot.refetch).toHaveBeenCalledTimes(1)
  expect(await screen.findByText("Sent 2 comments to the agent")).toBeInTheDocument()
})

it("disables Send to agent when nothing waits", () => {
  state.snapshot.data = snap({ comments: [comment({ id: "b", task_id: "t-b", status: "working" })] })
  renderLibrary()
  expect(within(screen.getByRole("complementary", { name: "Comments beside preview" })).getByRole("button", { name: "Send 0 to agent" })).toBeDisabled()
})

// ---- error states (#15) ----

it("403 offers Request access without showing the artifact", async () => {
  const writeText = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true })
  state.snapshot = { data: snap(), isLoading: false, error: err(403, "forbidden"), refetch: vi.fn() }
  renderLibrary()
  expect(screen.getByRole("heading", { name: "You can't open this artifact" })).toBeInTheDocument()
  expect(screen.queryByText("Launch site")).toBeNull()
  expect(screen.queryByTitle(/Preview of/)).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Request access" }))
  await waitFor(() => expect(writeText).toHaveBeenCalledWith(expect.stringContaining("launch")))
  expect(await screen.findByText(/Access request copied/)).toBeInTheDocument()
})

it("404 does not say whether the artifact exists", () => {
  state.snapshot = { data: undefined, isLoading: false, error: err(404, "not_found"), refetch: vi.fn() }
  renderLibrary()
  expect(screen.getByRole("heading", { name: "This artifact isn't available" })).toBeInTheDocument()
  expect(screen.getByText(/We don't say which, so private work stays private\./)).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Request access" })).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "All artifacts" }))
  expect(state.close).toHaveBeenCalled()
})

it("409 Reload and reapply puts the kept comment on the newest version", async () => {
  renderLibrary()
  selectHero()
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "Keep me" } })
  fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
  act(() => state.comment.mock.calls[0][1].onError(err(409, "artifact_version_conflict")))
  expect(screen.getByText(/Your comment and selection are kept/)).toBeInTheDocument()
  const fresh = snap({ latest_version: 6, manifest: { ...snap().manifest, version: 6 } })
  state.snapshot.refetch = vi.fn(async () => { state.snapshot = { ...state.snapshot, data: fresh }; return { data: fresh, error: null } })
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Reload and reapply" })) })
  expect(state.snapshot.refetch).toHaveBeenCalled()
  expect(screen.queryByText(/Your comment and selection are kept/)).toBeNull()
  expect(screen.getByRole("textbox", { name: "Comment" })).toHaveValue("Keep me")
  expect(screen.getByText(/On #hero/)).toHaveTextContent("· v6")
})

it("409 asks for a new selection when the element is gone from the newest version", async () => {
  renderLibrary()
  selectHero()
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "Keep me" } })
  fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
  act(() => state.comment.mock.calls[0][1].onError(err(409, "artifact_version_conflict")))
  const fresh = snap({ latest_version: 6, manifest: { version: 6, entrypoint: "home.html", files: [{ path: "home.html", content: "<body><p id=other>x</p></body>", media_type: "text/html" }] } })
  state.snapshot.refetch = vi.fn(async () => { state.snapshot = { ...state.snapshot, data: fresh }; return { data: fresh, error: null } })
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Reload and reapply" })) })
  expect(screen.getByText("This element changed. Select it again.")).toBeInTheDocument()
  expect(screen.getByRole("textbox", { name: "Comment" })).toHaveValue("Keep me")
})

it("a full history offers choices and keeps the comment", async () => {
  const writeText = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true })
  renderLibrary()
  selectHero()
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "Keep me" } })
  fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
  act(() => state.comment.mock.calls[0][1].onError(err(413, "artifact_history_quota")))
  expect(screen.getByText(/Saving is paused/)).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Copy my comment" }))
  await waitFor(() => expect(writeText).toHaveBeenCalledWith("Keep me"))
  fireEvent.click(screen.getByRole("button", { name: "Manage versions" }))
  expect(screen.getByRole("tab", { name: "History" })).toHaveAttribute("aria-selected", "true")
})

it("a history near its limit offers what to do before saving fails", () => {
  state.snapshot.data = snap({ quota: { used_bytes: 9, limit_bytes: 10, warning: true } })
  renderLibrary()
  expect(screen.getByText(/History is never deleted without you/)).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Review versions" }))
  expect(screen.getByRole("tab", { name: "History" })).toHaveAttribute("aria-selected", "true")
})

it.each([false,undefined])("does not send a waiting batch after a fresh read lacks send capability %s",cap=>{
 state.snapshot.data=snap({comments:[waiting("a","First"),waiting("b","Second")]})
 state.snapshot.refetch.mockResolvedValue({data:snap({can_send_to_agent:cap,comments:[waiting("a","First"),waiting("b","Second")]}),error:null})
 renderLibrary()
 const rail=within(screen.getByRole("complementary",{name:"Comments beside preview"}))
 return act(async()=>{
  fireEvent.click(rail.getByRole("button",{name:"Send 2 to agent"}))
  await Promise.resolve()
  expect(state.dispatch).not.toHaveBeenCalled()
  expect(state.comment).not.toHaveBeenCalled()
 })
})
it("stops a waiting batch when send capability leaves and returns during the first dispatch",async()=>{
 let resolve!:(ok:boolean)=>void
 state.dispatch.mockImplementationOnce(()=>new Promise<boolean>(done=>{resolve=done}))
 state.snapshot.data=snap({comments:[waiting("a","First"),waiting("b","Second")]})
 const view=renderLibrary()
 fireEvent.click(within(screen.getByRole("complementary",{name:"Comments beside preview"})).getByRole("button",{name:"Send 2 to agent"}))
 await waitFor(()=>expect(state.dispatch).toHaveBeenCalledTimes(1))
 state.snapshot.data=snap({can_send_to_agent:false,comments:[waiting("a","First"),waiting("b","Second")]});view.rerender(<ArtifactViewerPanel variant="library" />)
 expect(screen.queryByRole("button",{name:"Send 2 to agent"})).toBeNull()
 state.snapshot.data=snap({comments:[waiting("a","First"),waiting("b","Second")]});view.rerender(<ArtifactViewerPanel variant="library" />)
 await act(async()=>resolve(true))
 expect(state.dispatch).toHaveBeenCalledTimes(1)
 expect(state.comment).not.toHaveBeenCalled()
})
it("keeps source editing available to an Edit user without owner queue, sharing or restore controls",()=>{
 state.snapshot.data=snap({can_share:false,can_restore:false,can_send_to_agent:false})
 renderLibrary()
 expect(screen.getByRole("link",{name:"Edit source"})).toHaveAttribute("href","/pages/launch?artifact=1&workspace_id=ws1")
 selectHero();fireEvent.change(screen.getByRole("textbox",{name:"Comment"}),{target:{value:"Local feedback"}})
 expect(screen.queryByRole("button",{name:"Add to queue"})).toBeNull()
 expect(screen.queryByRole("button",{name:"Send to agent"})).toBeNull()
 fireEvent.click(screen.getByRole("button",{name:"Save comment"}))
 expect(state.comment).toHaveBeenCalledWith(expect.objectContaining({send_to_agent:false}),expect.anything())
})
