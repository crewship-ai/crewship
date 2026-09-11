/**
 * History — three kinds of history with three different effects.
 *
 * The single correctness rule this file exists for: a unified "Restore" is a
 * lie the reader cannot detect. Restoring a panel version changes the LIVE
 * definition for everyone; restoring a source revision changes only a DRAFT;
 * publishing a retained version makes a NEW live publication and moves the
 * counter forward. So the three controls are named differently, and — the
 * part a label alone cannot carry — their three confirmations say three
 * different things.
 *
 * The other two failures pinned here:
 *
 *   · An ordinary panel Page must not grow application headings. A heading
 *     for a thing that does not exist sends somebody looking for it, and a
 *     permanently disabled Publish beside it says "not yet" about something
 *     that will never be.
 *   · A publications read that FAILED must not render as "nothing has been
 *     published". That conflation turns a 403 or a busy archive into a
 *     statement about history.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, fireEvent, cleanup, waitFor, within } from "@testing-library/react"

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), message: vi.fn() } }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))

// The three application hooks are stubbed the way the dialogs' own tests
// stub them: this file is about what the section CLAIMS, and a real fetch
// layer under it would only make the claims harder to read.
const state = vi.hoisted(() => ({
  history: {
    query: {
      data: {
        pages: [
          {
            revisions: [
              { revision: 7, digest: "d7", git_commit: "abc1234567", created_at: "2026-09-09T08:00:00Z", restorable: true },
              { revision: 6, digest: "d6", git_commit: "def1234567", created_at: "2026-09-08T08:00:00Z", restorable: true },
            ],
          },
        ],
      },
      isError: false,
      isPending: false,
      error: null as Error | null,
      hasNextPage: false,
      isFetchingNextPage: false,
      fetchNextPage: vi.fn(),
    },
    restore: { mutate: vi.fn(), isPending: false, isError: false, isSuccess: false, error: null as Error | null },
  },
  publications: {
    query: {
      data: {
        pages: [
          {
            publication_version: 5,
            published: true,
            can_publish: true,
            publications: [
              {
                version: 2,
                source_revision: 6,
                build_id: "b2",
                git_commit: "def1234567",
                artifact_digest: "sha256:2",
                created_at: "2026-09-08T09:00:00Z",
                rollback_of: 0,
                withdrawn_at: "",
              },
            ],
          },
        ],
      },
      isError: false,
      isPending: false,
      error: null as Error | null,
      hasNextPage: false,
      isFetchingNextPage: false,
      fetchNextPage: vi.fn(),
    },
    source: {
      data: {
        git_commit: "def1234567",
        project: { files: [{ path: "src/main.tsx", encoding: "utf8", content: "export const title = 'Ops'" }] },
      },
      isError: false,
      isPending: false,
      error: null as Error | null,
    },
    withdraw: { mutate: vi.fn(), isPending: false, isError: false, isSuccess: false, error: null as Error | null },
  },
}))

vi.mock("@/hooks/use-page-project-history", () => ({ usePageProjectHistory: () => state.history }))
vi.mock("@/hooks/use-page-publications", () => ({ usePagePublications: () => state.publications }))

import { EditorHistorySection } from "@/components/features/pages/editor/section-history"
import {
  NO_PAGE_CAPABILITIES,
  type PageCapabilities,
  type ReviewSnapshotWire,
} from "@/lib/pages/editor-contract"

// ── Fixtures ───────────────────────────────────────────────────────────────

const VERSIONS = {
  page: "fleet-201",
  retained: 50,
  versions: [
    { seq: 4, created_at: "2026-08-10T08:00:00Z", author: "user/u1", author_label: "ada@example.com", name: "Flotila", panel_count: 2, current: true },
    { seq: 3, created_at: "2026-08-02T08:00:00Z", author: "agent/watcher", author_label: "watcher", name: "Flotila", panel_count: 1, current: false },
  ],
}

/**
 * `GET .../project/review?publication=N` — what the server says publishing
 * that retained version would be fenced on. The digests here are the ones the
 * request must carry back: a test that let the component invent them would
 * pass while the product published against a base nobody read.
 */
function reviewSnapshot(overrides: Partial<ReviewSnapshotWire> = {}): ReviewSnapshotWire {
  return {
    issued_at: "2026-09-10T09:00:00Z",
    candidate: null,
    baseline: {
      publication_version: 5,
      published: true,
      definition_digest: "sha256:live-definition",
      definition: { apiVersion: "crewship/v1", kind: "Page", spec: { panels: [] } },
      excluded_panels: 0,
      source_revision: 6,
      git_commit: "def1234567",
      source_available: true,
      source_unavailable_reason: null,
    },
    routines: [
      {
        routine: "nightly-close",
        published_digest: "r1",
        current_digest: "r1",
        state: "unchanged",
        // Declared by the version being published, so it is in the fence.
        // The list is a union and the flag is what separates the two halves.
        in_candidate: true,
      },
    ],
    capabilities: { may_edit_spec: true, may_publish: true },
    blockers: [],
    initial_publication: false,
    ...overrides,
  }
}

const RECEIPT = {
  version: 6,
  build_id: "b2",
  source_revision: 6,
  artifact_digest: "sha256:2",
  git_commit: "def1234567",
  created_at: "2026-09-10T09:01:00Z",
}

/** Set per test, before `mount()`. Reset by `resetFixtures`. */
let reviewResponse: Response | null = null
let publishResponse: Response | null = null
let snapshotBody: ReviewSnapshotWire = reviewSnapshot()

const WITH_APPLICATION: PageCapabilities = {
  ...NO_PAGE_CAPABILITIES,
  loaded: true,
  hasApplication: true,
  mayPublishApplication: true,
  mayViewSourceHistory: true,
}

function jsonResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: { get: () => null },
    json: async () => body,
    text: async () => JSON.stringify(body),
  } as unknown as Response
}

function section(capabilities: Partial<PageCapabilities>) {
  return (
    <EditorHistorySection
      workspaceId="ws-1"
      slug="fleet-201"
      page={null}
      capabilities={{ ...WITH_APPLICATION, ...capabilities }}
      onNavigate={vi.fn()}
      pane="section"
      onPaneChange={vi.fn()}
      onLeaveEditor={vi.fn()}
      onPageDeleted={vi.fn()}
      onDirtyChange={vi.fn()}
    />
  )
}

interface Sent {
  method: string
  url: string
  body: Record<string, unknown> | null
}

function mount(capabilities: Partial<PageCapabilities> = {}) {
  const sent: Sent[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      const method = (init?.method ?? "GET").toUpperCase()
      sent.push({
        method,
        url,
        body: typeof init?.body === "string" ? JSON.parse(init.body) : null,
      })
      if (url.includes("/versions")) return jsonResponse(200, VERSIONS)
      // The publish and its fence go through the real request path on
      // purpose: the body is the thing under test, and a mocked mutation
      // would assert the arguments this file chose rather than the ones the
      // server would receive.
      if (url.includes("/project/review")) return reviewResponse ?? jsonResponse(200, snapshotBody)
      if (url.includes("/project/publish")) return publishResponse ?? jsonResponse(200, RECEIPT)
      return jsonResponse(404, { error: `unrouted ${method} ${url}` })
    }),
  )
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  const view = render(
    <QueryClientProvider client={qc}>{section(capabilities)}</QueryClientProvider>,
  )
  /**
   * Re-render the same tree after moving the stubbed hook state. The hooks
   * are mocks, so this is how "somebody else published while you were
   * reading this" is expressed — and it is the only way to test that a
   * review is thrown away when the base it was taken against moves.
   */
  const again = () =>
    view.rerender(<QueryClientProvider client={qc}>{section(capabilities)}</QueryClientProvider>)
  return { again, sent }
}

/** The fenced publish, once the request has actually left. */
async function publishRequest(sent: Sent[]): Promise<Record<string, unknown>> {
  await waitFor(() => expect(sent.some((r) => r.url.includes("/project/publish"))).toBe(true))
  return sent.find((r) => r.url.includes("/project/publish"))!.body!
}

/** Walk the inspect pane up to a publish that is offered. */
async function readyToPublish(version: number): Promise<HTMLButtonElement> {
  fireEvent.click(screen.getByRole("button", { name: `Inspect version ${version}` }))
  await waitFor(() => expect(document.querySelector("[data-slot='publication-fence']")).toBeTruthy())
  fireEvent.click(screen.getByLabelText(`I reviewed version ${version} and trust its code.`))
  return screen.getByRole("button", { name: "Publish this version" }) as HTMLButtonElement
}

function sectionText(): string {
  return document.querySelector("[data-slot='editor-section-history']")!.textContent ?? ""
}

/** Open a confirm, read its description, close it again. */
async function confirmationFor(open: () => void): Promise<string> {
  open()
  const dialog = await screen.findByRole("alertdialog")
  const text = dialog.textContent ?? ""
  fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }))
  await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())
  return text
}

/**
 * Every test gets the fixtures back. They are mutated in place rather than
 * rebuilt because the mocked hooks close over these objects, and a test that
 * left `can_publish: false` behind would silently weaken the next one.
 */
function resetFixtures() {
  state.history.query.data.pages[0].revisions = [
    { revision: 7, digest: "d7", git_commit: "abc1234567", created_at: "2026-09-09T08:00:00Z", restorable: true },
    { revision: 6, digest: "d6", git_commit: "def1234567", created_at: "2026-09-08T08:00:00Z", restorable: true },
  ]
  state.history.query.isError = false
  state.history.query.error = null
  state.history.query.hasNextPage = false
  state.history.restore.mutate.mockReset()
  state.history.query.fetchNextPage.mockReset()

  const publications = state.publications.query.data.pages[0]
  publications.publication_version = 5
  publications.published = true
  publications.can_publish = true
  publications.publications = [
    {
      version: 2,
      source_revision: 6,
      build_id: "b2",
      git_commit: "def1234567",
      artifact_digest: "sha256:2",
      created_at: "2026-09-08T09:00:00Z",
      rollback_of: 0,
      withdrawn_at: "",
    },
  ]
  state.publications.query.isError = false
  state.publications.query.error = null
  state.publications.query.hasNextPage = false
  state.publications.query.fetchNextPage.mockReset()
  state.publications.source.data = {
    git_commit: "def1234567",
    project: { files: [{ path: "src/main.tsx", encoding: "utf8", content: "export const title = 'Ops'" }] },
  }
  state.publications.source.isError = false
  state.publications.withdraw.mutate.mockReset()
  reviewResponse = null
  publishResponse = null
  snapshotBody = reviewSnapshot()
}

beforeEach(() => {
  cleanup()
  resetFixtures()
})
afterEach(() => vi.unstubAllGlobals())

// ── 1. Three restores, three effects ───────────────────────────────────────

describe("the three histories", () => {
  it("names three restores and states three different effects in their confirmations", async () => {
    mount()
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='page-version']")).toHaveLength(2),
    )

    // The three sub-sections, each named for what it holds.
    const text = sectionText()
    expect(text).toContain("Panel definitions")
    expect(text).toContain("Application source")
    expect(text).toContain("Application publications")

    // 1. The live definition.
    const panel = await confirmationFor(() =>
      fireEvent.click(screen.getByLabelText("Restore panel version 3")),
    )
    expect(panel).toContain("writes the live definition")
    expect(panel).toContain("arrives with no data")

    // 2. A draft, and only a draft.
    const source = await confirmationFor(() =>
      fireEvent.click(screen.getByLabelText("Restore revision 6 to draft")),
    )
    expect(source).toContain("creates a new draft")
    expect(source).toContain("The live publication does not change")

    // 3. A new publication, going forward.
    const publishButton = await readyToPublish(2)
    const publication = await confirmationFor(() => fireEvent.click(publishButton))
    expect(publication).toContain("makes a new live publication")
    expect(publication).toContain("counter moves forward")

    // Three sentences, and no two of them the same. A shared confirmation is
    // how three effects become one in a reader's head.
    expect(new Set([panel, source, publication]).size).toBe(3)
    expect(source).not.toContain("writes the live definition")
    expect(publication).not.toContain("creates a new draft")
    expect(panel).not.toContain("publication")
  })

  it("restores a source revision without publishing anything", async () => {
    const { sent } = mount()
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='source-revision']")).toHaveLength(2),
    )

    fireEvent.click(screen.getByLabelText("Restore revision 6 to draft"))
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "Restore to draft" }))

    // CAS-fenced on the head shown as the current draft, exactly as the
    // dialog this was lifted out of did it.
    expect(state.history.restore.mutate).toHaveBeenCalledWith({ revision: 6, expectedRevision: 7 })
    // And the publication endpoint is not touched: a draft is not a release.
    expect(sent.some((r) => r.url.includes("/project/publish"))).toBe(false)
    expect(state.publications.withdraw.mutate).not.toHaveBeenCalled()
  })

  it("fences a publication on every base the reviewer was shown", async () => {
    const { sent } = mount()
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='publication']")).toHaveLength(1),
    )

    fireEvent.click(screen.getByRole("button", { name: "Inspect version 2" }))
    await waitFor(() =>
      expect(document.querySelector("[data-slot='publication-fence']")).toBeTruthy(),
    )
    const publish = screen.getByRole("button", { name: "Publish this version" }) as HTMLButtonElement
    // The per-version review gate survives the move out of the dialog.
    expect(publish.disabled).toBe(true)

    fireEvent.click(screen.getByLabelText("I reviewed version 2 and trust its code."))
    fireEvent.click(publish)
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "Publish this version" }))

    // The fence was asked about THIS version, not about the draft.
    const asked = sent.find((r) => r.url.includes("/project/review"))!
    expect(asked.url).toContain("publication=2")

    // Every value in the body came off the snapshot that was on screen. The
    // two digests are what the server compares inside the publishing
    // transaction; the three older fences are still here beside them.
    expect(await publishRequest(sent)).toEqual({
      rollback_version: 2,
      expected_publication: 5,
      reviewed_code: true,
      expected_definition_digest: "sha256:live-definition",
      expected_routine_digests: { "nightly-close": "r1" },
    })
    expect(state.history.restore.mutate).not.toHaveBeenCalled()
  })
})

// ── 2. An ordinary panel Page ──────────────────────────────────────────────

describe("a Page with no application", () => {
  it("shows panel definitions alone — the application headings are absent, not disabled", async () => {
    mount({ hasApplication: false, mayPublishApplication: false, mayViewSourceHistory: false })
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='page-version']")).toHaveLength(2),
    )

    expect(sectionText()).toContain("Panel definitions")
    // Absent, not merely disabled: a heading for a thing that does not exist
    // sends somebody looking for it.
    expect(sectionText()).not.toContain("Application source")
    expect(sectionText()).not.toContain("Application publications")
    expect(document.querySelector("[data-slot='history-source']")).toBeNull()
    expect(document.querySelector("[data-slot='history-publications']")).toBeNull()
    expect(screen.queryByRole("button", { name: /publish/i })).toBeNull()
    expect(screen.queryByRole("button", { name: /draft/i })).toBeNull()

    // The one restore that IS here still says what it does.
    expect(screen.getByLabelText("Restore panel version 3")).toBeTruthy()
  })
})

// ── 3. A read that failed is not an empty history ──────────────────────────

describe("when the publication history cannot be read", () => {
  it("gives the reason instead of rendering an empty list", async () => {
    state.publications.query.isError = true
    state.publications.query.error = new Error("the application archive is temporarily unavailable")
    mount()

    await waitFor(() =>
      expect(screen.getByText("the application archive is temporarily unavailable")).toBeTruthy(),
    )
    // The empty state would be a claim about history. Nobody made it.
    expect(sectionText()).not.toContain("No application has been published yet")
    expect(document.querySelectorAll("[data-slot='publication']")).toHaveLength(0)
    expect(sectionText()).toContain("unavailable")
  })

  it("keeps the application headings but refuses the lists when the source history is not this reader's", async () => {
    mount({ mayViewSourceHistory: false })
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='page-version']")).toHaveLength(2),
    )

    // The Page really does have an application, so the heading stays and the
    // missing right is named. What must NOT appear is an empty list.
    expect(document.querySelector("[data-slot='history-application-refused']")).toBeTruthy()
    expect(sectionText()).toContain("not yours to read")
    expect(sectionText()).toContain("This is not a statement that there is no history")
    expect(document.querySelectorAll("[data-slot='publication']")).toHaveLength(0)
    expect(document.querySelectorAll("[data-slot='source-revision']")).toHaveLength(0)
  })
})

// ── 4. What the two deleted dialogs used to guard ──────────────────────────
//
// `page-project-history.tsx` and `page-publications.tsx` are redundant now:
// their lists, their inspect pane, their restore and their withdraw all live
// in this section. Every assertion those two test files made is re-homed
// below, and the fences are re-homed as fences — an `expected_revision` or an
// `expected_publication` that stopped being sent would still render a screen
// that looks right and would publish over somebody else's work.

describe("the source list's own rules", () => {
  it("offers no restore for the current draft, and refuses one the archive cannot restore", async () => {
    state.history.query.data.pages[0].revisions = [
      { revision: 7, digest: "d7", git_commit: "abc1234567", created_at: "2026-09-09T08:00:00Z", restorable: true },
      { revision: 6, digest: "d6", git_commit: "def1234567", created_at: "2026-09-08T08:00:00Z", restorable: true },
      { revision: 5, digest: "d5", git_commit: "", created_at: "2026-09-07T08:00:00Z", restorable: false },
    ]
    mount()
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='source-revision']")).toHaveLength(3),
    )

    // The head is the draft you already have. It gets a badge, not a control:
    // "restore the thing you are on" is a button with nothing behind it.
    expect(screen.queryByLabelText("Restore revision 7 to draft")).toBeNull()
    expect(sectionText()).toContain("current draft")

    // The server said this one is not restorable — a revision whose source
    // is no longer retained. Offering it would produce a refusal at the far
    // end of a confirm dialog.
    expect(
      (screen.getByLabelText("Restore revision 5 to draft") as HTMLButtonElement).disabled,
    ).toBe(true)
    expect(sectionText()).toContain("not restorable")
    // A revision whose source predates commit pinning is still a revision.
    expect(sectionText()).toContain("legacy snapshot")

    expect(
      (screen.getByLabelText("Restore revision 6 to draft") as HTMLButtonElement).disabled,
    ).toBe(false)
  })

  it("distinguishes an unreadable source history from an empty one", async () => {
    state.history.query.isError = true
    state.history.query.error = new Error("the project archive is unreadable")
    mount()
    await waitFor(() => expect(screen.getByText("the project archive is unreadable")).toBeTruthy())
    // A failed read is not a claim about history.
    expect(sectionText()).not.toContain("No source revisions yet")
    expect(document.querySelectorAll("[data-slot='source-revision']")).toHaveLength(0)

    cleanup()
    resetFixtures()
    state.history.query.data.pages[0].revisions = []
    mount()
    await waitFor(() => expect(screen.getByText("No source revisions yet")).toBeTruthy())
    // …and an empty read IS one. The two must not render the same.
    expect(sectionText()).not.toContain("unreadable")
  })

  it("pages through both histories rather than presenting the first page as all of it", async () => {
    state.history.query.hasNextPage = true
    state.publications.query.hasNextPage = true
    mount()
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='source-revision']")).toHaveLength(2),
    )

    fireEvent.click(screen.getByRole("button", { name: "Load older revisions" }))
    expect(state.history.query.fetchNextPage).toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Older publications" }))
    expect(state.publications.query.fetchNextPage).toHaveBeenCalled()
  })
})

describe("inspecting a retained publication", () => {
  it("shows the archived source of the version being reviewed", async () => {
    mount()
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='publication']")).toHaveLength(1),
    )

    fireEvent.click(screen.getByRole("button", { name: "Inspect version 2" }))
    // The point of the pane: the reviewer reads the code that would go live,
    // not a summary of it.
    expect(screen.getByText("export const title = 'Ops'")).toBeTruthy()
    const pane = screen.getByLabelText("Inspect version 2")
    expect(pane.textContent).toContain("b2")
    expect(pane.textContent).toContain("def1234567")
    expect(pane.textContent).toContain("sha256:2")
  })

  it("describes a binary asset instead of rendering it", async () => {
    state.publications.source.data = {
      git_commit: "def1234567",
      project: { files: [{ path: "logo.png", encoding: "base64", content: "iVBORw0KGgoAAAANS" }] },
    }
    mount()
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='publication']")).toHaveLength(1),
    )

    fireEvent.click(screen.getByRole("button", { name: "Inspect version 2" }))
    expect(screen.getByText(/Binary asset \(base64\)/)).toBeTruthy()
    // The bytes are described, never poured into the DOM.
    expect(document.body.textContent).not.toContain("iVBORw0KGgoAAAANS")
  })
})

describe("the publish gate", () => {
  it("throws the review away when the live publication moves under it", async () => {
    const { again, sent } = mount()
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='publication']")).toHaveLength(1),
    )

    const publish = await readyToPublish(2)
    expect(publish.disabled).toBe(false)

    // Somebody else published while this reviewer was reading. A review of
    // what USED to be live is not a review of what is live now.
    state.publications.query.data.pages[0].publication_version = 6
    again()
    expect(publish.disabled).toBe(true)
    expect(
      (screen.getByLabelText("I reviewed version 2 and trust its code.") as HTMLInputElement).checked,
    ).toBe(false)
    expect(sent.some((r) => r.url.includes("/project/publish"))).toBe(false)
  })

  it("refuses a version whose archived source is not the source of that version", async () => {
    state.publications.source.data = {
      git_commit: "0000000000",
      project: { files: [{ path: "src/main.tsx", encoding: "utf8", content: "export const title = 'Ops'" }] },
    }
    mount()
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='publication']")).toHaveLength(1),
    )

    // Ticked, and still shut: what was read is not what would be published.
    expect((await readyToPublish(2)).disabled).toBe(true)
  })

  it("refuses to republish the version that is already live", async () => {
    state.publications.query.data.pages[0].publications = [
      {
        version: 5,
        source_revision: 6,
        build_id: "b5",
        git_commit: "def1234567",
        artifact_digest: "sha256:5",
        created_at: "2026-09-09T09:00:00Z",
        rollback_of: 0,
        withdrawn_at: "",
      },
    ]
    mount()
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='publication']")).toHaveLength(1),
    )

    expect(sectionText()).toContain("live now")
    // Publishing what is already live would burn a version number to change
    // nothing.
    expect((await readyToPublish(5)).disabled).toBe(true)
  })
})

describe("withdrawing the application", () => {
  it("requires a fresh confirmation when the live publication moves, and fences the call on it", async () => {
    const { again, sent } = mount()
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='publication']")).toHaveLength(1),
    )

    const withdraw = screen.getByRole("button", { name: "Withdraw application" }) as HTMLButtonElement
    expect(withdraw.disabled).toBe(true)
    fireEvent.click(screen.getByLabelText("Stop this application for all viewers"))
    expect(withdraw.disabled).toBe(false)

    // The confirmation was about version 5. It does not carry over to 6.
    state.publications.query.data.pages[0].publication_version = 6
    again()
    expect(withdraw.disabled).toBe(true)

    fireEvent.click(screen.getByLabelText("Stop this application for all viewers"))
    fireEvent.click(withdraw)
    expect(state.publications.withdraw.mutate).toHaveBeenCalledWith(6)
    expect(sent.some((r) => r.url.includes("/project/publish"))).toBe(false)
  })
})

describe("a reader who may not publish", () => {
  it("gets neither control when the server says can_publish is false, and is told why", async () => {
    state.publications.query.data.pages[0].can_publish = false
    mount()
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='publication']")).toHaveLength(1),
    )

    fireEvent.click(screen.getByRole("button", { name: "Inspect version 2" }))
    expect(screen.queryByRole("button", { name: "Publish this version" })).toBeNull()
    expect(screen.queryByRole("button", { name: "Withdraw application" })).toBeNull()
    expect(screen.queryByLabelText(/I reviewed version/)).toBeNull()
    // Inspecting a retained version is still theirs to do.
    expect(screen.getByText("export const title = 'Ops'")).toBeTruthy()
    expect(sectionText()).toContain("You may not publish")
  })

  it("gets neither control when the section's own capability says so", async () => {
    mount({ mayPublishApplication: false })
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='publication']")).toHaveLength(1),
    )

    // `can_publish` is true here: the optimistic capability alone is enough
    // to stop a control that goes live from being drawn.
    expect(screen.queryByRole("button", { name: "Withdraw application" })).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Inspect version 2" }))
    expect(screen.queryByRole("button", { name: "Publish this version" })).toBeNull()
  })
})

// ── 5. The publication fence ───────────────────────────────────────────────
//
// The server compares `expected_definition_digest` and
// `expected_routine_digests` inside the publishing transaction. Both come off
// `GET .../project/review?publication=N` — the snapshot the human was shown —
// because a value re-read at click time is a value nobody reviewed. What is
// pinned here is that the screen states what is being attested to *before*
// the click, and that a base which moved comes back as a sentence naming what
// moved rather than as a failed request.

describe("what the reviewer is attesting to", () => {
  it("names a routine that changed, refuses to let it pass on the code tick alone, and still fences on it", async () => {
    snapshotBody = reviewSnapshot({
      routines: [
        {
          routine: "nightly-close",
          published_digest: "r1",
          current_digest: "r2",
          state: "changed",
          in_candidate: true,
        },
        {
          routine: "sweep",
          published_digest: "s1",
          current_digest: "",
          state: "unknown",
          in_candidate: true,
        },
      ],
    })
    const { sent } = mount()
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='publication']")).toHaveLength(1),
    )

    const publish = await readyToPublish(2)

    // Named, and named as the thing it is: old code bound to a routine
    // nobody approved for this version.
    const changed = document.querySelector("[data-slot='fence-routine'][data-state='changed']")!
    expect(changed.textContent).toContain("nightly-close")
    expect(changed.textContent).toContain("changed since this version was published")
    expect(sectionText()).toContain("a routine nobody approved for this version")

    // A row with no current digest is not covered by the check, and says so
    // rather than sitting in the list looking checked.
    expect(sectionText()).toContain("sweep")
    expect(sectionText()).toContain("not covered by this check")

    // The code tick alone is not enough: it attests to the code, and the
    // routine is not in the code.
    expect(publish.disabled).toBe(true)
    fireEvent.click(
      screen.getByLabelText("I understand version 2 will run against the current nightly-close."),
    )
    expect(publish.disabled).toBe(false)

    fireEvent.click(publish)
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "Publish this version" }))

    // The changed routine is fenced on its CURRENT digest — that is the
    // server's own key set, and the uncovered one is absent rather than sent
    // as an empty string nobody reviewed.
    const body = await publishRequest(sent)
    expect(body.expected_routine_digests).toEqual({ "nightly-close": "r2" })
    expect(body.expected_definition_digest).toBe("sha256:live-definition")
  })

  it("turns a blocker into a sentence and shuts the control", async () => {
    snapshotBody = reviewSnapshot({
      blockers: [
        { code: "build_missing", message: "The build recorded for version 2 is no longer stored." },
      ],
    })
    mount()
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='publication']")).toHaveLength(1),
    )

    const publish = await readyToPublish(2)
    // Not a grey button with no explanation: the server's own sentence, and
    // the machine-readable code beside it.
    const blocker = document.querySelector("[data-slot='publish-blocker']")!
    expect(blocker.getAttribute("data-code")).toBe("build_missing")
    expect(blocker.textContent).toContain("no longer stored")
    // Ticked, acknowledged, and still shut.
    expect(publish.disabled).toBe(true)
  })

  it("will not publish at all when the fence itself cannot be read", async () => {
    reviewResponse = jsonResponse(503, { error: "the review store is busy" })
    const { sent } = mount()
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='publication']")).toHaveLength(1),
    )

    fireEvent.click(screen.getByRole("button", { name: "Inspect version 2" }))
    await waitFor(() => expect(screen.getByText(/the review store is busy/)).toBeTruthy())
    fireEvent.click(screen.getByLabelText("I reviewed version 2 and trust its code."))

    // No snapshot, no digests, no publish — a guessed fence is worse than
    // not publishing.
    expect(
      (screen.getByRole("button", { name: "Publish this version" }) as HTMLButtonElement).disabled,
    ).toBe(true)
    expect(sent.some((r) => r.url.includes("/project/publish"))).toBe(false)
  })
})

describe("when the fence trips", () => {
  it("names the routines the server refused on, and drops the review tick", async () => {
    publishResponse = jsonResponse(409, {
      error: "A base you reviewed changed before this publication was applied.",
      conflict: "routines",
      routines: ["nightly-close"],
    })
    mount()
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='publication']")).toHaveLength(1),
    )

    const publish = await readyToPublish(2)
    fireEvent.click(publish)
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "Publish this version" }))

    const conflict = await waitFor(() => {
      const node = document.querySelector("[data-slot='publish-conflict']")
      expect(node).toBeTruthy()
      return node!
    })
    // Which base moved, and which routines — a reviewer cannot parse that
    // back out of "publication failed".
    expect(conflict.getAttribute("data-conflict")).toBe("routines")
    expect(conflict.textContent).toContain("nightly-close")
    expect(conflict.textContent).toContain("Read them again before you publish")

    // The review went with the base it was taken against.
    await waitFor(() =>
      expect(
        (screen.getByLabelText("I reviewed version 2 and trust its code.") as HTMLInputElement)
          .checked,
      ).toBe(false),
    )
    expect(
      (screen.getByRole("button", { name: "Publish this version" }) as HTMLButtonElement).disabled,
    ).toBe(true)
  })
})

// ── 6. The routine list is a union ─────────────────────────────────────────

describe("a routine the published version does not call", () => {
  it("is shown as dropped and kept out of the fence", async () => {
    snapshotBody = reviewSnapshot({
      routines: [
        {
          routine: "nightly-close",
          published_digest: "r1",
          current_digest: "r1",
          state: "unchanged",
          in_candidate: true,
        },
        {
          // Only the LIVE publication calls this one. Version 2 drops it.
          routine: "hourly-sweep",
          published_digest: "s1",
          current_digest: "s2",
          state: "changed",
          in_candidate: false,
        },
      ],
    })
    const { sent } = mount()
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='publication']")).toHaveLength(1),
    )

    const publish = await readyToPublish(2)

    // The reviewer sees the loss: a routine the live application calls and
    // this version does not.
    const dropped = document.querySelector("[data-slot='fence-routine'][data-in-candidate='false']")!
    expect(dropped.textContent).toContain("hourly-sweep")
    expect(dropped.textContent).toContain("does not call it, so it is not fenced")
    expect(sectionText()).toContain("Publishing this version stops calling")

    // It is `changed`, but it cannot bind this version to anything — this
    // version never calls it — so it raises no acknowledgement gate.
    expect(screen.queryByLabelText(/I understand version 2 will run against/)).toBeNull()
    expect(publish.disabled).toBe(false)

    fireEvent.click(publish)
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "Publish this version" }))

    // And it is absent from the fence. Sending it would 409 on a routine
    // nobody moved, and no refetch would ever clear that.
    const body = await publishRequest(sent)
    expect(body.expected_routine_digests).toEqual({ "nightly-close": "r1" })
  })
})
