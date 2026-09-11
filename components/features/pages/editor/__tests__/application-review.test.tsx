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
  comparedWith: [] as unknown[],
}))

vi.mock("@/hooks/use-page-review", () => ({ usePageReview: () => state.review }))
vi.mock("@/hooks/use-page-preview", () => ({ usePagePreview: () => state.preview }))
vi.mock("@/lib/pages/definition-diff", () => ({
  compareDefinitions: (live: unknown, candidate: unknown) => {
    state.comparedWith.push(live, candidate)
    return state.definitionDiff
  },
}))
vi.mock("@/lib/pages/source-diff", () => ({ compareSources: () => state.sourceDiff }))
vi.mock("@/components/features/pages/page-preview", () => ({
  PagePreviewFrame: (props: Record<string, unknown>) => {
    state.frameProps.push(props)
    return <div data-testid="preview-frame" />
  },
}))

import { EditorApplicationReview } from "@/components/features/pages/editor/application-review"

/**
 * The two documents the review snapshot carries.
 *
 * Both are `pages.Document`s read by the server in the same statement as
 * `definition_digest`, filtered by one authorization rule — so what this
 * screen renders and what the publish fence attests to are one read. Neither
 * the Page detail (`props.page`) nor the draft's own `GET .../project` is
 * this: those are separate endpoints on separate cache entries, and the tests
 * below that move one without the other exist because they could disagree.
 */
const liveDefinition = {
  apiVersion: "crewship/v1",
  kind: "Page",
  metadata: { name: "Operations Lab", slug: "operations-lab", description: "Container fleet" },
  spec: { panels: [{ id: "services", schema: "status.v1", owner: "crew/ops", producer: "routine/nightly", sla: "5m" }] },
}

const candidateDefinition = {
  apiVersion: "crewship/v1",
  kind: "Page",
  metadata: { name: "Operations Lab", slug: "operations-lab", description: "Container fleet" },
  spec: {
    panels: [
      { id: "services", schema: "status.v1", owner: "crew/ops", producer: "routine/nightly", sla: "90.5s", actions: [{ id: "restart", kind: "call", routine: "ops-restart" }] },
    ],
  },
}

const baseSnapshot: ReviewSnapshotWire = {
  issued_at: "2026-09-10T12:03:00Z",
  candidate: {
    revision: 7,
    git_commit: "c7",
    source_digest: "sha256:cand",
    created_at: "2026-09-10T12:03:00Z",
    definition: candidateDefinition,
    actor: { kind: "agent", id: "ag_demo" },
    build: { id: "build-7", state: "ready", artifact_digest: "sha256:art" },
  },
  baseline: {
    publication_version: 3,
    published: true,
    definition_digest: "sha256:live-def",
    definition: liveDefinition,
    excluded_panels: 0,
    withheld_changed: false,
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
  state.comparedWith = []
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

  it("shows an absent timestamp as unknown rather than as an epoch", () => {
    const snapshot = clone(baseSnapshot)
    // A draft with no matching revision row sends "". Rendering it raw leaves a
    // dangling separator; parsing it would produce 1970 or "Invalid Date".
    snapshot.candidate!.created_at = ""
    setReview(snapshot)
    render(<EditorApplicationReview {...props} />)
    expect(screen.getByText(/saved at an unknown time/)).toBeTruthy()
    expect(screen.queryByText(/1970|Invalid Date/)).toBeNull()
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
      { routine: "ops-collect", published_digest: null, current_digest: "sha256:new", state: "unknown", in_candidate: true },
      { routine: "ops-quiet", published_digest: "sha256:a", current_digest: "sha256:a", state: "unchanged", in_candidate: true },
    ]
    setReview(snapshot)
    const { container } = render(<EditorApplicationReview {...props} />)
    const unknown = container.querySelector('[data-routine-state="unknown"]')!
    expect(within(unknown as HTMLElement).getByText(/earlier definition hash unavailable/)).toBeTruthy()
    expect(unknown.textContent).not.toMatch(/unchanged/i)
    expect(unknown.textContent).toMatch(/not reviewed/)
  })

  it("says on the row when a routine is not covered by the publish check", () => {
    const snapshot = clone(baseSnapshot)
    snapshot.routines = [
      { routine: "ops-collect", published_digest: null, current_digest: null, state: "unknown", in_candidate: true },
      { routine: "ops-restart", published_digest: null, current_digest: "sha256:new", state: "unknown", in_candidate: true },
    ]
    setReview(snapshot)
    const { container } = render(<EditorApplicationReview {...props} />)
    const rows = container.querySelectorAll('[data-routine-state="unknown"]')
    expect(rows).toHaveLength(2)
    // No current hash: the fence cannot carry it, and the row says exactly that.
    expect(rows[0].textContent).toMatch(/not covered by the publish check/)
    expect(rows[0].textContent).toMatch(/publishing will not be refused if this routine changes/)
    // A hash exists, so this one IS fenced — claiming otherwise would be a
    // second untruth in the opposite direction.
    expect(rows[1].textContent).not.toMatch(/not covered by the publish check/)
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

  it("leaves the editor on Close without publishing, having already said no message reaches the author", () => {
    render(<EditorApplicationReview {...props} />)
    const close = screen.getByRole("button", { name: "Close without publishing" })
    // The promise is on screen at the moment of the click, not in a panel that
    // replaces the review afterwards — there is no afterwards, the editor closes.
    const section = close.closest("div")!
    expect(section.textContent).toMatch(/The draft is retained and the live application does not change\. No message is sent to its author\. To disagree, tell the agent in chat\./)
    fireEvent.click(close)
    expect(props.onLeaveEditor).toHaveBeenCalledTimes(1)
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


  it("says how many panels the server withheld, and that this candidate changes none of them", () => {
    // The count comes with the documents it describes, and so does the verdict
    // on whether anything inside the withheld part moved — the server holds
    // both FULL documents and the client holds neither.
    const snapshot = clone(baseSnapshot)
    snapshot.baseline.excluded_panels = 2
    setReview(snapshot)
    render(<EditorApplicationReview {...props} />)
    expect(screen.getByText(/This comparison is partial: 2 panels on this Page are not visible to you\./)).toBeTruthy()
    expect(screen.getByText(/withheld from both documents compared above/)).toBeTruthy()
    expect(screen.getByText(/Nothing this candidate changes is among them, so the comparison above covers the whole of this change/)).toBeTruthy()
    // The old copy claimed a re-pointed panel could slip through invisibly.
    // That case now sets `withheld_changed` and blocks, so the sentence would
    // be false — and a false reassurance is the failure mode of this screen.
    expect(screen.queryByText(/re-pointed between a crew you may read/)).toBeNull()
  })

  it("scopes the consent to what it actually covers when panels are withheld but unchanged", () => {
    const snapshot = clone(baseSnapshot)
    snapshot.baseline.excluded_panels = 2
    setReview(snapshot)
    render(<EditorApplicationReview {...props} />)

    // Available, because the comparison does cover the whole change.
    expect(consentBox().disabled).toBe(false)
    expect(consentBox().closest("label")!.textContent).toMatch(
      /2 panels on this Page are not visible to you and are not changed by this candidate, so this consent covers everything it does change\./,
    )
    fireEvent.click(consentBox())
    expect(publishButton().disabled).toBe(false)
  })

  it("says nothing about withheld panels, and makes no narrower claim, when none were withheld", () => {
    render(<EditorApplicationReview {...props} />)
    expect(screen.queryByText(/not visible to you/)).toBeNull()
    expect(screen.queryByText(/This comparison is partial/)).toBeNull()
    expect(consentBox().closest("label")!.textContent).not.toMatch(/covers everything it does change/)
    // The blanket sentence is the whole claim here, and it is true here.
    expect(consentBox().closest("label")!.textContent).toMatch(/I reviewed this candidate.s code, definition changes and behaviour\./)
  })

  /**
   * What a reader may not see, they may not attest to.
   *
   * These strings stand for the identity of the withheld panel. None of them
   * is anywhere in the snapshot the screen is given — that is the point of
   * withholding — so an assertion that they are absent is really an assertion
   * that the screen invents nothing and echoes nothing back from a place it
   * should not be reading. They are checked against the WHOLE rendered
   * document, not just the blocker.
   */
  const PROTECTED = ["billing-secrets", "Q3 Billing Secrets", "crew/finance", "Finance"]

  it("blocks the publication when something the reader cannot see changed, and says so without saying what", () => {
    const snapshot = clone(baseSnapshot)
    snapshot.baseline.excluded_panels = 1
    snapshot.baseline.withheld_changed = true
    snapshot.blockers = [
      { code: "withheld_change", message: "Part of what this candidate changes is not visible to you, so this review cannot cover all of it. Completing this publication needs someone who can review the whole change." },
    ]
    setReview(snapshot)
    render(<EditorApplicationReview {...props} />)

    const blocker = document.querySelector('[data-blocker="withheld_change"]')!
    expect(blocker.textContent).toMatch(/Part of what this candidate changes is not visible to you/)
    expect(blocker.textContent).toMatch(/needs someone who can review the whole change/)

    // Through the one path, not a second mechanism beside it.
    expect(consentBox().disabled).toBe(true)
    fireEvent.click(consentBox())
    expect(consentBox().checked).toBe(false)
    expect(publishButton().disabled).toBe(true)

    // Neutral by construction: no panel id, no title, no crew, anywhere.
    for (const secret of PROTECTED) {
      expect(document.body.textContent).not.toContain(secret)
    }
  })

  it("blocks on the flag even when the server omitted the blocker row", () => {
    // The flag is the fact; the row is the server's sentence about it. A
    // snapshot carrying one without the other must not publish.
    const snapshot = clone(baseSnapshot)
    snapshot.baseline.excluded_panels = 1
    snapshot.baseline.withheld_changed = true
    setReview(snapshot)
    render(<EditorApplicationReview {...props} />)

    expect(document.querySelector('[data-blocker="withheld_change"]')!.textContent).toMatch(/this review cannot cover all of it/)
    expect(consentBox().disabled).toBe(true)
    expect(publishButton().disabled).toBe(true)
    // And the note above says the same thing rather than the reassurance.
    expect(screen.queryByText(/Nothing this candidate changes is among/)).toBeNull()
    expect(screen.getAllByText(/Completing this publication needs someone who can review the whole change/).length).toBe(2)
  })

  it("offers no way out it does not have: no delegation, no request for access, no escalation", () => {
    const snapshot = clone(baseSnapshot)
    snapshot.baseline.excluded_panels = 1
    snapshot.baseline.withheld_changed = true
    setReview(snapshot)
    render(<EditorApplicationReview {...props} />)

    // By role: every control on the screen — the source-diff file pickers
    // aside — is one of the ones that exist.
    const names = Array.from(document.querySelectorAll("button"))
      .filter(button => button.closest('[aria-label="Changed files"]') === null)
      .map(button => button.textContent?.trim())
    expect(names.sort()).toEqual(["Build preview", "Close without publishing", "Open preview", "Publish application"])
    expect(screen.queryByRole("link")).toBeNull()

    // And by name, for the controls a reader might expect to be offered.
    for (const pattern of [/delegate/i, /request/i, /access/i, /permission/i, /escalate/i, /ask/i, /admin/i, /grant/i, /invite/i]) {
      expect(screen.queryByRole("button", { name: pattern })).toBeNull()
    }
    // Not as prose either: the message must not send anybody anywhere.
    expect(document.body.textContent).not.toMatch(/ask (an|your) admin|request access|contact (an|your)/i)
  })


  it("says when there was no live definition to compare against, rather than listing additions alone", () => {
    state.definitionDiff = { ...state.definitionDiff, baselineMissing: true }
    render(<EditorApplicationReview {...props} />)
    expect(screen.getByText(/There was no live definition to compare against\./)).toBeTruthy()
    expect(screen.getByText(/not a statement that nothing else changed/)).toBeTruthy()
  })


  it("does not present a Page with no candidate as a pending review", () => {
    // Independent review §5 rule 6. The candidate query is never enabled here,
    // so the two "Reading…" placeholders would otherwise sit there for ever.
    const snapshot = clone(baseSnapshot)
    snapshot.candidate = null
    snapshot.blockers = [{ code: "candidate_matches_live", message: "The current draft is identical to the live publication; there is nothing new to review." }]
    setReview(snapshot, { candidate: { data: undefined }, baseline: { data: undefined } })
    render(<EditorApplicationReview {...props} />)

    expect(screen.getByRole("heading", { name: "Nothing to review" })).toBeTruthy()
    expect(screen.queryByText(/Reading the candidate's definition/)).toBeNull()
    expect(screen.queryByText(/Reading the source of both sides/)).toBeNull()
    expect(screen.queryByRole("checkbox")).toBeNull()
    expect(screen.queryByRole("button", { name: "Publish application" })).toBeNull()
    // The server's own reason is carried through, alongside the plain-English one.
    expect(document.querySelector('[data-blocker="candidate_matches_live"]')!.textContent).toMatch(/nothing new to review/)
  })

  it("reports a successful publish as a publication, not as the candidate changing under the reviewer", () => {
    const { rerender } = render(<EditorApplicationReview {...props} />)
    fireEvent.click(consentBox())

    // What the server actually sends back after the draft is consumed.
    const after = clone(baseSnapshot)
    after.candidate = null
    setReview(after, {
      candidate: { data: undefined },
      publish: { mutate: vi.fn(), isPending: false, isError: false, isSuccess: true, error: null, data: { version: 4, build_id: "build-7", source_revision: 7 } },
    })
    rerender(<EditorApplicationReview {...props} />)

    expect(screen.getByRole("heading", { name: "Application published" })).toBeTruthy()
    expect(screen.getByRole("status").textContent).toMatch(/Published as version 4/)
    expect(screen.queryByText(/The candidate changed while this review was open/)).toBeNull()
    expect(screen.queryByText(/read the new candidate before publishing/)).toBeNull()
  })

  it("offers no consent while the candidate's source has not arrived, and says which evidence is missing", () => {
    // R2. `blockers` is the server's list of refusals; it has no opinion about
    // whether the client got the evidence in front of the human.
    setReview(clone(baseSnapshot), { candidate: { data: undefined, isPending: true, isError: false, error: null } })
    render(<EditorApplicationReview {...props} />)

    expect(screen.getByText("Not everything this decision rests on is here")).toBeTruthy()
    expect(screen.getByText("The candidate's source has not finished loading.")).toBeTruthy()
    expect(consentBox().disabled).toBe(true)
    fireEvent.click(consentBox())
    expect(consentBox().checked).toBe(false)
    expect(publishButton().disabled).toBe(true)
  })

  it("renders a failed source read as an error with a retry, never as an endless Reading…", () => {
    setReview(clone(baseSnapshot), {
      candidate: { data: undefined, isPending: false, isError: true, error: new Error("The draft's source could not be read: 503.") },
    })
    const { container } = render(<EditorApplicationReview {...props} />)

    expect(screen.queryByText(/Reading the source of both sides/)).toBeNull()
    expect(screen.getByText("The candidate's source could not be read")).toBeTruthy()
    // The definition comparison is unaffected: it never came from that query.
    expect(screen.queryByText("This candidate’s definition could not be read")).toBeNull()
    expect(screen.getByText(/Remove panel memory from the live Page definition/)).toBeTruthy()
    expect(screen.getAllByText("The draft's source could not be read: 503.").length).toBeGreaterThan(0)
    expect(screen.getByText("The candidate's source could not be read: The draft's source could not be read: 503.")).toBeTruthy()
    // A retry exists, and it is the control that can actually clear the state.
    const retries = Array.from(container.querySelectorAll("button")).filter(button => button.textContent === "Try again")
    expect(retries.length).toBeGreaterThan(0)
    fireEvent.click(retries[0])
    expect(state.review.refresh).toHaveBeenCalled()
    expect(publishButton().disabled).toBe(true)
  })

  it("offers no consent while the baseline source is still loading", () => {
    setReview(clone(baseSnapshot), { baseline: { data: undefined } })
    render(<EditorApplicationReview {...props} />)
    expect(screen.getByText("The source behind the live publication has not finished loading.")).toBeTruthy()
    expect(consentBox().disabled).toBe(true)
    expect(publishButton().disabled).toBe(true)
  })

  it("excepts only the previous source on an initial publication, and still requires the candidate's own", () => {
    const snapshot = clone(baseSnapshot)
    snapshot.initial_publication = true
    snapshot.baseline = { ...snapshot.baseline, publication_version: 0, published: false, source_revision: null, git_commit: null, source_available: false, source_unavailable_reason: null }
    setReview(snapshot, { baseline: { data: undefined } })
    const { rerender } = render(<EditorApplicationReview {...props} />)

    // There is no previous source to wait for: consent is offered.
    expect(screen.queryByText(/The source behind the live publication/)).toBeNull()
    expect(consentBox().disabled).toBe(false)
    fireEvent.click(consentBox())
    expect(publishButton().disabled).toBe(false)

    // The candidate's own source is not excepted by anything.
    setReview(snapshot, { baseline: { data: undefined }, candidate: { data: undefined, isPending: true, isError: false, error: null } })
    rerender(<EditorApplicationReview {...props} />)
    expect(screen.getByText("The candidate's source has not finished loading.")).toBeTruthy()
    expect(consentBox().checked).toBe(false)
    expect(consentBox().disabled).toBe(true)
    expect(publishButton().disabled).toBe(true)
  })

  it("does not report the unreadable baseline twice: the blocker says it once", () => {
    setReview(clone(baseSnapshot), {
      baselineUnavailable: "The retained source for publication 3 was pruned on 2026-08-01.",
      baseline: { data: undefined },
    })
    render(<EditorApplicationReview {...props} />)
    expect(screen.queryByText(/The source behind the live publication has not finished loading/)).toBeNull()
    // And a pruned archive is not offered a retry that can never succeed.
    expect(screen.queryByText(/Try again/)).toBeNull()
    expect(publishButton().disabled).toBe(true)
  })

  it("offers a retry when the baseline source read FAILED rather than being gone", () => {
    setReview(clone(baseSnapshot), {
      baselineUnavailable: "The source of the live publication could not be read.",
      baseline: { data: undefined, isError: true, error: new Error("The source of the live publication could not be read.") },
    })
    render(<EditorApplicationReview {...props} />)
    expect(screen.getByText("Comparison unavailable")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Try again" }))
    expect(state.review.refresh).toHaveBeenCalled()
  })

  it("leaves an unreadable review to the gate above it, and gives no second account of it", () => {
    // `ApplicationPageContent` reads the same query key, holds
    // `kind: "unreadable"` for a failed read AND for an absent snapshot, and
    // carries the retry that refetches what the gate itself read. This
    // component is never mounted in either state; answering them here again
    // would put two surfaces on one condition, free to drift.
    setReview(clone(baseSnapshot), { snapshot: { data: undefined, isError: true, error: new Error("Project storage is busy.") } })
    const { container } = render(<EditorApplicationReview {...props} />)
    expect(container.innerHTML).toBe("")
    expect(screen.queryByText(/Project storage is busy/)).toBeNull()
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull()
    expect(screen.queryByRole("status")).toBeNull()
  })

  it("refuses to review a candidate when the live Page could not be loaded", () => {
    // `pages-layout.tsx:263` passes `detail.error ? null : detail.raw` while
    // capabilities come from `detail.raw`, so the two can disagree and this
    // screen can be mounted with no live definition at all.
    render(<EditorApplicationReview {...props} page={null} />)
    expect(screen.getByRole("alert").textContent).toMatch(/This Page could not be loaded\. Reviewing is not offered while it is missing/)
    expect(screen.queryByRole("checkbox")).toBeNull()
    expect(screen.queryByRole("button", { name: "Publish application" })).toBeNull()
  })

  it("clears consent, not merely disables it, when a blocker appears mid-review", () => {
    const { rerender } = render(<EditorApplicationReview {...props} />)
    fireEvent.click(consentBox())
    expect(consentBox().checked).toBe(true)

    // A transient failure: the baseline read errors, then recovers.
    setReview(clone(baseSnapshot), { baselineUnavailable: "The baseline source could not be read just now." })
    rerender(<EditorApplicationReview {...props} />)
    expect(consentBox().checked).toBe(false)

    setReview(clone(baseSnapshot))
    rerender(<EditorApplicationReview {...props} />)
    // Recovered — and the box must not come back ticked from before.
    expect(consentBox().disabled).toBe(false)
    expect(consentBox().checked).toBe(false)
    expect(publishButton().disabled).toBe(true)
  })

  it("compares the two documents the snapshot carries, not the Page detail and not the draft query", () => {
    // R1, both operands. The digest the publish fence sends and the two
    // documents this list is derived from must come out of one authorized
    // read, or the consent can attest to a comparison the screen never
    // showed. `state.review.candidate.data.definition` is deliberately a
    // different document ({kind: "Page"}): if either operand were still
    // taken from that query, these assertions would catch it.
    render(<EditorApplicationReview {...props} />)
    expect(state.comparedWith[0]).toEqual(liveDefinition)
    expect(state.comparedWith[1]).toEqual(candidateDefinition)
    expect(state.comparedWith[0]).not.toEqual(props.page)
    expect(state.comparedWith[1]).not.toEqual({ kind: "Page" })
  })

  it("does not recompute the comparison when the draft query's definition moves: it is not the basis", () => {
    const { rerender } = render(<EditorApplicationReview {...props} />)
    state.comparedWith = []

    // The draft endpoint's cache entry moved and the snapshot has not. The
    // candidate's SOURCE still comes from there, so the query is not gone —
    // which is exactly why its definition drifting must change nothing.
    setReview(clone(baseSnapshot), {
      candidate: { data: { git_commit: "c7", revision: 7, digest: "sha256:cand", definition: { kind: "Page", moved: true }, project: { files: [] } } },
    })
    rerender(<EditorApplicationReview {...props} />)

    expect(state.comparedWith.some(value => JSON.stringify(value).includes("moved"))).toBe(false)
    expect(state.comparedWith[1]).toEqual(candidateDefinition)
  })

  it("offers no consent when the candidate's stored definition could not be read as a document", () => {
    const snapshot = clone(baseSnapshot)
    snapshot.candidate!.definition = null
    setReview(snapshot)
    render(<EditorApplicationReview {...props} />)

    expect(screen.getByText("This candidate’s definition could not be read")).toBeTruthy()
    expect(screen.getByText("This candidate's own definition is not in this review: its stored definition could not be read as a Page document.")).toBeTruthy()
    expect(screen.getByText(/This is not a statement that the definition is unchanged/)).toBeTruthy()
    expect(consentBox().disabled).toBe(true)
    expect(publishButton().disabled).toBe(true)
  })

  it("does not recompute or re-consent on a Page detail that moved: the detail is not the basis", () => {
    const { rerender } = render(<EditorApplicationReview {...props} />)
    fireEvent.click(consentBox())
    state.comparedWith = []

    // The detail query moved and the snapshot has not. Under the old design
    // this cleared consent, which read as protection — while the request went
    // on attesting to a digest from the other endpoint entirely. Now the
    // detail is not evidence: nothing it does can change the comparison.
    const moved = { ...props.page!, panels: [{ id: "services", schema: "status.v1", owner: "crew/ops", producer: "routine/hourly", sla_seconds: 300 }] }
    rerender(<EditorApplicationReview {...props} page={moved as EditorSectionProps["page"]} />)

    expect(state.comparedWith.filter(value => value === moved)).toHaveLength(0)
    expect(consentBox().checked).toBe(true)
  })

  it("offers no consent at all when the snapshot arrives without the definition it was issued against", () => {
    const snapshot = clone(baseSnapshot)
    snapshot.baseline.definition = null
    setReview(snapshot)
    render(<EditorApplicationReview {...props} />)

    expect(screen.getByText("The live Page definition could not be read")).toBeTruthy()
    expect(screen.getByText("The live Page definition this candidate is compared against is not in this review: its stored definition could not be read as a Page document.")).toBeTruthy()
    // Not a "still loading" state it can never leave, and not a comparison
    // against the Page read elsewhere.
    expect(screen.queryByText(/Reading the candidate's definition/)).toBeNull()
    expect(consentBox().disabled).toBe(true)
    fireEvent.click(consentBox())
    expect(consentBox().checked).toBe(false)
    expect(publishButton().disabled).toBe(true)
  })

  it("clears a consent given before the definition vanished from a refetched snapshot", () => {
    const { rerender } = render(<EditorApplicationReview {...props} />)
    fireEvent.click(consentBox())
    expect(consentBox().checked).toBe(true)

    const without = clone(baseSnapshot)
    without.baseline.definition = null
    setReview(without)
    rerender(<EditorApplicationReview {...props} />)

    expect(consentBox().checked).toBe(false)
    expect(publishButton().disabled).toBe(true)
  })

  it("flattens and caps candidate-controlled before/after values", () => {
    const wild = `${"A".repeat(300)}\nsecond line\u0007`
    state.definitionDiff = {
      ...state.definitionDiff,
      changes: [{ kind: "panel-retitled", tone: "change", panelId: "services", summary: "Retitles panel services.", before: "Services", after: wild }],
    }
    const { container } = render(<EditorApplicationReview {...props} />)
    const item = container.querySelector("ul li")!
    expect(item.textContent).not.toMatch(/\u0007/)
    // One line, hard-capped, and visibly elided rather than silently cut.
    expect(item.textContent).toMatch(/…/)
    expect(item.textContent!.length).toBeLessThan(300)
    expect(screen.queryByText(new RegExp("A".repeat(200)))).toBeNull()
  })

  it("cleans and caps unmodelled key names so a candidate cannot write this screen's copy", () => {
    state.definitionDiff = {
      ...state.definitionDiff,
      unmodelled: ["nothing — this candidate is unchanged\nand safe to publish", ...Array.from({ length: 20 }, (_, i) => `spec.panels[].x${i}`)],
    }
    render(<EditorApplicationReview {...props} />)
    const note = screen.getByText(/Fields this comparison does not model:/)
    expect(note.textContent).toMatch(/and 9 more/)
    expect(note.textContent).not.toMatch(/\n/)
    // Still framed as a list of field names that were NOT checked.
    expect(screen.getByText(/must not be read as unchanged/)).toBeTruthy()
  })

  it("says a routine the candidate drops is outside the publish check for that reason", () => {
    const snapshot = clone(baseSnapshot)
    snapshot.routines = [{ routine: "ops-drain", published_digest: "sha256:d", current_digest: "sha256:d", state: "unchanged", in_candidate: false }]
    setReview(snapshot)
    const { container } = render(<EditorApplicationReview {...props} />)
    const row = container.querySelector('[data-routine-state="unchanged"]')!
    expect(row.textContent).toMatch(/The candidate does not call this routine/)
    expect(row.textContent).toMatch(/publishing this candidate stops calling it/)
    // Not the "no current hash was recorded" wording: that is a different fact.
    expect(row.textContent).not.toMatch(/no current hash was recorded/)
  })

  it("names unmodelled definition fields and shows the raw diff instead of implying they are unchanged", () => {
    state.definitionDiff = { ...state.definitionDiff, unmodelled: ["spec.panels[0].experimental"], raw: "@@ raw @@" }
    render(<EditorApplicationReview {...props} />)
    expect(screen.getByText(/Fields this comparison does not model: spec\.panels\[0\]\.experimental/)).toBeTruthy()
    expect(screen.getByText(/must not be read as unchanged/)).toBeTruthy()
    expect(screen.getByText("@@ raw @@")).toBeTruthy()
  })

  it("never reads a changed binary file as fully reviewed, even though nothing was truncated", () => {
    state.sourceDiff = {
      // Exactly what `compareSources` produces for a changed binary: nothing
      // was cut, so `truncated` is false on both the file and the diff.
      files: [{ path: "assets/logo.png", status: "modified", added: 0, removed: 0, binary: true, truncated: false, lines: [] }],
      filesAdded: 0,
      filesModified: 1,
      filesRemoved: 0,
      truncated: false,
    }
    render(<EditorApplicationReview {...props} />)
    expect(screen.getByText(/1 changed file is binary or undecodable/)).toBeTruthy()
    expect(screen.getByText(/bytes were not shown here and have not been reviewed/)).toBeTruthy()
    // The list row itself must not read as a reviewed zero-line change.
    expect(screen.getByText("Binary · not shown")).toBeTruthy()
    expect(screen.queryByText("+0 −0")).toBeNull()
    expect(screen.getByText(/Its bytes were not shown and have not been reviewed/)).toBeTruthy()
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

  /**
   * The evidence surface, read by someone who cannot see it.
   *
   * A browser pass found every diff line computing to the same colour with
   * the only distinction inside `aria-hidden`, so for assistive tech added
   * and removed were indistinguishable — and for everyone else they hung on
   * one glyph. These hold the three registers the fix put back.
   */
  it("says what each diff line is in words, not only in a glyph", () => {
    state.sourceDiff = {
      files: [
        {
          path: "src/App.tsx",
          status: "modified",
          added: 1,
          removed: 1,
          binary: false,
          truncated: false,
          lines: [
            { kind: "hunk", text: "@@ -1,3 +1,3 @@", oldLine: null, newLine: null },
            { kind: "context", text: "const a = 1", oldLine: 1, newLine: 1 },
            { kind: "del", text: "const b = 2", oldLine: 2, newLine: null },
            { kind: "add", text: "const b = 3", oldLine: null, newLine: 2 },
          ],
        },
      ],
      filesAdded: 0,
      filesModified: 1,
      filesRemoved: 0,
      truncated: false,
    }
    const { container } = render(<EditorApplicationReview {...props} />)

    const words = Array.from(container.querySelectorAll("[data-line-kind]")).map(line => ({
      kind: line.getAttribute("data-line-kind"),
      spoken: line.querySelector(".sr-only")!.textContent,
      marker: line.querySelector('[aria-hidden="true"]')!.textContent,
    }))
    expect(words).toEqual([
      { kind: "hunk", spoken: "Section: ", marker: "@" },
      { kind: "context", spoken: "Unchanged: ", marker: " " },
      { kind: "del", spoken: "Removed: ", marker: "−" },
      { kind: "add", spoken: "Added: ", marker: "+" },
    ])

    // The glyph stays decorative — it is a duplicate of the word above it,
    // and announcing "plus" before every added line is noise.
    for (const line of container.querySelectorAll("[data-line-kind]")) {
      expect(line.querySelector('[aria-hidden="true"]')!.getAttribute("aria-hidden")).toBe("true")
    }

    // And added/removed differ visibly by more than that glyph: a tint and a
    // left rule, so the two are told apart without reading the marker and
    // without relying on hue alone.
    const byKind = (kind: string) => container.querySelector(`[data-line-kind="${kind}"]`)!.className
    expect(byKind("add")).toMatch(/border-l-2/)
    expect(byKind("add")).toMatch(/border-success/)
    expect(byKind("add")).toMatch(/bg-success\/10/)
    expect(byKind("del")).toMatch(/border-destructive/)
    expect(byKind("del")).toMatch(/bg-destructive\/10/)
    expect(byKind("add")).not.toBe(byKind("del"))
    expect(byKind("context")).toMatch(/border-transparent/)
  })

  it("makes the diff a named, guaranteed focus stop rather than a browser accident", () => {
    const { container } = render(<EditorApplicationReview {...props} />)
    const region = container.querySelector('pre[role="region"][aria-label="Diff of src/App.tsx"]') as HTMLElement
    expect(region).toBeTruthy()
    expect(region.getAttribute("tabindex")).toBe("0")
    // Chromium tabs to an overflowing container on its own; Safari does not,
    // which left this unreachable there. The attribute is the guarantee.
    expect(region.className).toMatch(/overflow-auto/)
    expect(region.className).toMatch(/focus-visible:ring-2/)

    // Selecting another file renames the region, so the stop always says
    // which file it lands in.
    fireEvent.click(screen.getByRole("button", { name: /src\/Restart\.tsx/ }))
    expect(container.querySelector('pre[role="region"][aria-label="Diff of src/Restart.tsx"]')).toBeTruthy()
  })

  it("gives the heading focus is moved to a focus indicator that fires for a programmatic move", () => {
    // `:focus-visible` does not reliably match an element focused by script
    // with tabIndex={-1}, so a `focus-visible:` ring would be silent in the
    // one case this heading is ever focused. happy-dom computes no Tailwind,
    // so what is asserted is the rule that will be emitted — plus that the
    // move itself lands, which is the behaviour the ring has to accompany.
    const { rerender } = render(<EditorApplicationReview {...props} pane="preview" />)
    rerender(<EditorApplicationReview {...props} pane="section" />)
    const headings = document.querySelectorAll("#application-review-heading")
    expect(headings).toHaveLength(1)
    const heading = headings[0] as HTMLElement
    expect(document.activeElement).toBe(heading)
    expect(heading.className).toMatch(/focus:ring-2/)
    expect(heading.className).toMatch(/focus:ring-ring/)
    expect(heading.className).not.toMatch(/focus-visible:ring/)
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

  it("renders an interrupted build as its own state, never as a compiler error", () => {
    const snapshot = clone(baseSnapshot)
    // `recoverPageBuilds` sets this after a restart, and so does a build whose
    // artifact could not be persisted. The server blocks publishing on it the
    // same way it blocks a failure (pages_project_review.go:249) — but there is
    // no verdict and no log, so the screen must not send anyone looking for one.
    snapshot.candidate!.build = { id: "build-7", state: "interrupted", artifact_digest: "" }
    snapshot.blockers = [{ code: "build_failed", message: "The build of this draft revision did not complete; build the revision again before publishing." }]
    setReview(snapshot)
    const { container } = render(<EditorApplicationReview {...props} />)

    expect(screen.getByText(/Interrupted — this build stopped before it finished, so there is no result and no build log\. Build the candidate again\./)).toBeTruthy()
    // Not the failure branch's wording, and no compiler log presentation.
    expect(screen.queryByText(/^Failed —/)).toBeNull()
    expect(screen.queryByText(/the compiler gave no reason/)).toBeNull()
    expect(container.querySelector("pre[role='alert']")).toBeNull()
    // Nothing to preview, and no consent to give.
    expect((screen.getByRole("button", { name: "Open preview" }) as HTMLButtonElement).disabled).toBe(true)
    expect(consentBox().disabled).toBe(true)
    expect(publishButton().disabled).toBe(true)
    // Building again is the honest instruction, so that control stays live.
    expect((screen.getByRole("button", { name: "Build preview" }) as HTMLButtonElement).disabled).toBe(false)
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
