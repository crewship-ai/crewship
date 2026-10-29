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
  refetch: vi.fn(),
  version: undefined as number | undefined,
}))

// The library's own data around the detail; the detail itself stays real.
// Per-user preferences as plain state; the hook's server sync is its own test.
vi.mock("@/hooks/use-user-preference", async () => {
  const { useState } = await import("react")
  return { useUserPreference: <T,>(_key: string, initial: T) => { const [value, set] = useState<T>(initial); return [value, set, { ready: true }] as const } }
})
vi.mock("@/hooks/use-page-folders", () => ({ usePageFolders: () => ({ folders: [], loading: false, error: null, supported: true, refresh: () => {}, reread: async () => {} }) }))
vi.mock("@/components/features/chat/artifact-context/new-artifact/use-artifact-capabilities", () => ({
  useArtifactCapabilities: () => ({ projects: true, dashboards: false, slides: false, documents: false, sheets: false, pages: false }),
}))
vi.mock("@/components/layout/secondary-panel", async importOriginal => ({ ...(await importOriginal<typeof import("@/components/layout/secondary-panel")>()), SecondaryPanel: ({ label, children }: { label: string; children: React.ReactNode }) => <aside aria-label={label}>{children}</aside> }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "owner" } } }) }))
vi.mock("@/components/features/chat/artifact-context/viewer/artifact-slides-preview", () => ({ ArtifactSlidesPreview: ({ selectMode }: { selectMode: boolean }) => <div data-testid="library-slides" data-select-mode={String(selectMode)}>Library typed Slides</div> }))
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
    return { data: state.snapshot, error: state.error, isError: Boolean(state.error), isLoading: false, refetch: state.refetch }
  },
  useArtifactProjectMutations: () => ({
    restore: { mutate: vi.fn(), isPending: false },
    share: { mutate: vi.fn(), isPending: false },
    comment: { mutate: state.comment, isPending: false },
  }),
}))

import { toPageView } from "@/hooks/use-pages"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ArtifactsLibrary } from "../artifacts-library"
const withQuery = (ui: React.ReactElement) => <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{ui}</QueryClientProvider>

const saved: ArtifactComment = {
  id: "comment-1", task_id: "task-1", version: 3, path: "index.html",
  selector: "#heading", quote: "Hello", text: "Use the new heading",
}

beforeEach(() => {
  state.pages = [toPageView({ id: "page-1", slug: "launch", name: "Launch", owner: "user/owner", artifact_kind: "project" })]
  state.snapshot = {
    page_id: "page-1", name: "Launch", slug: "launch", can_write: true, can_share: true, can_restore: true, can_send_to_agent: true,
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
  state.refetch.mockReset().mockImplementation(async () => ({ data: state.snapshot, error: state.error }))
  state.comment.mockReset().mockImplementation((_body: unknown, callbacks: { onSuccess: (comment: ArtifactComment) => void }) => callbacks.onSuccess(saved))
})
afterEach(cleanup)

const open = () => render(withQuery(<ArtifactsLibrary workspaceId="workspace-1" item="launch" onSelect={vi.fn()} />))

function compose() {
  fireEvent.click(screen.getByRole("button", { name: "Select" }))
  const frame = screen.getByTitle(/Preview of/) as HTMLIFrameElement
  act(() => window.dispatchEvent(new MessageEvent("message", {
    source: frame.contentWindow,
    data: { type: "crewship-artifact-select", selector: "#heading", quote: "Hello" },
  })))
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: saved.text } })
}

it("provides Preview, History and Comments tabs without losing an unsent anchored comment", () => {
  open()
  expect(screen.getByRole("tab", { name: "Preview", selected: true })).toBeInTheDocument()
  compose()
  fireEvent.click(screen.getByRole("tab", { name: "History" }))
  expect(screen.getByRole("region", { name: "Version history" })).toBeVisible()
  fireEvent.click(screen.getByRole("tab", { name: "Comments" }))
  expect(screen.getByRole("region", { name: "Comment queue" })).toBeVisible()
  fireEvent.click(screen.getByRole("tab", { name: "Preview" }))
  expect(screen.getByRole("textbox", { name: "Comment" })).toHaveValue(saved.text)
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
  fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
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
  fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
  expect(await screen.findByText("The agent did not start")).toBeInTheDocument()
  const current = { ...saved, author: "owner", status: "pending" as const, delivery_retryable: true }
  state.snapshot!.comments = [current]
  fireEvent.click(screen.getByRole("button", { name: "Retry" }))
  expect(await screen.findByText("Comment sent to the agent")).toBeInTheDocument()
  expect(state.refetch).toHaveBeenCalledTimes(1)
  expect(state.dispatch).toHaveBeenNthCalledWith(1, saved)
  expect(state.dispatch).toHaveBeenNthCalledWith(2, current)
  expect(state.comment).toHaveBeenCalledTimes(1)
})

it.each([false, undefined])("refuses a library Retry when fresh delivery eligibility is %s", async deliveryRetryable => {
  state.dispatch.mockResolvedValue(false)
  open()
  compose()
  fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
  expect(await screen.findByText("The agent did not start")).toBeInTheDocument()
  state.snapshot!.comments = [{ ...saved, author: "owner", status: "pending", delivery_retryable: deliveryRetryable }]
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Retry" })) })
  expect(state.refetch).toHaveBeenCalledTimes(1)
  expect(state.dispatch).toHaveBeenCalledTimes(1)
  expect(state.comment).toHaveBeenCalledTimes(1)
  expect(screen.queryByText("Comment sent to the agent")).toBeNull()
})

it("never reports agent delivery when the server provides no source chat", async () => {
  state.snapshot!.origin = null
  open()
  compose()
  fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
  expect(await screen.findByText("Comment saved for the agent")).toBeInTheDocument()
  expect(state.dispatch).not.toHaveBeenCalled()
  expect(screen.queryByText("Comment sent to the agent")).toBeNull()
})

it.each(["true", "false"])("routes explicit server-typed Slides with flag %s while keeping a reader inert", flag => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACT_SLIDES", flag)
  try {
    state.pages = [toPageView({ id: "page-1", slug: "launch", name: "Launch", owner: "user/owner", artifact_kind: "slides", artifact_shared_version: 2 })]
    state.snapshot!.artifact_kind = "slides"
    state.snapshot!.can_write = false
    state.snapshot!.versions = []
    state.snapshot!.manifest.version = 2
    state.snapshot!.manifest.files.push({ path: "deck.json", media_type: "application/json", content: '{"schema_version":1,"order":["intro"]}' })
    open()
    if (flag === "true") {
      expect(screen.getByTestId("library-slides")).toHaveAttribute("data-select-mode", "false")
      expect(screen.queryByRole("button", { name: "Select" })).toBeNull()
      expect(screen.queryByRole("textbox", { name: "Comment" })).toBeNull()
    } else {
      expect(screen.queryByTestId("library-slides")).toBeNull()
      expect(screen.getByText("Its viewer is not switched on in this workspace.")).toBeInTheDocument()
      expect(screen.queryByRole("link", { name: "Open in Pages" })).toBeNull()
    }
  } finally { vi.unstubAllEnvs() }
})
