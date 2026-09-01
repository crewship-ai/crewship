import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"

// =============================================================================
// `crewship saved-view` has shipped for a while and /journal could not use it,
// so the only way to keep a journal query was a browser bookmark — which the
// URL contract then dropped the search string from.
//
// Two things have to hold for the shared surface not to break the other one:
// the saved-views endpoint returns EVERY view in the workspace, issue boards
// included, and it returns other people's shared views that this user cannot
// delete.
// =============================================================================

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

import { apiFetch } from "@/lib/api-fetch"
import { toast } from "sonner"
import { JournalSavedViews, encodeJournalViewFilters } from "../journal-saved-views"

function view(overrides: Record<string, unknown> = {}) {
  return {
    id: "v1",
    name: "Failed nightly digests",
    filters_json: encodeJournalViewFilters({ tab: "runs", run_status: "FAILED" }),
    sort_json: null,
    view_type: "list",
    is_default: false,
    shared: false,
    created_at: "2026-08-01T00:00:00Z",
    ...overrides,
  }
}

// Radix DropdownMenu opens on pointerdown with the primary button, not click.
async function openMenu() {
  fireEvent.pointerDown(await screen.findByLabelText("Saved views"), {
    button: 0,
    ctrlKey: false,
    pointerId: 1,
  })
}

function okJSON(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: async () => body } as unknown as Response
}

function mockViews(rows: unknown[]) {
  vi.mocked(apiFetch).mockImplementation(async () => okJSON(rows))
}

beforeEach(() => {
  vi.mocked(apiFetch).mockReset()
  vi.mocked(toast.error).mockReset()
  vi.mocked(toast.success).mockReset()
})

describe("JournalSavedViews", () => {
  it("lists only journal views, never the issue board's", async () => {
    mockViews([
      view(),
      view({ id: "v2", name: "My sprint board", view_type: "board", filters_json: "{}" }),
    ])
    render(<JournalSavedViews workspaceId="ws-1" filters={{}} onApply={vi.fn()} />)

    await openMenu()
    expect(await screen.findByText("Failed nightly digests")).toBeTruthy()
    expect(screen.queryByText("My sprint board")).toBeNull()
  })

  it("applies a view's params", async () => {
    const onApply = vi.fn()
    mockViews([view()])
    render(<JournalSavedViews workspaceId="ws-1" filters={{}} onApply={onApply} />)

    await openMenu()
    fireEvent.click(await screen.findByText("Failed nightly digests"))
    await waitFor(() => {
      expect(onApply).toHaveBeenCalledWith({ tab: "runs", run_status: "FAILED" })
    })
  })

  it("marks the view whose filters match what is on screen", async () => {
    mockViews([view()])
    const { rerender } = render(
      <JournalSavedViews workspaceId="ws-1" filters={{}} onApply={vi.fn()} />,
    )
    expect((await screen.findByLabelText("Saved views")).textContent).toContain("Saved views")

    rerender(
      <JournalSavedViews
        workspaceId="ws-1"
        filters={{ tab: "runs", run_status: "FAILED" }}
        onApply={vi.fn()}
      />,
    )
    await waitFor(() => {
      expect(screen.getByLabelText("Saved views").textContent).toContain("Failed nightly digests")
    })
  })

  it("saves the current filters as a journal view", async () => {
    vi.mocked(apiFetch).mockImplementation(async (input, init) => {
      if (init?.method === "POST") return okJSON({ id: "v9" }, 201)
      return okJSON([])
    })
    render(
      <JournalSavedViews
        workspaceId="ws-1"
        filters={{ tab: "runs", run_status: "FAILED" }}
        onApply={vi.fn()}
      />,
    )

    await openMenu()
    fireEvent.click(await screen.findByText("Save current view…"))
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Bad nights" } })
    fireEvent.click(screen.getByText("Save view"))

    await waitFor(() => {
      const post = vi.mocked(apiFetch).mock.calls.find((c) => c[1]?.method === "POST")
      expect(post).toBeTruthy()
      const body = JSON.parse(String(post?.[1]?.body))
      expect(body.name).toBe("Bad nights")
      // saved_views.view_type is CHECK-constrained to 'board' | 'list'.
      // Sending "journal" here 500s on the insert — the surface marker has
      // to live in filters_json, and this assertion is what pins that.
      expect(["board", "list"]).toContain(body.view_type)
      expect(body.shared).toBe(false)
      expect(JSON.parse(body.filters_json)).toEqual({
        surface: "journal",
        params: { tab: "runs", run_status: "FAILED" },
      })
    })
  })

  // The API refuses `create` for the VIEWER role. Swallowing that leaves the
  // dialog closing on a view that was never stored.
  it("says so when the workspace role cannot save views", async () => {
    vi.mocked(apiFetch).mockImplementation(async (input, init) => {
      if (init?.method === "POST") return okJSON({ detail: "forbidden" }, 403)
      return okJSON([])
    })
    render(
      <JournalSavedViews workspaceId="ws-1" filters={{ tab: "runs" }} onApply={vi.fn()} />,
    )

    await openMenu()
    fireEvent.click(await screen.findByText("Save current view…"))
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Nope" } })
    fireEvent.click(screen.getByText("Save view"))

    await waitFor(() => {
      expect(vi.mocked(toast.error)).toHaveBeenCalledWith(
        "You do not have permission to save views in this workspace",
      )
    })
  })

  // A shared view is visible to the whole workspace and deletable only by its
  // owner, so this 403 is the normal answer, not an error to hide.
  it("says so when deleting someone else's shared view", async () => {
    vi.mocked(apiFetch).mockImplementation(async (input, init) => {
      if (init?.method === "DELETE") return okJSON({ detail: "forbidden" }, 403)
      return okJSON([view({ shared: true })])
    })
    render(<JournalSavedViews workspaceId="ws-1" filters={{}} onApply={vi.fn()} />)

    await openMenu()
    fireEvent.click(await screen.findByLabelText("Delete view Failed nightly digests"))

    await waitFor(() => {
      expect(vi.mocked(toast.error)).toHaveBeenCalledWith("Only the owner can delete this view")
    })
  })

  it("will not offer to save an empty filter set", async () => {
    mockViews([])
    render(<JournalSavedViews workspaceId="ws-1" filters={{}} onApply={vi.fn()} />)

    await openMenu()
    expect(await screen.findByText("Filter something to save a view")).toBeTruthy()
  })
})
