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
  render(
    <QueryClientProvider client={qc}>
      <EditorHistorySection
        workspaceId="ws-1"
        slug="fleet-201"
        page={null}
        capabilities={{ ...WITH_APPLICATION, ...capabilities }}
        onNavigate={vi.fn()}
        pane="section"
        onPaneChange={vi.fn()}
        onDirtyChange={vi.fn()}
      />
    </QueryClientProvider>,
  )
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

beforeEach(() => {
  cleanup()
  state.history.restore.mutate.mockReset()
  state.publications.withdraw.mutate.mockReset()
  state.publish.mutate.mockReset()
  state.history.query.isError = false
  state.publications.query.isError = false
  state.publications.query.error = null
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
