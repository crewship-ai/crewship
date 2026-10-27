import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { ArtifactComment, ArtifactProjectSnapshot } from "../../projects/types"
import type { ArtifactViewerTarget } from "../../projects/artifact-viewer-context"

const state = vi.hoisted(() => ({
  target: null as ArtifactViewerTarget | null,
  close: vi.fn(),
  snapshot: { data: undefined as unknown, isLoading: false, error: null as unknown, refetch: vi.fn() },
  versionArg: [] as (number | undefined)[],
  restore: vi.fn(), share: vi.fn(), comment: vi.fn(),
}))

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

import { ArtifactViewerPanel, commentQueueChip } from "../artifact-viewer-panel"

const html = (name: string) => ({ path: name, content: `<body><h1 id="hero">Big  idea</h1>${name}</body>`, media_type: "text/html" })
function snap(patch: Partial<ArtifactProjectSnapshot> = {}): ArtifactProjectSnapshot {
  return {
    page_id: "p1", name: "Launch site", slug: "launch", can_write: true, latest_version: 3, shared_version: 2,
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
  state.target = { slug: "launch" }
  state.snapshot = { data: snap(), isLoading: false, error: null, refetch: vi.fn() }
  state.versionArg = []
  for (const fn of [state.restore, state.share, state.comment, state.close]) fn.mockReset()
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
    expect(screen.getByText("Comment sent to the agent")).toBeInTheDocument()
  }
  const { rerender } = render(<ArtifactViewerPanel />)
  send()
  fireEvent.click(screen.getByRole("button", { name: "Select" }))
  expect(screen.queryByText("Comment sent to the agent")).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Select" }))
  send()
  state.snapshot = { ...state.snapshot, data: snap({ latest_version: 4, manifest: { ...snap().manifest, version: 4 } }) }
  rerender(<ArtifactViewerPanel />)
  expect(screen.queryByText("Comment sent to the agent")).toBeNull()
})

it("keeps the draft and shows one conflict banner on a version conflict", () => {
  render(<ArtifactViewerPanel />)
  selectHero()
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "Keep me" } })
  fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
  act(() => state.comment.mock.calls[0][1].onError(err(409, "artifact_version_conflict")))
  act(() => state.comment.mock.calls[0][1].onError(err(409, "artifact_version_conflict")))
  expect(screen.getAllByText("Changed meanwhile")).toHaveLength(1)
  expect(screen.getByText("Someone saved a newer version while you were working. Nothing was overwritten.")).toBeInTheDocument()
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
  expect(screen.getByText(/8\.0 MB of 10 MB \(80%\)\. Old versions stay until someone decides\./)).toBeInTheDocument()
  expect(screen.getByRole("progressbar", { name: "History storage used" })).toHaveAttribute("aria-valuenow", "80")
  selectHero()
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "x" } })
  fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
  act(() => state.comment.mock.calls[0][1].onError(err(413, "artifact_history_quota")))
  expect(screen.getByText("History is full. Your changes were not saved and remain here.")).toBeInTheDocument()
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

it("hides the project behind a generic message on 403 and 404", () => {
  state.snapshot = { data: undefined, isLoading: false, error: err(403, "forbidden"), refetch: vi.fn() }
  render(<ArtifactViewerPanel />)
  expect(screen.getByText("You don’t have access to this project")).toBeInTheDocument()
  expect(screen.queryByText("Launch site")).toBeNull()
})

const comment = (patch: Partial<ArtifactComment>): ArtifactComment => ({ id: "c", version: 3, path: "home.html", selector: "#hero", quote: "Big idea", text: "t", ...patch })
it.each<[Partial<ArtifactComment>, string | null]>([
  [{ status: "pending" }, "Waiting"],
  [{ status: "working" }, "Agent working"],
  [{ status: "answered", answered_version: 5 }, "Done → v5"],
  [{ status: "answered", replies: [{ text: "ok", version: 4 }] }, "Done → v4"],
  [{ status: "needs_clarification" }, "Needs a new selection"],
  [{ replies: [{ text: "ok", version: 6 }] }, "Done → v6"],
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
