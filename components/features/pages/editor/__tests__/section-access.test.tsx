/**
 * Access — the editor's section for who reaches a Page.
 *
 * What is pinned here is what a wrong pixel costs someone:
 *
 *   · A producer token drawn as "Working" is a promise the list cannot make.
 *     The column knows one thing — nobody revoked the row — and the server
 *     re-derives the issuer's rights on every write, so a token whose issuer
 *     lost their grant is still "not revoked" and still rejected. An admin
 *     who reads that column as health stops policing the thing that actually
 *     decides.
 *   · A minted secret exists in exactly one response. If the form can bring
 *     it back after a dismiss, it was stored somewhere it should not be.
 *   · A section that renders nothing because one right is missing is V01: a
 *     reader who may legitimately SEE who reaches this Page is told nothing,
 *     including nothing about which right they lack.
 *   · A Save button here would say a grant can be staged. It cannot: every
 *     write on this screen is one immediate request of its own.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, fireEvent, cleanup, waitFor, within } from "@testing-library/react"

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), message: vi.fn() } }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))

import { EditorAccessSection } from "@/components/features/pages/editor/section-access"
import { NO_PAGE_CAPABILITIES, type PageCapabilities } from "@/lib/pages/editor-contract"
import type { WirePageDetail } from "@/hooks/use-page-grants"

// ── Fixtures ───────────────────────────────────────────────────────────────

const PAGE: WirePageDetail = {
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
  ],
}

const WEBHOOKS = {
  page: "fleet-201",
  webhooks: [
    {
      id: "wh1",
      panel: "sluzby",
      name: "Nightly collector",
      created_by: "ada@example.com",
      created_at: "2026-08-01T09:00:00Z",
      last_fired_at: "2026-09-09T09:00:00Z",
      fire_count: 12,
      live: true,
    },
  ],
}

const LINKS = {
  page: "fleet-201",
  tokens: [
    {
      id: "pl1",
      expires_at: "2026-10-01T09:00:00Z",
      show_provenance: false,
      has_password: false,
      created_by: "ada@example.com",
      created_at: "2026-08-01T09:00:00Z",
      live: true,
      panels: ["sluzby"],
    },
  ],
}

const SECRET_URL = "https://crewship.example/hook/wh2/9f3a-never-shown-again"

const CAPABLE: PageCapabilities = {
  ...NO_PAGE_CAPABILITIES,
  loaded: true,
  mayEditMetadata: true,
  mayEditDocument: true,
  mayManageAccess: true,
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

function mount(capabilities: Partial<PageCapabilities> = {}, grants: unknown = GRANTS) {
  const calls: Array<{ method: string; url: string }> = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      const method = (init?.method ?? "GET").toUpperCase()
      calls.push({ method, url })
      // The Page itself. Matched before the sub-resources so a DELETE of one
      // of those is not mistaken for the end of the Page.
      if (
        method === "DELETE" &&
        !url.includes("/webhooks") &&
        !url.includes("/public") &&
        !url.includes("/grants")
      ) {
        return jsonResponse(200, {})
      }
      if (url.includes("/webhooks")) {
        if (method === "POST") {
          return jsonResponse(200, {
            id: "wh2",
            panel: "sluzby",
            name: "CI push",
            token: "tok-9f3a",
            url: SECRET_URL,
            created_by: "ada@example.com",
            created_at: "2026-09-10T09:00:00Z",
            fire_count: 0,
            live: true,
          })
        }
        return jsonResponse(200, WEBHOOKS)
      }
      if (url.includes("/public")) return jsonResponse(200, LINKS)
      if (url.includes("/grants")) return jsonResponse(200, grants)
      return jsonResponse(404, { error: `unrouted ${method} ${url}` })
    }),
  )

  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  const onDirtyChange = vi.fn()
  const onLeaveEditor = vi.fn()
  const onPageDeleted = vi.fn()
  render(
    <QueryClientProvider client={qc}>
      <EditorAccessSection
        workspaceId="ws-1"
        slug="fleet-201"
        page={PAGE}
        capabilities={{ ...CAPABLE, ...capabilities }}
        onNavigate={vi.fn()}
        pane="section"
        onPaneChange={vi.fn()}
        onLeaveEditor={onLeaveEditor}
        onPageDeleted={onPageDeleted}
        onDirtyChange={onDirtyChange}
      />
    </QueryClientProvider>,
  )
  return { calls, onDirtyChange, onLeaveEditor, onPageDeleted }
}

function sectionText(): string {
  return document.querySelector("[data-slot='editor-section-access']")!.textContent ?? ""
}

beforeEach(() => cleanup())
afterEach(() => vi.unstubAllGlobals())

// ── 1. The three sub-sections, in order ────────────────────────────────────

describe("the Access section", () => {
  it("renders people, then producer tokens, then public links", async () => {
    mount()
    await waitFor(() => expect(document.querySelectorAll("[data-slot='page-grant']")).toHaveLength(1))

    const text = sectionText()
    const people = text.indexOf("People and crews")
    const tokens = text.indexOf("Producer tokens")
    const links = text.indexOf("Public links")
    expect(people).toBeGreaterThanOrEqual(0)
    expect(tokens).toBeGreaterThan(people)
    expect(links).toBeGreaterThan(tokens)

    // Webhooks moved OUT of Data & actions and were renamed with the move:
    // the old title would send a reader looking for the old home.
    expect(text).not.toContain("Webhooks")

    // Export and Delete have nowhere else to live now the settings modal is
    // gone, and both are access-shaped.
    expect(text).toContain("Export")
    expect(text).toContain("Delete this Page")
  })

  it("says every write here is immediate, and never offers a Save", async () => {
    mount()
    await waitFor(() => expect(document.querySelectorAll("[data-slot='page-grant']")).toHaveLength(1))

    // SAVE_EFFECT_NOTE["immediate-grant"], verbatim from the contract.
    expect(sectionText()).toContain(
      "This is written on its own, immediately. It is never part of a publication.",
    )
    expect(screen.queryByRole("button", { name: /^save/i })).toBeNull()
  })

  it("does not claim that administering access opens the other sections", async () => {
    mount()
    await waitFor(() => expect(document.querySelectorAll("[data-slot='page-grant']")).toHaveLength(1))

    expect(sectionText()).toContain(
      "Administering access does not by itself let you edit this Page",
    )
    expect(sectionText()).not.toMatch(/unlocks?/i)
  })

  it("says what a public link exposes, and that publishing an application is not that", async () => {
    mount()
    await waitFor(() => expect(document.querySelectorAll("[data-slot='page-grant']")).toHaveLength(1))

    const text = sectionText()
    // The public DTO carries panels, never the application artifact, and the
    // two acts share the word "publish" — which is exactly how somebody ends
    // up believing their custom application is on the internet.
    expect(text).toContain("panels marked public")
    expect(text).toContain("Publishing a custom application is not the same thing")
    expect(text).toContain("a public link never serves it")
  })

  it("hands the deleted Page back to the shell instead of routing itself", async () => {
    const { calls, onPageDeleted } = mount()
    await waitFor(() => expect(document.querySelectorAll("[data-slot='page-grant']")).toHaveLength(1))

    // Deleting is not gated on mayManageAccess — ending a Page is the
    // owner's right, which PageCapabilities does not model — so the control
    // is here for a caller who administers access, and the server decides.
    fireEvent.click(screen.getByRole("button", { name: /delete this page/i }))
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.change(within(dialog).getByLabelText("Type the page slug to confirm"), {
      target: { value: "fleet-201" },
    })
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }))

    await waitFor(() => expect(onPageDeleted).toHaveBeenCalledTimes(1))
    expect(calls.some((c) => c.method === "DELETE")).toBe(true)
    // The section must not navigate on its own: in this shell the router
    // would unmount the rail the editor is careful not to disturb.
    expect(sectionText()).toBeTruthy()
  })
})

// ── 2. What a token row may and may not claim ──────────────────────────────

describe("producer tokens", () => {
  it("says 'Not revoked', never 'Working', and carries the recheck sentence", async () => {
    mount()
    await waitFor(() => expect(document.querySelector("[data-slot='page-webhook']")).toBeTruthy())
    const row = document.querySelector("[data-slot='page-webhook']")!

    expect(row.textContent).toContain("Not revoked")
    // The list — not a tooltip — carries the property that changes what an
    // admin thinks they must police.
    expect(sectionText()).toContain("the server rechecks the issuer")
    expect(sectionText()).toContain("rights on every write")
    expect(sectionText()).toContain("bound to")
    expect(sectionText()).toContain("one panel")

    // "Working" would be a claim about the next write. Nothing here knows
    // that.
    expect(document.body.textContent).not.toContain("Working")

    // A past success, named as one.
    expect(row.textContent).toContain("last accepted")

    // Nothing invents an expiry, a rotation, or a second look at the secret.
    expect(sectionText()).not.toMatch(/rotate|expires in|show token again/i)
  })

  it("shows a minted secret once, and cannot bring it back after dismiss", async () => {
    mount()
    await waitFor(() => expect(document.querySelector("[data-slot='page-webhook']")).toBeTruthy())

    fireEvent.click(screen.getByRole("button", { name: /mint/i }))

    await waitFor(() => expect(screen.getByText(SECRET_URL)).toBeTruthy())
    expect(sectionText()).toContain("it is shown once")

    fireEvent.click(screen.getByRole("button", { name: "I have it" }))
    await waitFor(() => expect(screen.queryByText(SECRET_URL)).toBeNull())

    // The list refetches after the mint. A refetched row must not carry the
    // secret back: what is stored server-side is a hash.
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='page-webhook']").length).toBeGreaterThan(0),
    )
    expect(document.body.textContent).not.toContain(SECRET_URL)
    expect(document.body.textContent).not.toContain("tok-9f3a")
  })
})

// ── 3. An empty ACL says different things to different readers ─────────────

const NO_GRANTS = { page: "fleet-201", grants: [] }

describe("a page with no grants on it", () => {
  it("offers the form to somebody who may use it", async () => {
    mount({}, NO_GRANTS)
    await waitFor(() => expect(screen.getByText(/No grants on this page/)).toBeTruthy())

    // The reachability half is true for everybody; the call to action is
    // only true for a reader who has the form.
    expect(sectionText()).toContain("reachable by its owner")
    expect(sectionText()).toContain("Widen it with the form below")
    expect(sectionText()).toContain("crewship page grant")
  })

  it("does not send a reader without the right to a form they cannot see", async () => {
    mount({ mayManageAccess: false }, NO_GRANTS)
    await waitFor(() => expect(screen.getByText(/No grants on this page/)).toBeTruthy())

    expect(sectionText()).toContain("reachable by its owner")
    // Pointing at a form that is not rendered for them makes a missing
    // right read as a missing feature — and the CLI would refuse them for
    // the same reason, so it is not an escape hatch either.
    expect(sectionText()).not.toContain("Widen it with the form below")
    expect(sectionText()).not.toContain("crewship page grant")
    // What they are missing is named instead.
    expect(sectionText()).toContain("Only its owner or a workspace admin")
  })
})

// ── 4. A missing right hides controls, not the section ─────────────────────

describe("without mayManageAccess", () => {
  it("keeps the whole section readable and names the missing right on each card", async () => {
    mount({ mayManageAccess: false })
    await waitFor(() => expect(document.querySelectorAll("[data-slot='page-grant']")).toHaveLength(1))

    // Still the three sub-sections, still the rows: who reaches this Page is
    // exactly what such a reader legitimately needs.
    const text = sectionText()
    expect(text).toContain("People and crews")
    expect(text).toContain("Producer tokens")
    expect(text).toContain("Public links")
    expect(document.querySelector("[data-slot='page-webhook']")).toBeTruthy()

    // Three refusals, three different missing rights — not one shared
    // sentence, because the reader has to know which one to ask for.
    const refusals = Array.from(document.querySelectorAll("[data-slot='control-refusal']"))
    expect(refusals).toHaveLength(3)
    expect(refusals[0].textContent).toContain("Only its owner or a workspace admin")
    expect(refusals[1].textContent).toContain("issues a credential")
    expect(refusals[2].textContent).toContain("publish or withdraw a public link")

    // The writes are gone rather than disabled: a greyed control says
    // "later, maybe", which is not what a missing right means.
    expect(screen.queryByRole("button", { name: /^mint$/i })).toBeNull()
    expect(screen.queryByRole("button", { name: /^grant$/i })).toBeNull()
    expect(screen.queryByRole("button", { name: /^publish$/i })).toBeNull()
    expect(screen.queryByLabelText(/^Revoke produce from/)).toBeNull()

    // And still no Save, and still no claim about the other sections.
    expect(screen.queryByRole("button", { name: /^save/i })).toBeNull()
    expect(text).not.toMatch(/unlocks?/i)
  })

  it("reports the section as never dirty", async () => {
    const { onDirtyChange } = mount({ mayManageAccess: false })
    await waitFor(() => expect(onDirtyChange).toHaveBeenCalledWith(false))
    expect(onDirtyChange.mock.calls.every(([dirty]) => dirty === false)).toBe(true)
  })
})
