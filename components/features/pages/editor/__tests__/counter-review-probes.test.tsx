import React from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"

import { NO_PAGE_CAPABILITIES, type DefinitionDiff, type PublishConflictWire, type ReviewSnapshotWire, type SourceDiff } from "@/lib/pages/editor-contract"
import type { EditorSectionProps } from "@/components/features/pages/editor/section-props"

/**
 * The three adversarial probes from the 2026-09-11 counter-review (R1, R2),
 * adopted verbatim as acceptance criteria. All three failed against
 * `93246b89`; they are kept here so the two holes stay closed.
 *
 * One thing about this fixture is load-bearing and easy to undo by accident:
 * its `baseline` carries NO `definition`. That is the state R1.5 is about — a
 * snapshot that arrived without the authorized live document — and the screen
 * must refuse to offer consent at all in it. When `baseline.definition` lands
 * in `editor-contract.ts` as a required field, make it `null` here. Filling it
 * in with a document turns these into a different test: with the document and
 * the digest both present and corresponding, re-consenting after a moved
 * baseline is honest, and the last assertion of the third probe would then be
 * asserting the wrong thing.
 */

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
  routines: [{ routine: "ops-restart", published_digest: "sha256:old", current_digest: "sha256:new", state: "changed", in_candidate: true }],
  capabilities: { may_edit_spec: true, may_publish: true },
  blockers: [],
  initial_publication: false,
}

/**
 * A fixture is not a wire record, and the type says so.
 *
 * Every field of `ReviewSnapshotWire` is `readonly` on purpose: it models what
 * the server sent, and nothing in the product may write to it. A deep clone
 * built here is a different thing — a local fixture nobody has sent anywhere —
 * so `clone` hands back a genuinely mutable view of it, and each test says
 * "start from the base and change exactly this".
 *
 * This is the honest encoding rather than a way round the contract: building
 * a variant needs no cast of any kind, and `editor-contract.ts` keeps every
 * `readonly` it has. Mutable is assignable to readonly, so `setReview` still
 * takes the real wire type and the product code still sees an immutable
 * snapshot. (The `as unknown as` calls elsewhere in this file are unrelated —
 * `vi.hoisted` null-initialisers and a happy-dom probe.)
 *
 * Chosen over a `snapshot(overrides)` spread builder because the variants in
 * this file reach two and three levels deep and one of them sets
 * `candidate: null` — a deep-partial merge would need an "explicitly null vs
 * absent" rule, which is more machinery than the tests it serves.
 */
type DeepMutable<T> = T extends readonly (infer U)[] ? DeepMutable<U>[] : T extends object ? { -readonly [K in keyof T]: DeepMutable<T[K]> } : T

function clone<T>(value: T): DeepMutable<T> {
  return JSON.parse(JSON.stringify(value)) as DeepMutable<T>
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
  onLeaveEditor: vi.fn(),
  onPageDeleted: vi.fn(),
  onDirtyChange: vi.fn(),
}

beforeEach(() => {
  vi.clearAllMocks()
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
    baselineMissing: false,
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


describe("independent review adversarial probes", () => {
 it.each(["loading", "error"])("must not publish with candidate source %s", phase => {
  setReview(clone(baseSnapshot), {candidate: {data: undefined, isPending: phase === "loading", isError: phase === "error", error: phase === "error" ? new Error("Source unavailable") : null}})
  render(<EditorApplicationReview {...props} />)
  fireEvent.click(consentBox())
  expect(publishButton().disabled).toBe(true)
 })
 it("must not re-consent while fresh review snapshot has an unmatched older live detail", () => {
  const {rerender}=render(<EditorApplicationReview {...props} />)
  fireEvent.click(consentBox())
  const moved=clone(baseSnapshot)
  moved.baseline.definition_digest="b".repeat(64)
  setReview(moved)
  rerender(<EditorApplicationReview {...props} />)
  expect(consentBox().checked).toBe(false)
  fireEvent.click(consentBox())
  expect(publishButton().disabled).toBe(true)
 })
})
