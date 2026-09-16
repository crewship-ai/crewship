import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, within, cleanup, fireEvent, waitFor } from "@testing-library/react"

import { apiFetch } from "@/lib/api-fetch"

// =============================================================================
// Pages in ⌘K (#2570). A page can be found by name, its row opens THAT page,
// and Recent is the signed-in user's, in this workspace, checked against the
// list the server just authorised — so a workspace switch, a different login
// or a revoked page never leaves an old name on screen.
// =============================================================================

const h = vi.hoisted(() => ({
  workspaceId: "ws-A" as string | null,
  userId: "u-alice" as string | null,
  authStatus: "authenticated" as string,
  push: vi.fn(),
}))

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: h.workspaceId, role: "OWNER" }),
}))

vi.mock("@/hooks/use-auth", () => ({
  useSessionSafe: () => ({
    data: h.userId ? { user: { id: h.userId } } : null,
    status: h.authStatus,
  }),
}))

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: h.push, replace: vi.fn(), prefetch: vi.fn(), back: vi.fn(), forward: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => "/",
}))

// broadcastSessionExpired: hooks/use-pages loads the realtime hook, which
// binds the session-expiry broadcast at import time.
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(), broadcastSessionExpired: vi.fn() }))

import { CommandPalette } from "../command-palette"

type Answer = { ok: boolean; status?: number; body?: unknown; malformed?: boolean }

/** Per-path answers; anything unlisted is an empty 200 list. */
const ANSWERS: Record<string, Answer> = {}

function ok(rows: unknown) {
  return { ok: true, status: 200, json: async () => rows } as unknown as Response
}

function page(over: Record<string, unknown>) {
  return {
    id: `pg-${over.slug}`,
    slug: "x",
    name: "X",
    owner: "crew/ops",
    owner_crew_slug: "ops",
    panel_count: 1,
    panel_states: { fresh: 1, stale: 0, failed: 0, never_produced: 0 },
    reach: ["role"],
    folder: null,
    pages_version: 1,
    icon: "",
    color: "",
    has_project: false,
    has_application: false,
    publication_version: 0,
    ...over,
  }
}

const OPS_CREW = { id: "c1", name: "Operations", slug: "ops", color: "blue", icon: "server", _count: { agents: 1, members: 1 } }

beforeEach(() => {
  h.workspaceId = "ws-A"
  h.userId = "u-alice"
  h.authStatus = "authenticated"
  h.push.mockReset()
  for (const k of Object.keys(ANSWERS)) delete ANSWERS[k]
  vi.mocked(localStorage.getItem).mockReturnValue(null)
  vi.mocked(localStorage.setItem).mockClear()
  vi.mocked(localStorage.removeItem).mockClear()
  vi.mocked(apiFetch).mockReset()
  vi.mocked(apiFetch).mockImplementation(async (input: RequestInfo | URL) => {
    const url = String(input)
    const key = Object.keys(ANSWERS).find((k) => url.includes(k))
    const a = key ? ANSWERS[key] : { ok: true, body: [] }
    if (a.malformed) return { ok: true, status: 200, json: async () => { throw new SyntaxError("bad json") } } as unknown as Response
    return { ok: a.ok, status: a.status ?? (a.ok ? 200 : 500), json: async () => a.body ?? {} } as unknown as Response
  })
})

afterEach(cleanup)

function openPalette() {
  return render(<CommandPalette open={true} onOpenChange={vi.fn()} />)
}

async function group(name: RegExp) {
  return screen.findByRole("group", { name })
}

function pagesCalls() {
  return vi.mocked(apiFetch).mock.calls.map((c) => String(c[0])).filter((u) => u.startsWith("/api/v1/pages"))
}

describe("⌘K — Pages group", () => {
  it("lists the pages the server authorised, each opening /pages/<slug>", async () => {
    ANSWERS["/api/v1/pages"] = { ok: true, body: [page({ slug: "fleet", name: "Fleet board" })] }
    openPalette()
    const g = await group(/^pages$/i)
    const row = within(g).getByRole("option")
    expect(row).toHaveTextContent("Fleet board")
    expect(row).toHaveAttribute("data-href", "/pages/fleet")
    // The generic destination is still there, in its own group.
    const nav = await group(/navigation/i)
    expect(within(nav).getByText("Pages")).toBeInTheDocument()
  })

  it("encodes a slug the URL cannot carry as-is", async () => {
    ANSWERS["/api/v1/pages"] = { ok: true, body: [page({ slug: "q&a/2026 sít", name: "Q&A" })] }
    openPalette()
    const g = await group(/^pages$/i)
    expect(within(g).getByRole("option")).toHaveAttribute("data-href", `/pages/${encodeURIComponent("q&a/2026 sít")}`)
  })

  it("keeps two pages with the same name apart by folder and by crew", async () => {
    ANSWERS["/api/v1/crews"] = { ok: true, body: [OPS_CREW] }
    ANSWERS["/api/v1/pages"] = {
      ok: true,
      body: [
        page({ slug: "status-ops", name: "Status", folder: { slug: "ops-folder", name: "Ops folder", icon: null, color: null } }),
        page({ slug: "status-crew", name: "Status", folder: null }),
        page({ slug: "status-mine", name: "Status", owner: "user/u-alice", owner_crew_slug: "" }),
      ],
    }
    openPalette()
    const g = await group(/^pages$/i)
    const rows = within(g).getAllByRole("option")
    expect(rows).toHaveLength(3)
    const text = rows.map((r) => r.textContent)
    expect(text[0]).toContain("Ops folder")
    // The index sends only the crew SLUG; the name comes from the crews list already fetched.
    expect(text[1]).toContain("Operations")
    expect(text[2]).toBe("Status")
    // Three distinct destinations.
    expect(new Set(rows.map((r) => r.getAttribute("data-href"))).size).toBe(3)
  })

  it("draws the page's own glyph, and marks a Pages App as an ordinary row", async () => {
    ANSWERS["/api/v1/pages"] = {
      ok: true,
      body: [
        page({ slug: "app", name: "Orders app", icon: "rocket", color: "violet", has_application: true, has_project: true }),
        page({ slug: "draft", name: "Draft only", has_project: true }),
      ],
    }
    openPalette()
    const g = await group(/^pages$/i)
    const rows = within(g).getAllByRole("option")
    expect(rows).toHaveLength(2)
    expect(rows[0].querySelector('[data-slot="page-glyph"][data-icon="rocket"][data-color="violet"]')).toBeInTheDocument()
    expect(rows[0]).toHaveTextContent("app")
    expect(rows[1]).toHaveAttribute("data-href", "/pages/draft")
  })

  it("asks for the whole index — no limit, the caller's workspace", async () => {
    openPalette()
    await group(/navigation/i)
    await waitFor(() => expect(pagesCalls()).toHaveLength(1))
    const url = pagesCalls()[0]
    expect(url).toBe("/api/v1/pages?workspace_id=ws-A")
    expect(url).not.toMatch(/limit/)
  })

  it("opens the page on click and on Enter, and remembers it under this identity", async () => {
    ANSWERS["/api/v1/pages"] = { ok: true, body: [page({ slug: "fleet", name: "Fleet board" })] }
    openPalette()
    const g = await group(/^pages$/i)
    fireEvent.click(within(g).getByRole("option"))
    expect(h.push).toHaveBeenCalledWith("/pages/fleet")
    expect(vi.mocked(localStorage.setItem)).toHaveBeenCalledWith(
      "crewship.palette.recent:u-alice:ws-A",
      JSON.stringify([{ href: "/pages/fleet", label: "Fleet board", group: "Pages" }]),
    )

    h.push.mockReset()
    cleanup()
    openPalette()
    const g2 = await group(/^pages$/i)
    const input = screen.getByPlaceholderText(/search issues/i)
    fireEvent.change(input, { target: { value: "fleet" } })
    await waitFor(() => expect(within(g2).getByRole("option")).toHaveAttribute("aria-selected", "true"))
    fireEvent.keyDown(input, { key: "Enter" })
    expect(h.push).toHaveBeenCalledWith("/pages/fleet")
  })
})

describe("⌘K — Pages list failures leave the rest of the palette standing", () => {
  it.each([
    ["403", { ok: false, status: 403, body: { error: "forbidden" } }],
    ["500", { ok: false, status: 500, body: { error: "boom" } }],
    ["malformed JSON", { ok: true, malformed: true }],
    ["an unrecognised envelope", { ok: true, body: { nope: true } }],
  ])("on %s: no Pages group, every other group intact, no page row in Recent", async (_label, answer) => {
    ANSWERS["/api/v1/pages"] = answer as Answer
    ANSWERS["/api/v1/crews"] = { ok: true, body: [OPS_CREW] }
    vi.mocked(localStorage.getItem).mockReturnValue(
      JSON.stringify([
        { href: "/pages/secret", label: "Secret page", group: "Pages" },
        { href: "/issues/ENG-1", label: "Rewrite the README", group: "Issues" },
      ]),
    )
    openPalette()
    const crews = await group(/^crews$/i)
    expect(within(crews).getByText("Operations")).toBeInTheDocument()
    expect(screen.queryByRole("group", { name: /^pages$/i })).not.toBeInTheDocument()
    // The stored page name is not shown as an available page against a
    // list that did not answer; the rest of Recent is untouched.
    const recent = await group(/recent/i)
    expect(within(recent).getByText("Rewrite the README")).toBeInTheDocument()
    expect(screen.queryByText("Secret page")).not.toBeInTheDocument()
  })
})

describe("⌘K — Recent page rows are checked against the authorised list", () => {
  beforeEach(() => {
    vi.mocked(localStorage.getItem).mockReturnValue(
      JSON.stringify([
        { href: "/pages/fleet", label: "Fleet board (old name)", group: "Pages" },
        { href: "/pages/gone", label: "Deleted page", group: "Pages" },
        // Recent holds five rows at most; a deep link onto a tab is still a page row.
        { href: "/pages/revoked?tab=ops", label: "Revoked page", group: "Pages" },
        { href: "/pages", label: "Pages", group: "Navigation" },
        { href: "/issues/ENG-1", label: "Rewrite the README", group: "Issues" },
      ]),
    )
  })

  it("keeps a page that is still in the list, under its current name; drops the deleted and the revoked", async () => {
    ANSWERS["/api/v1/pages"] = { ok: true, body: [page({ slug: "fleet", name: "Fleet board" })] }
    openPalette()
    await group(/^pages$/i)
    const recent = await group(/recent/i)
    const labels = within(recent).getAllByRole("option").map((r) => r.textContent)
    expect(labels.some((l) => l?.includes("Fleet board") && !l.includes("old name"))).toBe(true)
    expect(screen.queryByText("Deleted page")).not.toBeInTheDocument()
    expect(screen.queryByText("Revoked page")).not.toBeInTheDocument()
    // Rows that are not page deep links are not subject to the check.
    expect(within(recent).getByText("Rewrite the README")).toBeInTheDocument()
    expect(within(recent).getAllByRole("option").some((r) => r.getAttribute("data-href") === "/pages")).toBe(true)
    // One list request certified all of them — never one per row.
    expect(pagesCalls()).toHaveLength(1)
  })

  it("shows no page row while the list is still pending", async () => {
    let release: (() => void) | null = null
    vi.mocked(apiFetch).mockImplementation(async (input: RequestInfo | URL) => {
      if (String(input).startsWith("/api/v1/pages")) {
        await new Promise<void>((r) => { release = r })
        return ok([page({ slug: "fleet", name: "Fleet board" })])
      }
      return ok([])
    })
    openPalette()
    const recent = await group(/recent/i)
    expect(within(recent).getByText("Rewrite the README")).toBeInTheDocument()
    expect(screen.queryByText(/Fleet board/)).not.toBeInTheDocument()
    await waitFor(() => expect(release).not.toBeNull())
    release!()
    expect(await within(recent).findByText("Fleet board")).toBeInTheDocument()
  })
})

describe("⌘K — identity and workspace changes", () => {
  it("clears the rows at once on a workspace switch, and a late answer for the old one never lands", async () => {
    let resolveA: ((r: Response) => void) | null = null
    vi.mocked(apiFetch).mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const u = String(input)
      if (u.includes("ws-A")) {
        return new Promise<Response>((resolve, reject) => {
          if (u.startsWith("/api/v1/pages")) resolveA = resolve
          init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")))
        })
      }
      if (u.startsWith("/api/v1/pages")) return ok([page({ slug: "b-page", name: "Page of B" })])
      return ok([])
    })
    const view = openPalette()
    await waitFor(() => expect(resolveA).not.toBeNull())

    h.workspaceId = "ws-B"
    view.rerender(<CommandPalette open={true} onOpenChange={vi.fn()} />)
    resolveA!(ok([page({ slug: "a-page", name: "Ghost page of A" })]))
    await screen.findByText("Page of B")
    expect(screen.queryByText("Ghost page of A")).not.toBeInTheDocument()
    expect(pagesCalls()).toEqual(["/api/v1/pages?workspace_id=ws-A", "/api/v1/pages?workspace_id=ws-B"])
  })

  it("clears the rows when the workspace is lost, even before any answer arrives", async () => {
    ANSWERS["/api/v1/pages"] = { ok: true, body: [page({ slug: "fleet", name: "Fleet board" })] }
    ANSWERS["/api/v1/crews"] = { ok: true, body: [OPS_CREW] }
    const view = openPalette()
    await group(/^pages$/i)
    await group(/^crews$/i)

    h.workspaceId = null
    view.rerender(<CommandPalette open={true} onOpenChange={vi.fn()} />)
    await waitFor(() => {
      expect(screen.queryByText("Fleet board")).not.toBeInTheDocument()
      expect(screen.queryByText("Operations")).not.toBeInTheDocument()
    })
    // Nothing was asked for a workspace that does not exist.
    expect(pagesCalls()).toEqual(["/api/v1/pages?workspace_id=ws-A"])
  })

  it("re-reads Recent for the new user and clears the old rows when the session changes", async () => {
    const stored: Record<string, string> = {
      "crewship.palette.recent:u-alice:ws-A": JSON.stringify([{ href: "/issues/ENG-1", label: "Alice's issue", group: "Issues" }]),
      "crewship.palette.recent:u-bob:ws-A": JSON.stringify([{ href: "/issues/ENG-2", label: "Bob's issue", group: "Issues" }]),
    }
    vi.mocked(localStorage.getItem).mockImplementation((k: string) => stored[k] ?? null)
    ANSWERS["/api/v1/pages"] = { ok: true, body: [page({ slug: "fleet", name: "Fleet board" })] }
    const view = openPalette()
    await group(/^pages$/i)
    expect(screen.getByText("Alice's issue")).toBeInTheDocument()

    // Signed out: nothing of Alice's stays, nothing is fetched for nobody.
    h.userId = null
    h.authStatus = "unauthenticated"
    const callsBefore = vi.mocked(apiFetch).mock.calls.length
    view.rerender(<CommandPalette open={true} onOpenChange={vi.fn()} />)
    await waitFor(() => {
      expect(screen.queryByText("Alice's issue")).not.toBeInTheDocument()
      expect(screen.queryByText("Fleet board")).not.toBeInTheDocument()
    })
    expect(vi.mocked(apiFetch).mock.calls.length).toBe(callsBefore)

    // Bob signs in: Bob's history, Bob's lists.
    h.userId = "u-bob"
    h.authStatus = "authenticated"
    view.rerender(<CommandPalette open={true} onOpenChange={vi.fn()} />)
    expect(await screen.findByText("Bob's issue")).toBeInTheDocument()
    expect(screen.queryByText("Alice's issue")).not.toBeInTheDocument()
    await group(/^pages$/i)
  })

  it("neither reads nor writes Recent without a signed-in user", async () => {
    h.userId = null
    h.authStatus = "loading"
    vi.mocked(localStorage.getItem).mockReturnValue(
      JSON.stringify([{ href: "/issues/ENG-1", label: "Somebody's issue", group: "Issues" }]),
    )
    ANSWERS["/api/v1/pages"] = { ok: true, body: [page({ slug: "fleet", name: "Fleet board" })] }
    openPalette()
    const g = await group(/^pages$/i)
    expect(screen.queryByText("Somebody's issue")).not.toBeInTheDocument()
    fireEvent.click(within(g).getByRole("option"))
    expect(h.push).toHaveBeenCalledWith("/pages/fleet")
    expect(vi.mocked(localStorage.setItem)).not.toHaveBeenCalled()
  })
})

describe("⌘K — a closed palette costs nothing", () => {
  it("fetches only on open, and never again while closed", async () => {
    vi.useFakeTimers()
    try {
      const view = render(<CommandPalette open={false} onOpenChange={vi.fn()} />)
      await vi.advanceTimersByTimeAsync(60_000)
      expect(vi.mocked(apiFetch)).not.toHaveBeenCalled()

      view.rerender(<CommandPalette open={true} onOpenChange={vi.fn()} />)
      await vi.advanceTimersByTimeAsync(0)
      const opened = vi.mocked(apiFetch).mock.calls.length
      expect(pagesCalls()).toHaveLength(1)

      view.rerender(<CommandPalette open={false} onOpenChange={vi.fn()} />)
      await vi.advanceTimersByTimeAsync(120_000)
      expect(vi.mocked(apiFetch).mock.calls.length).toBe(opened)
    } finally {
      vi.useRealTimers()
    }
  })
})
