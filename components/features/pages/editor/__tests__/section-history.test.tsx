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
  publish: { mutate: vi.fn(), isPending: false, isError: false, isSuccess: false, error: null as Error | null, data: null },
}))

vi.mock("@/hooks/use-page-project-history", () => ({ usePageProjectHistory: () => state.history }))
vi.mock("@/hooks/use-page-publications", () => ({ usePagePublications: () => state.publications }))
vi.mock("@/hooks/use-page-application", () => ({ usePageApplication: () => ({ publish: state.publish }) }))

import { EditorHistorySection } from "@/components/features/pages/editor/section-history"
import { NO_PAGE_CAPABILITIES, type PageCapabilities } from "@/lib/pages/editor-contract"

// ── Fixtures ───────────────────────────────────────────────────────────────

const VERSIONS = {
  page: "fleet-201",
  retained: 50,
  versions: [
    { seq: 4, created_at: "2026-08-10T08:00:00Z", author: "user/u1", author_label: "ada@example.com", name: "Flotila", panel_count: 2, current: true },
    { seq: 3, created_at: "2026-08-02T08:00:00Z", author: "agent/watcher", author_label: "watcher", name: "Flotila", panel_count: 1, current: false },
  ],
}

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

function mount(capabilities: Partial<PageCapabilities> = {}) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) =>
      String(input).includes("/versions")
        ? jsonResponse(200, VERSIONS)
        : jsonResponse(404, { error: `unrouted ${String(input)}` }),
    ),
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
  return { again }
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
  state.publish.mutate.mockReset()
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
    fireEvent.click(screen.getByRole("button", { name: "Inspect version 2" }))
    fireEvent.click(screen.getByLabelText("I reviewed version 2 and trust its code."))
    const publication = await confirmationFor(() =>
      fireEvent.click(screen.getByRole("button", { name: "Publish this version" })),
    )
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
    mount()
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
    expect(state.publish.mutate).not.toHaveBeenCalled()
    expect(state.publications.withdraw.mutate).not.toHaveBeenCalled()
  })

  it("fences a publication on the version the reviewer was looking at", async () => {
    mount()
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='publication']")).toHaveLength(1),
    )

    fireEvent.click(screen.getByRole("button", { name: "Inspect version 2" }))
    const publish = screen.getByRole("button", { name: "Publish this version" }) as HTMLButtonElement
    // The per-version review gate survives the move out of the dialog.
    expect(publish.disabled).toBe(true)

    fireEvent.click(screen.getByLabelText("I reviewed version 2 and trust its code."))
    fireEvent.click(publish)
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "Publish this version" }))

    expect(state.publish.mutate).toHaveBeenCalledWith({
      rollback_version: 2,
      expected_publication: 5,
      reviewed_code: true,
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
    const { again } = mount()
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='publication']")).toHaveLength(1),
    )

    fireEvent.click(screen.getByRole("button", { name: "Inspect version 2" }))
    fireEvent.click(screen.getByLabelText("I reviewed version 2 and trust its code."))
    const publish = screen.getByRole("button", { name: "Publish this version" }) as HTMLButtonElement
    expect(publish.disabled).toBe(false)

    // Somebody else published while this reviewer was reading. A review of
    // what USED to be live is not a review of what is live now.
    state.publications.query.data.pages[0].publication_version = 6
    again()
    expect(publish.disabled).toBe(true)
    expect(
      (screen.getByLabelText("I reviewed version 2 and trust its code.") as HTMLInputElement).checked,
    ).toBe(false)
    expect(state.publish.mutate).not.toHaveBeenCalled()
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

    fireEvent.click(screen.getByRole("button", { name: "Inspect version 2" }))
    fireEvent.click(screen.getByLabelText("I reviewed version 2 and trust its code."))
    // Ticked, and still shut: what was read is not what would be published.
    expect(
      (screen.getByRole("button", { name: "Publish this version" }) as HTMLButtonElement).disabled,
    ).toBe(true)
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
    fireEvent.click(screen.getByRole("button", { name: "Inspect version 5" }))
    fireEvent.click(screen.getByLabelText("I reviewed version 5 and trust its code."))
    // Publishing what is already live would burn a version number to change
    // nothing.
    expect(
      (screen.getByRole("button", { name: "Publish this version" }) as HTMLButtonElement).disabled,
    ).toBe(true)
  })
})

describe("withdrawing the application", () => {
  it("requires a fresh confirmation when the live publication moves, and fences the call on it", async () => {
    const { again } = mount()
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
    expect(state.publish.mutate).not.toHaveBeenCalled()
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
