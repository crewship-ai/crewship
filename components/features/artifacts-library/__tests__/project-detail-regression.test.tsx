import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { ArtifactComment, ArtifactProjectSnapshot } from "@/components/features/chat/artifact-context/projects/types"
import type { PageView } from "@/hooks/use-pages"

const state = vi.hoisted(() => ({
  pages: [] as PageView[],
  snapshot: undefined as ArtifactProjectSnapshot | undefined,
  error: null as Error | null,
  dispatch: vi.fn(),
  dispatchHook: vi.fn(),
  comment: vi.fn(),
  version: undefined as number | undefined,
}))

vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "owner" } } }) }))
vi.mock("@/hooks/use-pages", async importOriginal => ({
  ...(await importOriginal<typeof import("@/hooks/use-pages")>()),
  usePages: () => ({ pages: state.pages, loading: false, error: null, refresh: vi.fn() }),
}))
vi.mock("@/hooks/use-artifact-agent-dispatch", () => ({
  useArtifactAgentDispatch: (...args: unknown[]) => {
    state.dispatchHook(...args)
    return { dispatch: state.dispatch, ready: true }
  },
}))
vi.mock("@/components/features/chat/artifact-context/projects/use-artifact-project-data", () => ({
  useArtifactProjectsAvailable: () => true,
  useArtifactProjectSnapshot: (_workspace: string, _slug: string | null, version?: number) => {
    state.version = version
    return { data: state.snapshot, error: state.error, isError: Boolean(state.error), isLoading: false, refetch: vi.fn() }
  },
  useArtifactProjectMutations: () => ({
    restore: { mutate: vi.fn(), isPending: false },
    share: { mutate: vi.fn(), isPending: false },
    comment: { mutate: state.comment, isPending: false },
  }),
}))

import { toPageView } from "@/hooks/use-pages"
import { ArtifactsLibrary } from "../artifacts-library"

const saved: ArtifactComment = {
  id: "comment-1", task_id: "task-1", version: 3, path: "index.html",
  selector: "#heading", quote: "Hello", text: "Use the new heading",
}

beforeEach(() => {
  state.pages = [toPageView({ id: "page-1", slug: "launch", name: "Launch", owner: "user/owner", artifact_kind: "project" })]
  state.snapshot = {
    page_id: "page-1", name: "Launch", slug: "launch", can_write: true,
    origin: { chat_id: "source-chat", agent_id: "source-agent" },
    latest_version: 3, shared_version: 2,
    manifest: { version: 3, entrypoint: "index.html", files: [{ path: "index.html", media_type: "text/html", content: '<body><h1 id="heading">Hello</h1></body>' }] },
    versions: [{ version: 3, message: "Latest changes" }, { version: 2, message: "Shared changes" }],
    comments: [],
  }
  state.error = null
  state.version = undefined
  state.dispatch.mockReset().mockResolvedValue(true)
  state.dispatchHook.mockReset()
  state.comment.mockReset().mockImplementation((_body: unknown, callbacks: { onSuccess: (comment: ArtifactComment) => void }) => callbacks.onSuccess(saved))
})
afterEach(cleanup)

const open = () => render(<ArtifactsLibrary workspaceId="workspace-1" item="launch" onSelect={vi.fn()} />)

function compose() {
  fireEvent.click(screen.getByRole("button", { name: "Select", exact: true }))
  const frame = screen.getByTitle(/Preview of/) as HTMLIFrameElement
  act(() => window.dispatchEvent(new MessageEvent("message", {
    source: frame.contentWindow,
    data: { type: "crewship-artifact-select", selector: "#heading", quote: "Hello" },
  })))
  fireEvent.change(screen.getByRole("textbox", { name: "Comment", exact: true }), { target: { value: saved.text } })
}

it("provides Preview, History and Comments tabs without losing an unsent anchored comment", () => {
  open()
  expect(screen.getByRole("tab", { name: "Preview", selected: true })).toBeInTheDocument()
  compose()
  fireEvent.click(screen.getByRole("tab", { name: "History" }))
  expect(screen.getByRole("region", { name: "Version history" })).toBeVisible()
  fireEvent.click(screen.getByRole("tab", { name: "Comments", exact: true }))
  expect(screen.getByRole("region", { name: "Comment queue" })).toBeVisible()
  fireEvent.click(screen.getByRole("tab", { name: "Preview" }))
  expect(screen.getByRole("textbox", { name: "Comment", exact: true })).toHaveValue(saved.text)
  expect(state.version).toBeUndefined()
  expect(state.comment).not.toHaveBeenCalled()
})

it("supports arrow keys and Home and End between detail tabs", () => {
  open()
  const preview = screen.getByRole("tab", { name: "Preview" })
  preview.focus()
  fireEvent.keyDown(preview, { key: "ArrowRight" })
  expect(screen.getByRole("tab", { name: "History", selected: true })).toHaveFocus()
  fireEvent.keyDown(document.activeElement!, { key: "End" })
  expect(screen.getByRole("tab", { name: "Comments", selected: true })).toHaveFocus()
  fireEvent.keyDown(document.activeElement!, { key: "Home" })
  expect(screen.getByRole("tab", { name: "Preview", selected: true })).toHaveFocus()
})

it("starts the source agent only after saving the exact library comment", async () => {
  open()
  compose()
  fireEvent.click(screen.getByRole("button", { name: "Send to agent", exact: true }))
  expect(state.comment).toHaveBeenCalledWith({
    version: 3, path: "index.html", selector: "#heading", quote: "Hello", text: saved.text, send_to_agent: true,
  }, expect.anything())
  await waitFor(() => expect(state.dispatch).toHaveBeenCalledWith(saved))
  expect(state.dispatchHook).toHaveBeenCalledWith("workspace-1", "page-1", state.snapshot!.origin, true)
  expect(await screen.findByText("Comment sent to the agent")).toBeInTheDocument()
})

it("retries delivery of the saved task without creating a second comment", async () => {
  state.dispatch.mockResolvedValueOnce(false).mockResolvedValueOnce(true)
  open()
  compose()
  fireEvent.click(screen.getByRole("button", { name: "Send to agent", exact: true }))
  expect(await screen.findByText("The agent did not start")).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Retry", exact: true }))
  expect(await screen.findByText("Comment sent to the agent")).toBeInTheDocument()
  expect(state.dispatch).toHaveBeenNthCalledWith(1, saved)
  expect(state.dispatch).toHaveBeenNthCalledWith(2, saved)
  expect(state.comment).toHaveBeenCalledTimes(1)
})

it("never reports agent delivery when the server provides no source chat", async () => {
  state.snapshot!.origin = null
  open()
  compose()
  fireEvent.click(screen.getByRole("button", { name: "Send to agent", exact: true }))
  expect(await screen.findByText("Comment saved for the agent")).toBeInTheDocument()
  expect(state.dispatch).not.toHaveBeenCalled()
  expect(screen.queryByText("Comment sent to the agent")).toBeNull()
})
