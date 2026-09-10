import React from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"

import { NO_PAGE_CAPABILITIES, type DefinitionDiff, type PublishConflictWire, type ReviewSnapshotWire, type SourceDiff } from "@/lib/pages/editor-contract"
import type { EditorSectionProps } from "@/components/features/pages/editor/section-props"

/**
 * Everything this screen derives from is mocked, on purpose: the comparators
 * (stream S2) and the review endpoint (stream S3) have their own tests, and
 * what is under test here is the screen's *judgement* — what it refuses to
 * claim, and what clears a consent.
 */

const state = vi.hoisted(() => ({
  review: null as unknown as Record<string, unknown>,
  definitionDiff: null as unknown as DefinitionDiff,
  sourceDiff: null as unknown as SourceDiff,
  preview: null as unknown as Record<string, unknown>,
  frameProps: [] as Array<Record<string, unknown>>,
  hidden: [] as string[],
}))

vi.mock("@/hooks/use-page-review", () => ({
  usePageReview: () => state.review,
  liveDefinitionFromPage: (page: unknown) => page,
  candidateDefinitionForComparison: (definition: unknown) => definition,
  hiddenPanelIds: () => state.hidden,
  publishConflictOf: () => null,
}))
vi.mock("@/hooks/use-page-preview", () => ({ usePagePreview: () => state.preview }))
vi.mock("@/lib/pages/definition-diff", () => ({ compareDefinitions: () => state.definitionDiff }))
vi.mock("@/lib/pages/source-diff", () => ({ compareSources: () => state.sourceDiff }))
vi.mock("@/components/features/pages/page-preview", () => ({
  PagePreviewFrame: (props: Record<string, unknown>) => {
    state.frameProps.push(props)
    return <div data-testid="preview-frame" />
  },
}))

import { EditorApplicationReview } from "@/components/features/pages/editor/application-review"

const baseSnapshot: ReviewSnapshotWire = {
  issued_at: "2026-09-10T12:03:00Z",
  candidate: {
    revision: 7,
    git_commit: "c7",
    source_digest: "sha256:cand",
    created_at: "2026-09-10T12:03:00Z",
    actor: { kind: "agent", id: "ag_demo" },
    build: { id: "build-7", state: "ready", artifact_digest: "sha256:art" },
  },
  baseline: {
    publication_version: 3,
    published: true,
    definition_digest: "sha256:live-def",
    source_revision: 4,
    git_commit: "c4",
    source_available: true,
    source_unavailable_reason: null,
  },
  routines: [{ routine: "ops-restart", published_digest: "sha256:old", current_digest: "sha256:new", state: "changed" }],
  capabilities: { may_edit_spec: true, may_publish: true },
  blockers: [],
  initial_publication: false,
}

function clone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T
}

function setReview(snapshot: ReviewSnapshotWire, overrides: Record<string, unknown> = {}) {
  state.review = {
    snapshot: { data: snapshot, isError: false, error: null },
    candidate: { data: { git_commit: "c7", revision: 7, digest: "sha256:cand", definition: { kind: "Page" }, project: { files: [] } } },
    baseline: { data: { git_commit: "c4", revision: 4, digest: "sha256:base", definition: { kind: "Page" }, project: { files: [] } } },
    baselineUnavailable: null,
    candidateMoved: false,
    publish: { mutate: vi.fn(), isPending: false, isError: false, isSuccess: false, error: null, data: null },
    conflict: null as PublishConflictWire | null,
    refresh: vi.fn(),
    ...overrides,
  }
}

const props: EditorSectionProps = {
  workspaceId: "ws1",
  slug: "operations-lab",
  page: { name: "Operations Lab", slug: "operations-lab", panels: [] } as EditorSectionProps["page"],
  capabilities: { ...NO_PAGE_CAPABILITIES, loaded: true, hasApplication: true, mayPublishApplication: true },
  onNavigate: vi.fn(),
  pane: "section",
  onPaneChange: vi.fn(),
  onDirtyChange: vi.fn(),
}

beforeEach(() => {
  state.frameProps = []
  state.hidden = []
  state.definitionDiff = {
    changes: [
      { kind: "action-added", tone: "add", actionId: "restart", summary: "Add action restart → routine ops-restart" },
      { kind: "panel-removed", tone: "remove", panelId: "memory", summary: "Remove panel memory from the live Page definition" },
    ],
    unmodelled: [],
    raw: "--- live\n+++ candidate\n",
    identical: false,
  }
  state.sourceDiff = {
    files: [
      { path: "src/App.tsx", status: "modified", added: 42, removed: 8, binary: false, truncated: false, lines: [{ kind: "add", text: "<RestartCollector />", oldLine: null, newLine: 12 }] },
      { path: "src/Restart.tsx", status: "added", added: 61, removed: 0, binary: false, truncated: false, lines: [] },
    ],
    filesAdded: 1,
    filesModified: 1,
    filesRemoved: 0,
    truncated: false,
  }
  state.preview = {
    query: { data: { revision: 7, runtime_url: "https://pages.example.net/boot", build: { id: "build-7", source_revision: 7, state: "ready" }, artifact: { format: "crewship-page-preview/v1", javascript: "", css: "", toolchain: "t" } }, isError: false, error: null },
    build: { mutate: vi.fn(), isPending: false, error: null },
  }
  setReview(clone(baseSnapshot))
})
afterEach(cleanup)

function publishButton() {
  return screen.getByRole("button", { name: "Publish application" }) as HTMLButtonElement
}
function consentBox() {
  return screen.getByRole("checkbox") as HTMLInputElement
}

describe("EditorApplicationReview", () => {
  it("renders all seven sections and holds Publish until consent", () => {
    render(<EditorApplicationReview {...props} />)
    for (const heading of ["Review application changes", "Definition changes", "Execution dependencies", "Source changes", "Candidate build", "Publish", "Close without publishing"]) {
      expect(screen.getByRole("heading", { name: heading })).toBeTruthy()
    }
    expect(publishButton().disabled).toBe(true)
    fireEvent.click(consentBox())
    expect(publishButton().disabled).toBe(false)
    fireEvent.click(publishButton())
    expect(state.review.publish).toHaveProperty("mutate")
    expect((state.review.publish as { mutate: ReturnType<typeof vi.fn> }).mutate).toHaveBeenCalled()
  })

  it("states both comparison bases separately and never as one series", () => {
    render(<EditorApplicationReview {...props} />)
    expect(screen.getByText(/Source is compared with live publication 3.*definition is compared with the current live Page/s)).toBeTruthy()
    expect(screen.getByText(/different series and cannot be compared/)).toBeTruthy()
  })

  it("shows the provable author only: kind and raw id, no invented name and no chat link", () => {
    render(<EditorApplicationReview {...props} />)
    expect(screen.getByText(/saved by agent ag_demo/)).toBeTruthy()
    expect(screen.queryByRole("link")).toBeNull()
  })

  it("makes consent impossible while a blocker is present and lists the blockers as sentences", () => {
    const snapshot = clone(baseSnapshot)
    snapshot.blockers = [{ code: "build_failed", message: "This candidate did not build, so there is nothing to publish." }]
    setReview(snapshot)
    render(<EditorApplicationReview {...props} />)
    expect(consentBox().disabled).toBe(true)
    expect(publishButton().disabled).toBe(true)
    expect(screen.getByText("This candidate did not build, so there is nothing to publish.")).toBeTruthy()
  })

  it("clears consent visibly when the candidate revision changes", () => {
    const { rerender } = render(<EditorApplicationReview {...props} />)
    fireEvent.click(consentBox())
    expect(consentBox().checked).toBe(true)

    const moved = clone(baseSnapshot)
    moved.candidate!.revision = 8
    moved.candidate!.git_commit = "c8"
    setReview(moved)
    rerender(<EditorApplicationReview {...props} />)

    expect(consentBox().checked).toBe(false)
    expect(screen.getByText(/The candidate changed while this review was open/)).toBeTruthy()
  })

  it("clears consent visibly when a new build lands", () => {
    const { rerender } = render(<EditorApplicationReview {...props} />)
    fireEvent.click(consentBox())

    const rebuilt = clone(baseSnapshot)
    rebuilt.candidate!.build = { id: "build-8", state: "ready", artifact_digest: "sha256:art2" }
    setReview(rebuilt)
    rerender(<EditorApplicationReview {...props} />)

    expect(consentBox().checked).toBe(false)
    expect(screen.getByText(/A new build of this candidate landed/)).toBeTruthy()
  })

  it("clears consent visibly when a baseline moves", () => {
    const { rerender } = render(<EditorApplicationReview {...props} />)
    fireEvent.click(consentBox())

    const moved = clone(baseSnapshot)
    moved.baseline = { ...moved.baseline, definition_digest: "sha256:live-def-2" }
    setReview(moved)
    rerender(<EditorApplicationReview {...props} />)

    expect(consentBox().checked).toBe(false)
    expect(screen.getByText(/A base you were comparing against moved/)).toBeTruthy()
  })

  it("clears consent when the reviewer comes back from the preview", () => {
    const { rerender } = render(<EditorApplicationReview {...props} />)
    fireEvent.click(consentBox())
    rerender(<EditorApplicationReview {...props} pane="preview" />)
    expect(screen.getByRole("heading", { name: "Candidate preview" })).toBeTruthy()
    rerender(<EditorApplicationReview {...props} pane="section" />)
    expect(consentBox().checked).toBe(false)
    expect(screen.getByText(/You returned from the preview/)).toBeTruthy()
  })

  it("blocks publishing on a moved base, and a fresh build does not clear it", () => {
    const snapshot = clone(baseSnapshot)
    snapshot.blockers = [{ code: "definition_moved", message: "A different user changed the Services producer while you were reviewing." }]
    setReview(snapshot)
    const { rerender } = render(<EditorApplicationReview {...props} />)
    expect(screen.getByRole("alert").textContent).toMatch(/A base you reviewed moved/)
    expect(consentBox().disabled).toBe(true)
    expect(publishButton().disabled).toBe(true)

    const rebuilt = clone(snapshot)
    rebuilt.candidate!.build = { id: "build-9", state: "ready", artifact_digest: "sha256:art3" }
    setReview(rebuilt)
    rerender(<EditorApplicationReview {...props} />)

    expect(consentBox().disabled).toBe(true)
    expect(publishButton().disabled).toBe(true)
    expect(screen.getByText(/Building the candidate again does not clear this/)).toBeTruthy()
  })

  it("renders a missing baseline as comparison unavailable, with no empty diff and no publishing", () => {
    setReview(clone(baseSnapshot), {
      baselineUnavailable: "The retained source for publication 3 was pruned on 2026-08-01.",
      baseline: { data: undefined },
    })
    render(<EditorApplicationReview {...props} />)
    expect(screen.getByText("Comparison unavailable")).toBeTruthy()
    // Twice on purpose: once as the reason under Source changes, once as a
    // blocker sentence next to the disabled control it explains.
    expect(screen.getAllByText(/pruned on 2026-08-01/).length).toBe(2)
    expect(screen.getByText(/This does not mean there are no changes/)).toBeTruthy()
    expect(screen.queryByLabelText("Changed files")).toBeNull()
    expect(screen.queryByText(/No source file differs/)).toBeNull()
    expect(publishButton().disabled).toBe(true)
    expect(consentBox().disabled).toBe(true)
  })

  it("presents an initial publication as an entirely new candidate, never as no changes", () => {
    const snapshot = clone(baseSnapshot)
    snapshot.initial_publication = true
    snapshot.baseline = { ...snapshot.baseline, publication_version: 0, published: false, source_revision: null, git_commit: null, source_available: false, source_unavailable_reason: null }
    setReview(snapshot, { baseline: { data: undefined } })
    render(<EditorApplicationReview {...props} />)
    expect(screen.getByText(/Initial publication\./)).toBeTruthy()
    expect(screen.getByText(/All application source is new/)).toBeTruthy()
    expect(screen.queryByText("Comparison unavailable")).toBeNull()
    expect(screen.queryByText(/No source file differs/)).toBeNull()
  })

  it("does not treat a withdrawn publication's receipt as a live basis", () => {
    const snapshot = clone(baseSnapshot)
    snapshot.baseline = { ...snapshot.baseline, published: false }
    setReview(snapshot)
    render(<EditorApplicationReview {...props} />)
    expect(screen.getByText(/No live application: the last publication was withdrawn/)).toBeTruthy()
    expect(screen.getByText(/history, not a live basis/)).toBeTruthy()
  })

  it("renders an unknown routine hash as unknown and never as unchanged", () => {
    const snapshot = clone(baseSnapshot)
    snapshot.routines = [
      { routine: "ops-collect", published_digest: null, current_digest: "sha256:new", state: "unknown" },
      { routine: "ops-quiet", published_digest: "sha256:a", current_digest: "sha256:a", state: "unchanged" },
    ]
    setReview(snapshot)
    const { container } = render(<EditorApplicationReview {...props} />)
    const unknown = container.querySelector('[data-routine-state="unknown"]')!
    expect(within(unknown as HTMLElement).getByText(/earlier definition hash unavailable/)).toBeTruthy()
    expect(unknown.textContent).not.toMatch(/unchanged/i)
    expect(unknown.textContent).toMatch(/not reviewed/)
  })

  it("warns about a changed routine here, at the moment of approval", () => {
    render(<EditorApplicationReview {...props} />)
    const changed = screen.getByText("Routine definition changed: ops-restart")
    expect(changed).toBeTruthy()
    expect(changed.parentElement!.textContent).toMatch(/does not pin the scripts a routine calls/)
  })

  it("has no Reject button and says plainly that no message reaches the author", () => {
    render(<EditorApplicationReview {...props} />)
    expect(screen.queryByRole("button", { name: /reject/i })).toBeNull()
    expect(screen.getByText(/No message is sent to its author\. To disagree, tell the agent in chat\./)).toBeTruthy()
  })

  it("retains the draft and says so when the review is closed without publishing", () => {
    render(<EditorApplicationReview {...props} />)
    fireEvent.click(screen.getByRole("button", { name: "Close without publishing" }))
    expect(screen.getByRole("heading", { name: "Review closed without publishing" })).toBeTruthy()
    expect(screen.getByRole("status").textContent).toMatch(/No message was sent to its author/)
    expect(screen.queryByRole("button", { name: "Publish application" })).toBeNull()
  })

  it("names the routines from a publish 409 and clears the consent", () => {
    const { rerender } = render(<EditorApplicationReview {...props} />)
    fireEvent.click(consentBox())

    setReview(clone(baseSnapshot), {
      conflict: { error: "a routine changed", conflict: "routines", routines: ["ops-restart"] } as PublishConflictWire,
      publish: { mutate: vi.fn(), isPending: false, isError: true, isSuccess: false, error: new Error("a routine changed"), data: null },
    })
    rerender(<EditorApplicationReview {...props} />)

    expect(consentBox().checked).toBe(false)
    expect(consentBox().disabled).toBe(true)
    expect(screen.getByRole("status").textContent).toMatch(/Re-review: ops-restart\./)
    expect(screen.getByRole("alert").textContent).toMatch(/A routine this application calls changed/)
    expect(publishButton().disabled).toBe(true)
  })


  it("excludes panels this viewer cannot read from the comparison and says which", () => {
    state.hidden = ["memory"]
    render(<EditorApplicationReview {...props} />)
    expect(screen.getByText(/1 panel on this Page is not visible to you and is excluded from this comparison \(memory\)/)).toBeTruthy()
    expect(screen.getByText(/This review does not cover it\./)).toBeTruthy()
  })

  it("names unmodelled definition fields and shows the raw diff instead of implying they are unchanged", () => {
    state.definitionDiff = { ...state.definitionDiff, unmodelled: ["spec.panels[0].experimental"], raw: "@@ raw @@" }
    render(<EditorApplicationReview {...props} />)
    expect(screen.getByText(/Fields this comparison does not model: spec\.panels\[0\]\.experimental/)).toBeTruthy()
    expect(screen.getByText(/must not be read as unchanged/)).toBeTruthy()
    expect(screen.getByText("@@ raw @@")).toBeTruthy()
  })

  it("describes a binary file rather than rendering it, and admits a truncated diff", () => {
    state.sourceDiff = {
      files: [{ path: "assets/logo.png", status: "modified", added: 0, removed: 0, binary: true, truncated: false, lines: [] }],
      filesAdded: 0,
      filesModified: 1,
      filesRemoved: 0,
      truncated: true,
    }
    render(<EditorApplicationReview {...props} />)
    expect(screen.getByText(/binary or undecodable file/)).toBeTruthy()
    expect(screen.getByText(/Reviewing what is shown is not the same as reviewing the whole change/)).toBeTruthy()
  })

  it("renders source diffs as text and never as markup", () => {
    state.sourceDiff = {
      files: [{ path: "src/App.tsx", status: "modified", added: 1, removed: 0, binary: false, truncated: false, lines: [{ kind: "add", text: "<img src=x onerror=alert(1)>", oldLine: null, newLine: 1 }] }],
      filesAdded: 0,
      filesModified: 1,
      filesRemoved: 0,
      truncated: false,
    }
    const { container } = render(<EditorApplicationReview {...props} />)
    expect(screen.getByText(/onerror=alert\(1\)/)).toBeTruthy()
    expect(container.querySelector("img")).toBeNull()
  })

  it("shows the four build states and does not call Ready a verification", () => {
    const unbuilt = clone(baseSnapshot)
    unbuilt.candidate!.build = null
    setReview(unbuilt)
    const { rerender } = render(<EditorApplicationReview {...props} />)
    expect(screen.getByText("Not built")).toBeTruthy()

    const running = clone(baseSnapshot)
    running.candidate!.build = { id: "b", state: "running", artifact_digest: "" }
    setReview(running)
    rerender(<EditorApplicationReview {...props} />)
    expect(screen.getByText("Building…")).toBeTruthy()

    const failed = clone(baseSnapshot)
    failed.candidate!.build = { id: "b", state: "failed", artifact_digest: "", error: "src/Restart.tsx: unresolved import" }
    setReview(failed)
    rerender(<EditorApplicationReview {...props} />)
    expect(screen.getByText(/Failed — src\/Restart\.tsx: unresolved import/)).toBeTruthy()

    setReview(clone(baseSnapshot))
    rerender(<EditorApplicationReview {...props} />)
    expect(screen.getByText(/Ready — a build of draft 7 exists/)).toBeTruthy()
    expect(screen.getByText(/It is not automated verification of the actions/)).toBeTruthy()
  })

  it("enters the preview as its own pane and mounts the frame without an action handler", () => {
    const { rerender } = render(<EditorApplicationReview {...props} />)
    fireEvent.click(screen.getByRole("button", { name: "Open preview" }))
    expect(props.onPaneChange).toHaveBeenCalledWith("preview")

    rerender(<EditorApplicationReview {...props} pane="preview" />)
    expect(screen.getByTestId("preview-frame")).toBeTruthy()
    expect(state.frameProps).toHaveLength(1)
    expect("onRequest" in state.frameProps[0]).toBe(false)
    expect(screen.getByText(/does not run the application's actions/)).toBeTruthy()
    expect(screen.getByText(/not a test of the actions/)).toBeTruthy()
  })

  it("stops the preview from a host control outside the frame", () => {
    render(<EditorApplicationReview {...props} pane="preview" />)
    fireEvent.click(screen.getByRole("button", { name: "Stop preview" }))
    expect(screen.queryByTestId("preview-frame")).toBeNull()
    expect(screen.getByText("Preview stopped.")).toBeTruthy()
  })

  it("leaves the preview by an unmistakable return control", () => {
    render(<EditorApplicationReview {...props} pane="preview" />)
    fireEvent.click(screen.getByRole("button", { name: "Back to review" }))
    expect(props.onPaneChange).toHaveBeenCalledWith("section")
  })
})
