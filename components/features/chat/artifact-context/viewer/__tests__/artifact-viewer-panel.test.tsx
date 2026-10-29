import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { ArtifactComment, ArtifactProjectSnapshot } from "../../projects/types"
import type { ArtifactViewerTarget } from "../../projects/artifact-viewer-context"

const state = vi.hoisted(() => ({
  agentHook:vi.fn(),
  userId: "user-me",
  target: null as ArtifactViewerTarget | null,
  close: vi.fn(),
  snapshot: { data: undefined as unknown, isLoading: false, error: null as unknown, refetch: vi.fn() },
  versionArg: [] as (number | undefined)[],
  restore: vi.fn(), share: vi.fn(), comment: vi.fn(),
  sendToAgent: undefined as undefined | ((request: unknown) => Promise<boolean>),
}))

vi.mock("@/hooks/use-artifact-agent-dispatch",()=>({useArtifactAgentDispatch:(...args:unknown[])=>{state.agentHook(...args);return {dispatch:vi.fn(),ready:false}}}))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: state.userId } } }) }))
vi.mock("../../projects/artifact-viewer-context", () => ({
  useArtifactViewer: () => ({ workspaceId: "ws1", target: state.target, open: vi.fn(), close: state.close, sendToAgent: state.sendToAgent }),
}))
vi.mock("../../projects/use-artifact-project-data", () => ({
  useArtifactProjectSnapshot: (_ws: string, _slug: string | null, version?: number) => { state.versionArg.push(version); return state.snapshot },
  useArtifactProjectMutations: () => ({
    restore: { mutate: state.restore, isPending: false },
    share: { mutate: state.share, isPending: false },
    comment: { mutate: state.comment, isPending: false },
  }),
}))

vi.mock("../artifact-dashboard-preview", () => ({ ArtifactDashboardPreview: () => <div>Bound dashboard</div> }))
vi.mock("../artifact-slides-preview", () => ({ ArtifactSlidesPreview: () => <div>Typed slides preview</div> }))

import { ArtifactViewerPanel, commentQueueChip } from "../artifact-viewer-panel"

const html = (name: string) => ({ path: name, content: `<body><h1 id="hero">Big  idea</h1>${name}</body>`, media_type: "text/html" })
function snap(patch: Partial<ArtifactProjectSnapshot> = {}): ArtifactProjectSnapshot {
  return {
    page_id: "p1", name: "Launch site", slug: "launch", can_write: true, can_share: true, can_restore: true, can_send_to_agent: true, latest_version: 3, shared_version: 2,
    manifest: { version: 3, entrypoint: "home.html", files: [html("about.html"), { path: "a.css", content: "", media_type: "text/css" }, html("home.html"), html("contact.html")] },
    versions: [
      { version: 1, message: "First", actor: "Ada", created_at: "2026-10-20T10:00:00Z" },
      { version: 2, message: "Hero", actor: "Agent", created_at: "2026-10-21T10:00:00Z" },
      { version: 3, message: "Restore", actor: "Ada", created_at: "2026-10-22T10:00:00Z", restored_from: 1 },
    ],
    comments: [],
    ...patch,
  }
}
const err = (status: number, code: string) => Object.assign(new Error(code), { status, code })

beforeEach(() => {
  state.userId = "user-me"
  state.target = { slug: "launch" }
  state.snapshot = { data: snap(), isLoading: false, error: null, refetch: vi.fn() }
  state.versionArg = []
  for (const fn of [state.restore, state.share, state.comment, state.close]) fn.mockReset()
  state.sendToAgent = undefined
})
afterEach(cleanup)

function selectHero() {
  fireEvent.click(screen.getByRole("button", { name: "Select" }))
  expect(screen.getByText(/Click a part of the screen/)).toBeInTheDocument()
  const frame = screen.getByTitle(/Preview of/) as HTMLIFrameElement
  act(() => { window.dispatchEvent(new MessageEvent("message", { source: frame.contentWindow, data: { type: "crewship-artifact-select", selector: "#hero", quote: "Big  idea" } })) })
}
const history = () => within(screen.getByRole("region", { name: "Version history" }))

it("renders nothing without a target", () => {
  state.target = null
  const { container } = render(<ArtifactViewerPanel />)
  expect(container).toBeEmptyDOMElement()
})

it("shows header, version switch and the screens with the entrypoint first", () => {
  render(<ArtifactViewerPanel />)
  expect(screen.getByRole("heading", { name: "Launch site" })).toBeInTheDocument()
  expect(screen.getByText("Draft")).toBeInTheDocument()
  expect(within(screen.getByRole("radiogroup", { name: "Version" })).getAllByRole("radio").map(el => el.textContent)).toEqual(["Latest · v3", "Shared · v2"])
  const screens = within(screen.getByRole("radiogroup", { name: "Screen" })).getAllByRole("radio").map(el => el.textContent)
  expect(screens).toEqual(["Home", "About", "Contact"])
  fireEvent.click(within(screen.getByRole("radiogroup", { name: "Screen" })).getByRole("radio", { name: "Contact" }))
  expect((screen.getByTitle(/Preview of/) as HTMLIFrameElement).getAttribute("srcdoc")).toContain("contact.html")
  expect(screen.getByText("A comment starts the agent only when you choose Send to agent.")).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Close viewer" }))
  expect(state.close).toHaveBeenCalled()
})

it("hides the Shared segment when the latest version is shared", () => {
  state.snapshot.data = snap({ shared_version: 3 })
  render(<ArtifactViewerPanel />)
  expect(within(screen.getByRole("radiogroup", { name: "Version" })).getAllByRole("radio")).toHaveLength(1)
  expect(screen.getByText("Shared")).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Share" })).toBeNull()
})

it("switches to the shared version and offers to share the latest", () => {
  render(<ArtifactViewerPanel />)
  fireEvent.click(screen.getByRole("radio", { name: "Shared · v2" }))
  expect(state.versionArg.at(-1)).toBe(2)
  cleanup()
  state.snapshot.data = snap({ manifest: { ...snap().manifest, version: 2 } })
  render(<ArtifactViewerPanel />)
  expect(screen.getByText(/You see the shared version v2, the one people with the link get. Latest is v3./)).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Select" })).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Share v3" }))
  expect(state.share).toHaveBeenCalledWith({ base_version: 3, version: 3 }, expect.anything())
})

it("offers Back to chat in fullscreen", () => {
  render(<ArtifactViewerPanel variant="fullscreen" />)
  expect(screen.getByRole("button", { name: "Back to chat" })).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Close viewer" })).toBeNull()
})

it.each([["Send to agent", true], ["Save comment", false]] as const)("%s posts the exact anchor with send_to_agent=%s", (button, send) => {
  render(<ArtifactViewerPanel />)
  selectHero()
  expect(screen.getByText(/On #hero/)).toHaveTextContent("On #hero · “Big idea” · v3")
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "Make it bolder" } })
  fireEvent.click(screen.getByRole("button", { name: button }))
  expect(state.comment).toHaveBeenCalledWith(
    { version: 3, path: "home.html", selector: "#hero", quote: "Big idea", text: "Make it bolder", send_to_agent: send },
    expect.anything(),
  )
  act(() => state.comment.mock.calls[0][1].onSuccess({}))
  expect(screen.queryByRole("textbox", { name: "Comment" })).toBeNull()
})

it("keeps the segmented screen switch for up to four screens and adds a compact select only beyond", () => {
  render(<ArtifactViewerPanel />)
  expect(screen.getByRole("radiogroup", { name: "Screen" }).parentElement!.className).not.toContain("hidden")
  expect(screen.queryByRole("combobox", { name: "Screen (compact)" })).toBeNull()
  cleanup()
  const five = ["home.html", "a.html", "b.html", "c.html", "d.html"].map(html)
  state.snapshot.data = snap({ manifest: { version: 3, entrypoint: "home.html", files: five } })
  render(<ArtifactViewerPanel />)
  expect(screen.getByRole("combobox", { name: "Screen (compact)" })).toBeInTheDocument()
})

it("labels screens by their <title>", () => {
  const titled = (path: string, title: string) => ({ path, content: `<title>${title}</title><body>x</body>`, media_type: "text/html" })
  state.snapshot.data = snap({ manifest: { version: 3, entrypoint: "plans.html", files: [titled("plans.html", "Our plans"), titled("faq.html", "Questions")] } })
  render(<ArtifactViewerPanel />)
  expect(within(screen.getByRole("radiogroup", { name: "Screen" })).getAllByRole("radio").map(el => el.textContent)).toEqual(["Our plans", "Questions"])
  expect(screen.getByTitle("Preview of Our plans")).toBeInTheDocument()
})

it("drops the sent notice when a new version arrives or a new selection starts", () => {
  const send = () => {
    selectHero()
    fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "Bolder" } })
    fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
    act(() => state.comment.mock.calls.at(-1)![1].onSuccess({}))
    expect(screen.getByText("Comment saved for the agent")).toBeInTheDocument()
  }
  const { rerender } = render(<ArtifactViewerPanel />)
  send()
  fireEvent.click(screen.getByRole("button", { name: "Select" }))
  expect(screen.queryByText("Comment saved for the agent")).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Select" }))
  send()
  state.snapshot = { ...state.snapshot, data: snap({ latest_version: 4, manifest: { ...snap().manifest, version: 4 } }) }
  rerender(<ArtifactViewerPanel />)
  expect(screen.queryByText("Comment saved for the agent")).toBeNull()
})

it("keeps the draft and shows one conflict banner on a version conflict", () => {
  render(<ArtifactViewerPanel />)
  selectHero()
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "Keep me" } })
  fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
  act(() => state.comment.mock.calls[0][1].onError(err(409, "artifact_version_conflict")))
  act(() => state.comment.mock.calls[0][1].onError(err(409, "artifact_version_conflict")))
  expect(screen.getAllByText("Changed meanwhile")).toHaveLength(1)
  expect(screen.getByText(/Someone saved a newer version while you were working\. Nothing was overwritten\. Your comment and selection are kept/)).toBeInTheDocument()
  expect(screen.getByRole("textbox", { name: "Comment" })).toHaveValue("Keep me")
  fireEvent.click(screen.getByRole("button", { name: "Reload and reapply" }))
  expect(state.snapshot.refetch).toHaveBeenCalled()
})

it("asks for a new selection when the anchor no longer matches", () => {
  render(<ArtifactViewerPanel />)
  selectHero()
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "Draft" } })
  fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
  act(() => state.comment.mock.calls[0][1].onError(err(409, "artifact_anchor_mismatch")))
  expect(screen.getByText("This element changed. Select it again.")).toBeInTheDocument()
  expect(screen.getByRole("textbox", { name: "Comment" })).toHaveValue("Draft")
  expect(screen.getByRole("button", { name: "Send to agent" })).toBeDisabled()
})

it("warns about quota and reports a full history", () => {
  state.snapshot.data = snap({ quota: { used_bytes: 8 * 1024 * 1024, limit_bytes: 10 * 1024 * 1024, warning: true } })
  render(<ArtifactViewerPanel />)
  expect(screen.getByText("This project is close to its limit")).toBeInTheDocument()
  expect(screen.getByText(/8\.0 MB of 10 MB \(80%\)\. History is never deleted without you\./)).toBeInTheDocument()
  expect(screen.getByRole("progressbar", { name: "History storage used" })).toHaveAttribute("aria-valuenow", "80")
  selectHero()
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "x" } })
  fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
  act(() => state.comment.mock.calls[0][1].onError(err(413, "artifact_history_quota")))
  expect(screen.getByText("Saving is paused")).toBeInTheDocument()
  expect(screen.getByText(/History is full, so this was not saved\. Your comment stays here/)).toBeInTheDocument()
  expect(screen.getByRole("textbox", { name: "Comment" })).toHaveValue("x")
})

it("restores against the latest version from the history list", () => {
  render(<ArtifactViewerPanel />)
  expect(screen.queryByRole("region", { name: "Version history" })).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "History" }))
  const rows = history().getAllByRole("listitem")
  expect(rows.map(row => row.querySelector("[data-slot=version-number]")?.textContent)).toEqual(["v3", "v2", "v1"])
  expect(within(rows[0]).getByText("Latest")).toBeInTheDocument()
  expect(within(rows[1]).getByText("Shared")).toBeInTheDocument()
  expect(within(rows[0]).getByText(/Restored from v1/)).toBeInTheDocument()
  expect(within(rows[0]).getByText("Ada · Restore")).toBeInTheDocument()
  expect(within(rows[0]).queryByRole("button", { name: /Restore as/ })).toBeNull()
  fireEvent.click(within(rows[2]).getByRole("button", { name: "Restore as new version" }))
  expect(state.restore).toHaveBeenCalledWith({ base_version: 3, version: 1 }, expect.anything())
  act(() => state.restore.mock.calls[0][1].onSuccess({ version: 4 }))
  expect(screen.getByText("Restored v1 as v4 · nothing deleted")).toBeInTheDocument()
  fireEvent.click(within(history().getAllByRole("listitem")[2]).getByRole("button", { name: "View" }))
  expect(state.versionArg.at(-1)).toBe(1)
})

it("opens history for target.tab=versions", () => {
  state.target = { slug: "launch", tab: "versions" }
  render(<ArtifactViewerPanel />)
  expect(screen.getByRole("region", { name: "Version history" })).toHaveFocus()
})

it("keeps an unsent comment when a card asks for the comments or history of the same project", () => {
  const { rerender } = render(<ArtifactViewerPanel />)
  selectHero()
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "Bolder" } })
  state.target = { slug: "launch", tab: "comments" }
  rerender(<ArtifactViewerPanel />)
  expect(screen.getByRole("textbox", { name: "Comment" })).toHaveValue("Bolder")
  state.target = { slug: "launch", tab: "versions" }
  rerender(<ArtifactViewerPanel />)
  expect(screen.getByRole("region", { name: "Version history" })).toHaveFocus()
  expect(screen.getByRole("textbox", { name: "Comment" })).toHaveValue("Bolder")
})

it("names the reader's own comments You and never shows a raw account id", () => {
  state.snapshot.data = snap({ comments: [
    comment({ id: "c1", path: "home.html", text: "Mine", author: "user-me" }),
    comment({ id: "c2", path: "home.html", text: "Theirs", author: "cmuxw94hh0001de39519d" }),
  ] })
  render(<ArtifactViewerPanel />)
  const queue = within(screen.getByLabelText("Comment queue"))
  expect(queue.getByText(/^You · /)).toBeInTheDocument()
  expect(queue.queryByText(/cmuxw94hh0001de39519d|user-me/)).toBeNull()
})

it("Send to agent starts the agent in this chat for the saved comment and offers a retry when it does not start", async () => {
  const sendToAgent = vi.fn().mockResolvedValueOnce(false).mockResolvedValueOnce(true)
  state.sendToAgent = sendToAgent
  state.snapshot.data=snap({origin:{chat_id:"origin-chat",agent_id:"origin-agent"}})
  render(<ArtifactViewerPanel />)
  selectHero()
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "Bolder" } })
  fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
  const saved = { id: "cm9", version: 3, path: "home.html", selector: "#hero", quote: "Big idea", text: "Bolder", task_id: "t9" }
  await act(async () => { state.comment.mock.calls.at(-1)![1].onSuccess(saved) })
  expect(sendToAgent).toHaveBeenCalledWith({ pageId: "p1", comment: saved })
  expect(await screen.findByText(/agent did not start/i)).toBeInTheDocument()
  state.snapshot.data = snap({origin:{chat_id:"origin-chat",agent_id:"origin-agent"}})
  state.snapshot.refetch.mockResolvedValue({data:snap({origin:{chat_id:"origin-chat",agent_id:"origin-agent"},comments:[{...saved,author:"user-me",status:"pending",delivery_retryable:true}]}),error:null})
  // A real snapshot update preserves the original failed delivery state.
  // The fresh read then authorizes retry of that same saved task.
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Retry" })) })
  expect(sendToAgent).toHaveBeenCalledTimes(2)
  expect(await screen.findByText("Comment sent to the agent")).toBeInTheDocument()
  expect(screen.queryByText(/agent did not start/i)).toBeNull()
})

it("Save only never starts the agent", async () => {
  const sendToAgent = vi.fn().mockResolvedValue(true)
  state.sendToAgent = sendToAgent
  render(<ArtifactViewerPanel />)
  selectHero()
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "Later" } })
  fireEvent.click(screen.getByRole("button", { name: "Save comment" }))
  await act(async () => { state.comment.mock.calls.at(-1)![1].onSuccess({ id: "cm8", version: 3, path: "home.html", selector: "#hero", quote: "Big idea", text: "Later" }) })
  expect(sendToAgent).not.toHaveBeenCalled()
})

it("says so when the screen has nothing a comment can anchor to", () => {
  const bare = { path: "home.html", content: "<body><h1>No ids here</h1></body>", media_type: "text/html" }
  state.snapshot.data = snap({ manifest: { ...snap().manifest, entrypoint: "home.html", files: [bare] } })
  render(<ArtifactViewerPanel />)
  expect(screen.queryByText(/nothing on this screen can take a comment/i)).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Select" }))
  expect(screen.getByText(/nothing on this screen can take a comment/i)).toBeInTheDocument()
})

it("does not warn on a screen with anchorable parts", () => {
  render(<ArtifactViewerPanel />)
  fireEvent.click(screen.getByRole("button", { name: "Select" }))
  expect(screen.queryByText(/nothing on this screen can take a comment/i)).toBeNull()
})

it("names version authors You or Agent instead of account and run ids", () => {
  state.target = { slug: "launch", tab: "versions" }
  state.snapshot.data = snap({ versions: [
    { version: 1, message: "First", actor: "user-me", created_at: "2026-10-20T10:00:00Z" },
    { version: 2, message: "Bolder plans", actor: "agent:cmuxw95k60027db59c6f2:msg_1793118136200864661_c3b6aa1c9005c313", created_at: "2026-10-21T10:00:00Z" },
    { version: 3, message: "Restore version 1", actor: "cmuxw94hh0001de39519d", created_at: "2026-10-22T10:00:00Z", restored_from: 1 },
  ] })
  render(<ArtifactViewerPanel />)
  const rows = history()
  expect(rows.getByText("You · First")).toBeInTheDocument()
  expect(rows.getByText("Agent · Bolder plans")).toBeInTheDocument()
  expect(rows.getByText("Restore version 1")).toBeInTheDocument()
  expect(rows.queryByText(/cmuxw9|msg_/)).toBeNull()
})

it("shares only when the viewer can write, after confirming", () => {
  render(<ArtifactViewerPanel />)
  fireEvent.click(screen.getByRole("button", { name: "Share" }))
  expect(screen.getByText(/People with access will see v3. Later versions stay private until you share them./)).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Share v3" }))
  expect(state.share).toHaveBeenCalledWith({ base_version: 3, version: 3 }, expect.anything())
  cleanup()
  state.snapshot.data = snap({ can_write: false, versions: [] })
  render(<ArtifactViewerPanel />)
  expect(screen.queryByRole("button", { name: /Share|Restore|History/ })).toBeNull()
})

it.each([[403, "You can't open this artifact"], [404, "This artifact isn't available"]] as const)("hides the project behind a generic message on %i", (status, message) => {
  state.snapshot = { data: undefined, isLoading: false, error: err(status, "forbidden"), refetch: vi.fn() }
  render(<ArtifactViewerPanel />)
  expect(screen.getByText(message)).toBeInTheDocument()
  expect(screen.queryByText("Launch site")).toBeNull()
})

const comment = (patch: Partial<ArtifactComment>): ArtifactComment => ({ id: "c", version: 3, path: "home.html", selector: "#hero", quote: "Big idea", text: "t", ...patch })
it.each<[Partial<ArtifactComment>, string | null]>([
  [{ status: "pending" }, "Waiting"],
  [{ status: "working" }, "Agent working"],
  [{ status: "answered", answered_version: 5 }, "Done · v5"],
  [{ status: "answered", replies: [{ text: "ok", version: 4 }] }, "Done · v4"],
  [{ status: "needs_clarification" }, "Needs a new selection"],
  [{ replies: [{ text: "ok", version: 6 }] }, "Done · v6"],
  [{ task_id: "t1" }, "Sent to agent"],
  [{}, null],
])("queue chip for %j is %j", (patch, label) => {
  expect(commentQueueChip(comment(patch))?.label ?? null).toBe(label)
})

it("lists the queue, focuses it for target.tab=comments and jumps to the comment's screen", () => {
  state.target = { slug: "launch", tab: "comments" }
  state.snapshot.data = snap({ comments: [comment({ id: "c1", path: "about.html", text: "Fix the title", status: "working", author: "Ada" })] })
  render(<ArtifactViewerPanel />)
  const queue = screen.getByRole("region", { name: "Comment queue" })
  expect(queue).toHaveFocus()
  expect(within(queue).getByText("Agent working")).toBeInTheDocument()
  expect(within(queue).getByText("Ada · About · “Big idea”")).toBeInTheDocument()
  fireEvent.click(within(queue).getByRole("button", { name: /Fix the title/ }))
  expect(within(screen.getByRole("radiogroup", { name: "Screen" })).getByRole("radio", { name: "About" })).toHaveAttribute("aria-checked", "true")
})

it.each([403, 404])("drops the cached project when a background refresh answers %i: no stale preview after access is lost", status => {
  state.snapshot = { data: snap(), isLoading: false, error: err(status, "forbidden"), refetch: vi.fn() }
  render(<ArtifactViewerPanel />)
  expect(screen.getByText(status === 403 ? "You can't open this artifact" : "This artifact isn't available")).toBeInTheDocument()
  expect(screen.queryByTitle(/Preview of/)).toBeNull()
  expect(screen.queryByText("Launch site")).toBeNull()
})

it("keeps the last project through a transient refresh failure", () => {
  state.snapshot = { data: snap(), isLoading: false, error: err(0, "unavailable"), refetch: vi.fn() }
  render(<ArtifactViewerPanel />)
  expect(screen.getByTitle(/Preview of/)).toBeInTheDocument()
})

for (const phase of ["working", "answered"] as const) {
 it(`removes stale delivery Retry for exact authoritative ${phase} task, including a late timeout`, async () => {
  let resolveDispatch!: (ok: boolean) => void
  state.sendToAgent = vi.fn(() => new Promise<boolean>(resolve => { resolveDispatch = resolve }))
  const view = render(<ArtifactViewerPanel />)
  selectHero()
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "Bolder" } })
  fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
  const saved: ArtifactComment = { id: "cm9", version: 3, path: "home.html", selector: "#hero", quote: "Big idea", text: "Bolder", task_id: "t9", status: "pending" }
  await act(async () => { state.comment.mock.calls.at(-1)![1].onSuccess(saved) })
  state.snapshot.data = snap({ comments: [{ ...saved, status: phase }] })
  view.rerender(<ArtifactViewerPanel />)
  await act(async () => { resolveDispatch(false) })
  expect(screen.queryByText(/agent did not start/i)).toBeNull()
  expect(screen.queryByRole("button", { name: "Retry" })).toBeNull()
  expect(state.sendToAgent).toHaveBeenCalledTimes(1)
 })
}

it("keeps a failed pending delivery Retry when another task is answered, then clears only its exact task", async () => {
 state.sendToAgent = vi.fn().mockResolvedValue(false)
 const view = render(<ArtifactViewerPanel />)
 selectHero()
 fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "Bolder" } })
 fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
 const saved: ArtifactComment = { id: "cm9", version: 3, path: "home.html", selector: "#hero", quote: "Big idea", text: "Bolder", task_id: "t9", status: "pending" }
 await act(async () => { state.comment.mock.calls.at(-1)![1].onSuccess(saved) })
 expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument()
 state.snapshot.data = snap({ comments: [saved, { ...saved, id: "other", task_id: "other-task", status: "answered" }] })
 view.rerender(<ArtifactViewerPanel />)
 expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument()
 state.snapshot.data = snap({ comments: [{ ...saved, status: "answered", answered_version: 4 }] })
 view.rerender(<ArtifactViewerPanel />)
 expect(screen.queryByRole("button", { name: "Retry" })).toBeNull()
 expect(state.sendToAgent).toHaveBeenCalledTimes(1)
})

it("preserves a legacy Project's ordinary preview with a dashboard.json asset when Dashboard is off", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACT_DASHBOARDS", "false")
  try {
    const snapshot = snap()
    snapshot.manifest.files.push({ path: "dashboard.json", content: '{"schema_version":1,"collections":["sales"]}', media_type: "application/json" })
    snapshot.manifest.files.find(file => file.path === "home.html")!.content += '<script>window.ordinaryProject=true</script>'
    state.snapshot.data = snapshot
    render(<ArtifactViewerPanel />)
    expect(screen.getByTitle(/Preview of/)).toHaveAttribute("srcdoc", expect.stringContaining("window.ordinaryProject=true"))
    expect(screen.queryByText("Dashboard preview is unavailable.")).toBeNull()
  } finally { vi.unstubAllEnvs() }
})


it.each([["true", true, "Dashboard"], ["false", true, "Project"], ["true", false, "Project"]] as const)("labels only the opted-in selected valid Dashboard (%s/%s)", (flag, valid, label) => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACT_DASHBOARDS", flag)
  try {
    const snapshot = snap()
    snapshot.manifest.files.push({ path: "dashboard.json", media_type: "application/json", content: valid ? '{"schema_version":1,"collections":["sales"]}' : '{}' })
    state.snapshot.data = snapshot
    render(<ArtifactViewerPanel />)
    expect(screen.getByText(label, { exact: true })).toBeInTheDocument()
  } finally { vi.unstubAllEnvs() }
})

it.each([false, undefined])("keeps reader controls inert when can_write is %s", canWrite => {
  state.snapshot.data = snap({ can_write: canWrite, comments: [comment({ text: "Existing feedback" })] })
  render(<ArtifactViewerPanel />)
  expect(screen.queryByRole("button", { name: "Select" })).toBeNull()
  expect(screen.queryByRole("form", { name: "New comment" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Send to agent" })).toBeNull()
  expect(screen.getByText("Existing feedback")).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "History" }))
  expect(screen.getByRole("region", { name: "Version history" })).toBeInTheDocument()
  expect(state.comment).not.toHaveBeenCalled()
})

it("does not invite a read-only reader to select an element in an empty queue", () => {
  state.snapshot.data = snap({ can_write: false })
  render(<ArtifactViewerPanel />)
  expect(screen.getByText("No comments yet.", { exact: true })).toBeInTheDocument()
  expect(screen.queryByText(/Turn on Select/)).toBeNull()
})

it("retires a lost writer's anchor and selection while preserving the unsent draft", () => {
  const view = render(<ArtifactViewerPanel />)
  selectHero()
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "Keep my draft" } })
  const frame = screen.getByTitle(/Preview of/) as HTMLIFrameElement
  state.snapshot.data = snap({ can_write: false })
  view.rerender(<ArtifactViewerPanel />)
  expect(screen.queryByRole("button", { name: "Select" })).toBeNull()
  expect(screen.queryByRole("textbox", { name: "Comment" })).toBeNull()
  act(() => window.dispatchEvent(new MessageEvent("message", { source: frame.contentWindow, data: { type: "crewship-artifact-select", selector: "#hero", quote: "Big idea" } })))
  state.snapshot.data = snap()
  view.rerender(<ArtifactViewerPanel />)
  expect(screen.getByRole("textbox", { name: "Comment" })).toHaveValue("Keep my draft")
  expect(screen.getByRole("button", { name: "Save comment" })).toBeDisabled()
  expect(screen.getByRole("button", { name: "Send to agent" })).toBeDisabled()
  expect(screen.getByRole("button", { name: "Select" })).toHaveAttribute("aria-pressed", "false")
  expect(state.comment).not.toHaveBeenCalled()
})

it("never dispatches a saved comment callback after write authority has changed", async () => {
  const send = vi.fn().mockResolvedValue(true)
  state.sendToAgent = send
  const view = render(<ArtifactViewerPanel />)
  selectHero()
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "Preserve pending draft" } })
  fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
  const callbacks = state.comment.mock.calls.at(-1)![1]
  const saved = { id: "pending-comment", version: 3, path: "home.html", selector: "#hero", quote: "Big idea", text: "Preserve pending draft" }
  state.snapshot.data = snap({ can_write: false })
  view.rerender(<ArtifactViewerPanel />)
  await act(async () => callbacks.onSuccess(saved))
  expect(send).not.toHaveBeenCalled()
  state.snapshot.data = snap()
  view.rerender(<ArtifactViewerPanel />)
  await act(async () => callbacks.onSuccess(saved))
  expect(send).not.toHaveBeenCalled()
  expect(screen.getByRole("textbox", { name: "Comment" })).toHaveValue("Preserve pending draft")
})

it("removes agent retry mutations when the writer becomes a reader", async () => {
  const send = vi.fn().mockResolvedValue(false)
  state.sendToAgent = send
  const view = render(<ArtifactViewerPanel />)
  selectHero()
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "Retry later" } })
  fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
  await act(async () => state.comment.mock.calls.at(-1)![1].onSuccess({ id: "saved", version: 3 }))
  expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument()
  state.snapshot.data = snap({ can_write: false })
  view.rerender(<ArtifactViewerPanel />)
  expect(screen.queryByRole("button", { name: "Retry" })).toBeNull()
  expect(send).toHaveBeenCalledTimes(1)
})


it.each([false, true])("fences a pending comment only when refresh loses authority (query error: %s)", queryError => {
  const send = vi.fn().mockResolvedValue(true)
  state.sendToAgent = send
  const view = render(<ArtifactViewerPanel />)
  selectHero()
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "Pending refresh" } })
  fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
  const callbacks = state.comment.mock.calls.at(-1)![1]
  // A new ordinary snapshot object and newer private version are not a permission change.
  state.snapshot.data = snap({ latest_version: 4 })
  state.snapshot.error = queryError ? err(0, "unavailable") : null
  view.rerender(<ArtifactViewerPanel />)
  if (queryError) expect(screen.queryByRole("button", { name: "Select" })).toBeNull()
  return act(async () => {
    callbacks.onSuccess({ id: "pending-refresh", version: 3, path: "home.html", selector: "#hero", quote: "Big idea", text: "Pending refresh" })
    if (queryError) expect(send).not.toHaveBeenCalled()
    else expect(send).toHaveBeenCalledTimes(1)
  })
})

it.each([["true", "slides", "typed"], ["true", undefined, "unsupported"], ["false", "slides", "legacy"]] as const)("routes the selected deck safely with Slides flag %s and kind %s", (flag, kind, mode) => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACT_SLIDES", flag)
  try {
    const data = Object.assign(snap(), { artifact_kind: kind })
    data.manifest.files.push({ path: "deck.json", media_type: "application/json", content: '{"schema_version":1,"order":["intro"]}' })
    state.snapshot.data = data
    render(<ArtifactViewerPanel />)
    if (mode === "typed") {
      expect(screen.getByText("Typed slides preview")).toBeInTheDocument()
      expect(screen.getByText("Slides", { exact: true })).toBeInTheDocument()
    } else if (mode === "unsupported") expect(screen.getByText("This Slides format is unsupported.")).toBeInTheDocument()
    else expect(screen.getByTitle(/Preview of/)).toHaveAttribute("srcdoc", expect.stringContaining("Big  idea"))
    if (mode !== "legacy") expect(screen.queryByTitle(/Preview of/)).toBeNull()
  } finally { vi.unstubAllEnvs() }
})

it("refuses ambiguous deck and dashboard source without either preview", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACT_SLIDES", "true")
  vi.stubEnv("NEXT_PUBLIC_ARTIFACT_DASHBOARDS", "true")
  try {
    const data = Object.assign(snap(), { artifact_kind: "slides" })
    data.manifest.files.push({ path: "deck.json", media_type: "application/json", content: '{}' }, { path: "dashboard.json", media_type: "application/json", content: '{"schema_version":1,"collections":["sales"]}' })
    state.snapshot.data = data
    render(<ArtifactViewerPanel />)
    expect(screen.getByText("This Slides format is unsupported.")).toBeInTheDocument()
    expect(screen.queryByText("Typed slides preview")).toBeNull()
    expect(screen.queryByText("Bound dashboard")).toBeNull()
    expect(screen.queryByTitle(/Preview of/)).toBeNull()
  } finally { vi.unstubAllEnvs() }
})

function retrySnapshot(): ArtifactProjectSnapshot {
 return snap({origin:{chat_id:"origin-chat",agent_id:"origin-agent"},comments:[{id:"waiting-c1",task_id:"waiting-t1",author:"user-me",version:3,path:"home.html",selector:"#hero",quote:"Big idea",text:"Saved busy comment",status:"pending",delivery_retryable:true}]})
}

it("recovers an exact persisted Waiting task after close/reopen without creating another comment", async()=>{
 state.snapshot.data=retrySnapshot()
 state.snapshot.refetch.mockResolvedValue({data:retrySnapshot(),error:null})
 state.sendToAgent=vi.fn().mockResolvedValue(true)
 const view=render(<ArtifactViewerPanel />)
 expect(screen.getByRole("button",{name:"Retry"})).toBeInTheDocument()
 state.target=null;view.rerender(<ArtifactViewerPanel />)
 state.target={slug:"launch"};view.rerender(<ArtifactViewerPanel />)
 await act(async()=>fireEvent.click(screen.getByRole("button",{name:"Retry"})))
 expect(state.snapshot.refetch).toHaveBeenCalledTimes(1)
 expect(state.sendToAgent).toHaveBeenCalledWith({pageId:"p1",comment:retrySnapshot().comments[0]})
 expect(state.comment).not.toHaveBeenCalled()
 expect(screen.queryByRole("button",{name:"Retry"})).toBeNull()
})

it.each(["accepted", "missing eligibility", "claimed", "new head", "changed origin", "lost write", "lost send", "missing send"])("fresh Retry read fences %s without another dispatch", async change=>{
 state.snapshot.data=retrySnapshot()
 const fresh=retrySnapshot()
 if(change==="accepted")fresh.comments[0].delivery_retryable=false
 if(change==="missing eligibility")delete fresh.comments[0].delivery_retryable
 if(change==="claimed"){fresh.comments[0].status="working";fresh.comments[0].delivery_retryable=false}
 if(change==="new head")fresh.latest_version=4
 if(change==="changed origin")fresh.origin={chat_id:"different-chat",agent_id:"origin-agent"}
 if(change==="lost write")fresh.can_write=false
 if(change==="lost send")fresh.can_send_to_agent=false
 if(change==="missing send")delete fresh.can_send_to_agent
 state.snapshot.refetch.mockResolvedValue({data:fresh,error:null})
 state.sendToAgent=vi.fn().mockResolvedValue(true)
 render(<ArtifactViewerPanel />)
 await act(async()=>fireEvent.click(screen.getByRole("button",{name:"Retry"})))
 expect(state.sendToAgent).not.toHaveBeenCalled()
 expect(state.comment).not.toHaveBeenCalled()
})

it("fences a late Retry refresh after Page rescope",async()=>{
 state.snapshot.data=retrySnapshot()
 let settle!: (value:unknown)=>void
 state.snapshot.refetch.mockImplementation(()=>new Promise(resolve=>{settle=resolve}))
 state.sendToAgent=vi.fn().mockResolvedValue(true)
 const view=render(<ArtifactViewerPanel />)
 fireEvent.click(screen.getByRole("button",{name:"Retry"}))
 state.target={slug:"other"};view.rerender(<ArtifactViewerPanel />)
 await act(async()=>settle({data:retrySnapshot(),error:null}))
 expect(state.sendToAgent).not.toHaveBeenCalled()
})

it("disables a duplicate Retry click while its fresh delivery read is pending",async()=>{
 state.snapshot.data=retrySnapshot()
 let settle!: (value:unknown)=>void
 state.snapshot.refetch.mockImplementation(()=>new Promise(resolve=>{settle=resolve}))
 state.sendToAgent=vi.fn().mockResolvedValue(true)
 render(<ArtifactViewerPanel />)
 const button=screen.getByRole("button",{name:"Retry"})
 fireEvent.click(button)
 expect(button).toBeDisabled()
 fireEvent.click(button)
 expect(state.snapshot.refetch).toHaveBeenCalledTimes(1)
 await act(async()=>settle({data:retrySnapshot(),error:null}))
 expect(state.sendToAgent).toHaveBeenCalledTimes(1)
 expect(state.comment).not.toHaveBeenCalled()
})

 it("fences a late Retry refresh across a human identity change on the same Page",async()=>{
 state.snapshot.data=retrySnapshot()
 let settle!: (value:unknown)=>void
 state.snapshot.refetch.mockImplementation(()=>new Promise(resolve=>{settle=resolve}))
 state.sendToAgent=vi.fn().mockResolvedValue(true)
 const view=render(<ArtifactViewerPanel />)
 fireEvent.click(screen.getByRole("button",{name:"Retry"}))
 state.userId="different-human"
 view.rerender(<ArtifactViewerPanel />)
 await act(async()=>settle({data:retrySnapshot(),error:null}))
 expect(state.sendToAgent).not.toHaveBeenCalled()
 })

it("restores usable Retry after write authority leaves and returns during an old refresh",async()=>{
 state.snapshot.data=retrySnapshot()
 const reads: ((value:unknown)=>void)[]=[]
 state.snapshot.refetch.mockImplementation(()=>new Promise(resolve=>reads.push(resolve)))
 state.sendToAgent=vi.fn().mockResolvedValue(true)
 const view=render(<ArtifactViewerPanel />)
 fireEvent.click(screen.getByRole("button",{name:"Retry"}))
 state.snapshot.data={...retrySnapshot(),can_write:false};view.rerender(<ArtifactViewerPanel />)
 state.snapshot.data=retrySnapshot();view.rerender(<ArtifactViewerPanel />)
 const renewed=screen.getByRole("button",{name:"Retry"})
 expect(renewed).toBeEnabled()
 fireEvent.click(renewed)
 expect(reads).toHaveLength(2)
 await act(async()=>reads[0]({data:retrySnapshot(),error:null}))
 expect(screen.getByRole("button",{name:"Retry"})).toBeDisabled()
 expect(state.sendToAgent).not.toHaveBeenCalled()
 await act(async()=>reads[1]({data:retrySnapshot(),error:null}))
 expect(state.sendToAgent).toHaveBeenCalledTimes(1)
})

it.each(["another-origin",null])("fences an old Retry refresh after origin changes to %s",async next=>{
 state.snapshot.data=retrySnapshot()
 let settle!: (value:unknown)=>void
 state.snapshot.refetch.mockImplementation(()=>new Promise(resolve=>{settle=resolve}))
 state.sendToAgent=vi.fn().mockResolvedValue(true)
 const view=render(<ArtifactViewerPanel />)
 fireEvent.click(screen.getByRole("button",{name:"Retry"}))
 state.snapshot.data={...retrySnapshot(),origin:next?{chat_id:next,agent_id:"origin-agent"}:null}
 view.rerender(<ArtifactViewerPanel />)
 await act(async()=>settle({data:retrySnapshot(),error:null}))
 expect(state.sendToAgent).not.toHaveBeenCalled()
})

it.each([false,undefined,"true"])("withholds owner actions from an editor when capabilities are %s",cap=>{
 state.snapshot.data=snap({can_share:cap,can_restore:cap,can_send_to_agent:cap} as unknown as Partial<ArtifactProjectSnapshot>)
 render(<ArtifactViewerPanel />)
 fireEvent.click(screen.getByRole("button",{name:"History"}))
 expect(screen.queryAllByRole("button",{name:/Share|Restore as new version/})).toHaveLength(0)
 selectHero();fireEvent.change(screen.getByRole("textbox",{name:"Comment"}),{target:{value:"Feedback"}})
 expect(screen.queryByRole("button",{name:"Send to agent"})).toBeNull()
 fireEvent.submit(screen.getByRole("form",{name:"New comment"}))
 expect(state.comment).toHaveBeenCalledWith(expect.objectContaining({send_to_agent:false}),expect.anything())
})
it("retires an acknowledged agent comment when send permission changes while Edit remains",async()=>{
 const send=vi.fn().mockResolvedValue(true);state.sendToAgent=send
 const view=render(<ArtifactViewerPanel />)
 selectHero();fireEvent.change(screen.getByRole("textbox",{name:"Comment"}),{target:{value:"Feedback"}})
 fireEvent.click(screen.getByRole("button",{name:"Send to agent"}))
 const success=state.comment.mock.calls[0][1].onSuccess
 state.snapshot.data=snap({can_send_to_agent:false});view.rerender(<ArtifactViewerPanel />)
 await act(async()=>success(comment({task_id:"task"})))
 expect(send).not.toHaveBeenCalled()
})
it("offers only editors an escaped unpinned source editor link",()=>{
 state.target={slug:"a/b ?&"}
 render(<ArtifactViewerPanel variant="library" />)
 expect(screen.getByRole("link",{name:"Edit source"})).toHaveAttribute("href","/pages/a%2Fb%20%3F%26?artifact=1&workspace_id=ws1")
 cleanup();state.snapshot.data=snap({can_write:false});render(<ArtifactViewerPanel variant="library" />)
 expect(screen.queryByRole("link",{name:"Edit source"})).toBeNull()
})

it("links the chat viewer to the artifact's own address in Artifacts only with the library on", () => {
  render(<ArtifactViewerPanel />)
  expect(screen.queryByRole("link", { name: /Open in Artifacts/ })).toBeNull()
  cleanup()
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true")
  try {
    render(<ArtifactViewerPanel />)
    expect(screen.getByRole("link", { name: /Open in Artifacts/ }).getAttribute("href")).toBe("/artifacts/p1?workspace_id=ws1")
    cleanup()
    render(<ArtifactViewerPanel variant="library" />)
    expect(screen.queryByRole("link", { name: /Open in Artifacts/ })).toBeNull()
  } finally { vi.unstubAllEnvs() }
})
