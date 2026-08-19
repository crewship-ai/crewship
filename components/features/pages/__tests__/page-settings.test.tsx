/**
 * Page settings — the ACL and the page's own facts (PRD §7.1b, §10b.1).
 *
 * What is pinned here is what a wrong pixel costs someone:
 *
 *   · A grant with no issuer is an ACL nobody can audit (§7.1b), and
 *     `granted_by_user_id` is NOT NULL precisely so this line can always be
 *     drawn.
 *   · A grant drawn as though it worked, when the server has already declared
 *     it inert, is the exact failure `inertReason()` exists to prevent —
 *     somebody believes access was granted. The reason must arrive verbatim.
 *   · `read` opens the page and unseals nothing. A card that warned it decided
 *     nothing would now be lying about a working grant; a card that drew it as
 *     full access would be lying the other way.
 *   · A revoke that fires on the first click is one misclick away from
 *     locking a crew out of a page.
 *   · A refused write that clears the form, or removes the row it failed to
 *     remove, is #1563 all over again: rule 1 (check ok first) and rule 3
 *     (never destroy the state a retry needs).
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, fireEvent, cleanup, waitFor, within } from "@testing-library/react"

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), message: vi.fn() } }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))

import { PagesLayout } from "@/components/features/pages/pages-layout"
import { PageSettings } from "@/components/features/pages/page-settings"
import { toPageGrant, type WirePageDetail } from "@/hooks/use-page-grants"

// ── Fixtures ───────────────────────────────────────────────────────────────

const INERT_REASON = "the human who issued it is no longer a member of this workspace"

const GRANTS = {
  page: "fleet-201",
  grants: [
    {
      subject_type: "agent",
      subject: "watcher",
      subject_id: "ag1",
      level: "produce",
      panels: ["sluzby"],
      granted_by: "ada@example.com",
      granted_by_user_id: "u1",
      granted_at: "2026-08-01T09:00:00Z",
      live: true,
    },
    {
      subject_type: "crew",
      subject: "lookout",
      subject_id: "cr1",
      level: "read",
      granted_by: "bob@example.com",
      granted_by_user_id: "u2",
      granted_at: "2026-07-20T09:00:00Z",
      live: false,
      inert_reason: INERT_REASON,
    },
  ],
}

const VERSIONS = {
  page: "fleet-201",
  retained: 50,
  versions: [
    {
      seq: 4,
      created_at: "2026-08-10T08:00:00Z",
      author: "user/u1",
      author_label: "ada@example.com",
      name: "Flotila .201",
      panel_count: 2,
      current: true,
    },
    {
      seq: 3,
      created_at: "2026-08-02T08:00:00Z",
      author: "agent/watcher",
      author_label: "watcher",
      name: "Flotila .201",
      panel_count: 1,
      current: false,
    },
  ],
}

const CREW_OWNED: WirePageDetail = {
  id: "cpage1",
  slug: "fleet-201",
  name: "Flotila .201",
  description: "Ship telemetry",
  owner: "crew/lookout",
  panels: [
    { id: "sluzby", schema: "status.v1", span: 8 },
    { id: "zatizeni", schema: "metric.v1", span: 4 },
  ],
  created_at: "2026-07-01T08:00:00Z",
  updated_at: "2026-08-10T08:00:00Z",
}

const USER_OWNED: WirePageDetail = {
  id: "cpage2",
  slug: "nightly-close",
  name: "Nightly close",
  owner: "user/ada@example.com",
  panels: [{ id: "total", schema: "metric.v1", span: 12 }],
  created_at: "2026-07-04T08:00:00Z",
  updated_at: "2026-08-01T08:00:00Z",
}

// ── Harness ────────────────────────────────────────────────────────────────

function jsonResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: { get: () => null },
    json: async () => body,
    text: async () => JSON.stringify(body),
  } as unknown as Response
}

interface Routes {
  /** Answer for `GET …/grants`. */
  grants?: Response
  /** Answer for `GET …/versions`. */
  versions?: Response
  /** Answer for `DELETE …/grants`. */
  revoke?: Response
  /** Answer for `PUT …/grants`. */
  grant?: Response
  /** Answer for `POST …/rollback`. */
  rollback?: Response
}

function mount(page: WirePageDetail, routes: Routes = {}) {
  const calls: Array<{ method: string; url: string }> = []
  const mockFetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = (init?.method ?? "GET").toUpperCase()
    calls.push({ method, url })
    if (url.includes("/rollback")) return routes.rollback ?? jsonResponse(200, { rolled_back_to: 3 })
    if (url.includes("/versions")) return routes.versions ?? jsonResponse(200, VERSIONS)
    if (url.includes("/grants")) {
      if (method === "DELETE") return routes.revoke ?? jsonResponse(200, { page: page.slug, grants: [] })
      if (method === "PUT") return routes.grant ?? jsonResponse(200, GRANTS)
      return routes.grants ?? jsonResponse(200, GRANTS)
    }
    return jsonResponse(404, { error: `unrouted ${method} ${url}` })
  })
  vi.stubGlobal("fetch", mockFetch)

  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  render(
    <QueryClientProvider client={qc}>
      <PageSettings workspaceId="ws-1" slug={page.slug!} page={page} onClose={() => {}} />
    </QueryClientProvider>,
  )
  return { calls, mockFetch }
}

function grantRows(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>("[data-slot='page-grant']"))
}

beforeEach(() => cleanup())
afterEach(() => vi.unstubAllGlobals())

// ── 1. Grants render with issuer and verdict ───────────────────────────────

describe("the Access card", () => {
  it("names the human who issued each grant, and the server's live verdict", async () => {
    mount(CREW_OWNED)
    await waitFor(() => expect(grantRows()).toHaveLength(2))

    const live = grantRows().find((r) => r.dataset.live === "true")!
    // §7.1b rule 1: only a human issues a grant, and the audit trail is the
    // reason granted_by_user_id is NOT NULL.
    expect(live.textContent).toContain("Granted by")
    expect(live.textContent).toContain("ada@example.com")
    expect(live.textContent).toContain("live")
    // A produce grant says what it is scoped to; the scope is the whole point
    // of the level (§7.1b — one agent cannot overwrite another's panel).
    expect(live.textContent).toContain("panels: sluzby")

    const inert = grantRows().find((r) => r.dataset.live === "false")!
    expect(inert.textContent).toContain("bob@example.com")
  })

  it("draws an inert grant as inert and repeats the server's reason verbatim", async () => {
    mount(CREW_OWNED)
    await waitFor(() => expect(grantRows()).toHaveLength(2))

    const inert = grantRows().find((r) => r.dataset.live === "false")
    expect(inert).toBeTruthy()
    // Not "inert" alone: the reason is the only thing that says what to fix.
    expect(inert!.textContent).toContain(`inert — ${INERT_REASON}`)
    // …and the live row is not smeared with the same warning.
    const live = grantRows().find((r) => r.dataset.live === "true")!
    expect(live.textContent).not.toContain("inert")
  })

  it("draws a `read` row as an ordinary live grant, with no warning beside it", async () => {
    mount(CREW_OWNED)
    await waitFor(() => expect(grantRows()).toHaveLength(2))

    const read = grantRows().find((r) => r.dataset.level === "read")!
    // The warning that used to sit here said `read` decided nothing. It decides
    // page reach now, and a card still carrying that sentence would tell an
    // owner their working grant is dead.
    expect(read.querySelector("[data-slot='grant-read-caveat']")).toBeNull()
    expect(read.textContent).not.toContain("decides nothing")
    expect(read.textContent).toContain("lookout")
  })

  it("shows the server's own sentence when the ACL is not this caller's to read", async () => {
    const refusal =
      "only the page owner or a workspace admin may read or change this page's grants; " +
      "a `write` grant rearranges the page, it does not widen who reaches it (§7.1 rule 3)"
    mount(CREW_OWNED, { grants: jsonResponse(403, { error: refusal }) })

    await waitFor(() => expect(screen.getByText(refusal)).toBeTruthy())
    expect(grantRows()).toHaveLength(0)
  })
})

// ── 2. Revoke confirms ─────────────────────────────────────────────────────

describe("revoke", () => {
  it("asks before removing anything", async () => {
    const { calls } = mount(CREW_OWNED)
    await waitFor(() => expect(grantRows()).toHaveLength(2))

    fireEvent.click(screen.getByLabelText("Revoke produce from agent/watcher"))

    // Nothing has been sent yet — the first click opens a question.
    expect(calls.filter((c) => c.method === "DELETE")).toHaveLength(0)
    const dialog = await screen.findByRole("alertdialog")
    expect(dialog.textContent).toContain("Revoke this grant")
    expect(dialog.textContent).toContain("agent/watcher")

    fireEvent.click(within(dialog).getByRole("button", { name: "Revoke" }))

    await waitFor(() => expect(calls.filter((c) => c.method === "DELETE")).toHaveLength(1))
    const sent = calls.find((c) => c.method === "DELETE")!.url
    expect(sent).toContain("subject_type=agent")
    expect(sent).toContain("subject=watcher")
    expect(sent).toContain("level=produce")
  })

  it("removes nothing and cancels cleanly when the question is declined", async () => {
    const { calls } = mount(CREW_OWNED)
    await waitFor(() => expect(grantRows()).toHaveLength(2))

    fireEvent.click(screen.getByLabelText("Revoke produce from agent/watcher"))
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }))

    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())
    expect(calls.filter((c) => c.method === "DELETE")).toHaveLength(0)
    expect(grantRows()).toHaveLength(2)
  })

  it("shows the server's sentence on a refusal and changes nothing on screen", async () => {
    const refusal =
      "only a human may issue or revoke a page grant (§7.1b rule 1); an agent with `write` " +
      "may rebuild the page but can never widen who reaches it"
    const { calls } = mount(CREW_OWNED, { revoke: jsonResponse(403, { error: refusal }) })
    await waitFor(() => expect(grantRows()).toHaveLength(2))

    fireEvent.click(screen.getByLabelText("Revoke produce from agent/watcher"))
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "Revoke" }))

    // Rule 2: the server's own words, not "HTTP 403".
    await waitFor(() => expect(screen.getByText(refusal)).toBeTruthy())
    // Rules 1 and 3: nothing was invalidated, nothing was optimistically
    // removed. Both rows are exactly where they were.
    expect(grantRows()).toHaveLength(2)
    expect(grantRows().find((r) => r.dataset.level === "produce")!.textContent).toContain("watcher")
    // One DELETE, and no refetch of the list it did not change.
    expect(calls.filter((c) => c.method === "DELETE")).toHaveLength(1)
    expect(calls.filter((c) => c.method === "GET" && c.url.includes("/grants"))).toHaveLength(1)
  })
})

// ── 3. A refused grant keeps what a retry needs ────────────────────────────

describe("issuing a grant", () => {
  it("keeps the typed reference and shows the refusal when the server says no", async () => {
    const refusal = "no member of this workspace matches user \"ghost@example.com\""
    mount(CREW_OWNED, { grant: jsonResponse(400, { error: refusal }) })
    await waitFor(() => expect(grantRows()).toHaveLength(2))

    const subject = screen.getByLabelText("Subject") as HTMLInputElement
    fireEvent.change(subject, { target: { value: "ghost@example.com" } })
    fireEvent.change(screen.getByLabelText("Level"), { target: { value: "write" } })
    fireEvent.click(screen.getByRole("button", { name: /grant/i }))

    await waitFor(() => expect(screen.getByText(refusal)).toBeTruthy())
    // #1563 rule 3: the buffer a retry needs is untouched.
    expect(subject.value).toBe("ghost@example.com")
    expect(grantRows()).toHaveLength(2)
  })

  it("offers the panel scope for produce and for nothing else", async () => {
    mount(CREW_OWNED)
    await waitFor(() => expect(grantRows()).toHaveLength(2))

    // The form opens on produce, where a scope means something.
    expect(screen.getByLabelText("Panels")).toBeTruthy()
    fireEvent.change(screen.getByLabelText("Level"), { target: { value: "read" } })
    expect(screen.queryByLabelText("Panels")).toBeNull()
  })
})

// ── 4. General information ─────────────────────────────────────────────────

describe("the General card", () => {
  it("renders the owner and the panel count for a crew-owned page", async () => {
    mount(CREW_OWNED)
    const owner = await waitFor(() => document.querySelector("[data-fact='owner']")!)
    // §7.1 rule 1: owner_user_id XOR owner_crew_id, and which arc it is
    // changes what the line means.
    expect(owner.textContent).toContain("crew")
    expect(owner.textContent).toContain("lookout")
    expect(owner.textContent).not.toContain("user")

    expect(document.querySelector("[data-fact='panels']")!.textContent).toContain("2")
    expect(document.querySelector("[data-fact='slug']")!.textContent).toContain("fleet-201")
    expect(document.querySelector("[data-fact='description']")!.textContent).toContain("Ship telemetry")
  })

  it("renders the owner and the panel count for a user-owned page", async () => {
    mount(USER_OWNED)
    const owner = await waitFor(() => document.querySelector("[data-fact='owner']")!)
    expect(owner.textContent).toContain("user")
    expect(owner.textContent).toContain("ada@example.com")
    expect(owner.textContent).not.toContain("crew")

    expect(document.querySelector("[data-fact='panels']")!.textContent).toContain("1")
    // §9b.4: no description is `—`, never a blank that reads as a bug.
    expect(
      document.querySelector("[data-fact='description'] [data-slot='fact-value']")!.textContent,
    ).toBe("—")
  })

  it("lists the version history with who authored each version", async () => {
    mount(CREW_OWNED)
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='page-version']")).toHaveLength(2),
    )
    const rows = Array.from(document.querySelectorAll<HTMLElement>("[data-slot='page-version']"))
    expect(rows[0].textContent).toContain("v4")
    expect(rows[0].textContent).toContain("ada@example.com")
    expect(rows[0].textContent).toContain("current")
    // A version authored by an agent is credited to the agent (§10b.1 — the
    // one who breaks it is rarely the one who notices).
    expect(rows[1].textContent).toContain("watcher")
  })

  it("asks before rolling back, and names what a rollback does to the data", async () => {
    const { calls } = mount(CREW_OWNED)
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='page-version']")).toHaveLength(2),
    )

    fireEvent.click(screen.getByLabelText("Roll back to version 3"))
    expect(calls.filter((c) => c.url.includes("/rollback"))).toHaveLength(0)

    const dialog = await screen.findByRole("alertdialog")
    // §10b.1: rollback restores structure, never numbers.
    expect(dialog.textContent).toContain("arrives with no data")
    expect(dialog.textContent).toContain("appends a new version")

    fireEvent.click(within(dialog).getByRole("button", { name: "Roll back" }))
    await waitFor(() => expect(calls.filter((c) => c.url.includes("/rollback"))).toHaveLength(1))
    expect(calls.find((c) => c.url.includes("/rollback"))!.method).toBe("POST")
  })
})

// ── 5. Reachable from the shell ────────────────────────────────────────────

describe("the /pages shell", () => {
  it("opens settings from the SubBar, beside Edit", async () => {
    const mockFetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      const method = (init?.method ?? "GET").toUpperCase()
      if (url.includes("/versions")) return jsonResponse(200, VERSIONS)
      if (url.includes("/grants")) return jsonResponse(200, GRANTS)
      if (url.includes("/api/v1/pages/")) return jsonResponse(200, CREW_OWNED)
      if (method === "GET") return jsonResponse(200, [CREW_OWNED])
      return jsonResponse(404, { error: `unrouted ${method} ${url}` })
    })
    vi.stubGlobal("fetch", mockFetch)

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
    render(
      <QueryClientProvider client={qc}>
        <PagesLayout workspaceId="ws-1" slug="fleet-201" />
      </QueryClientProvider>,
    )

    const bar = screen.getByLabelText("Pages")
    // Enabled only once the page itself has loaded — there is nothing to show
    // the settings of until then.
    const settings = await waitFor(() => {
      const button = within(bar).getByRole("button", { name: /settings/i }) as HTMLButtonElement
      expect(button.disabled).toBe(false)
      return button
    })
    // Beside Edit, not inside it: the editor owns one document, this owns
    // rows in two tables no document can express.
    expect(within(bar).getByRole("button", { name: /edit/i })).toBeTruthy()

    fireEvent.click(settings)
    expect(await screen.findByRole("dialog", { name: "Settings for fleet-201" })).toBeTruthy()
    await waitFor(() => expect(grantRows()).toHaveLength(2))
  })
})

// ── 6. The normaliser's two honesty rules ──────────────────────────────────

describe("toPageGrant", () => {
  it("treats an unreadable verdict as inert, never as live", () => {
    // The server always sends `live`. If a build ever reads a row without
    // one, the safe direction of the mistake is the pessimistic one: showing
    // a grant as working when it is not is the failure inertReason() exists
    // to prevent.
    expect(toPageGrant({ subject_type: "user", subject: "a@b.c", level: "write" }).live).toBe(false)
    expect(toPageGrant({ live: true } as never).live).toBe(true)
    // Truthy-but-not-true is not a verdict.
    expect(toPageGrant({ live: "yes" } as never).live).toBe(false)
  })

  it("keeps the produce scope empty when the grant covers the whole page", () => {
    // A null panel_ids column means "every panel". Inventing a list here
    // would describe an agent as scoped to nothing.
    expect(toPageGrant({ level: "produce" }).panels).toEqual([])
    expect(toPageGrant({ level: "produce", panels: ["a", " ", "b"] }).panels).toEqual(["a", "b"])
  })

  it("drops the inert reason once the server says the grant is live", () => {
    const g = toPageGrant({ live: true, inert_reason: "stale text from a previous read" })
    expect(g.inertReason).toBeNull()
  })
})
