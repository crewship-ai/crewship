import { beforeEach, describe, expect, it, vi } from "vitest"
import { render, waitFor } from "@testing-library/react"

// /journal is a redirect into Activity (F-02). The mapping table itself is
// lib/__tests__/journal-redirect.test.ts; this suite checks the page applies
// it with the reader's role, waits for what it needs, and never shows the
// technical log on its own.

const m = vi.hoisted(() => ({
  search: "",
  replace: vi.fn(),
  push: vi.fn(),
  role: "OWNER" as string | null,
  loading: false,
  lookup: {
    crews: new Map(),
    agents: new Map(),
    missions: new Map(),
    loading: false,
    refresh: () => {},
  } as {
    crews: Map<string, { id: string; slug: string }>
    agents: Map<string, { id: string; slug: string }>
    missions: Map<string, { id: string; identifier?: string }>
    loading: boolean
    refresh: () => void
  },
}))

vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: m.replace, push: m.push }),
  useSearchParams: () => new URLSearchParams(m.search),
  usePathname: () => "/journal",
}))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: m.role, loading: m.loading }) }))
vi.mock("@/hooks/use-journal-lookup", () => ({ useJournalLookup: () => m.lookup }))

import JournalPage from "../page"

beforeEach(() => {
  m.replace.mockClear()
  m.push.mockClear()
  m.role = "OWNER"
  m.loading = false
  m.search = ""
  m.lookup = { crews: new Map(), agents: new Map(), missions: new Map(), loading: false, refresh: () => {} }
})

describe("/journal redirects into Activity", () => {
  it.each([
    ["OWNER", "", "/activity?section=log"],
    ["ADMIN", "tab=timeline&trace_id=run_1&severity=error", "/activity?section=log&trace_id=run_1&severity=error"],
    ["ADMIN", "tab=runs&run_status=FAILED", "/activity?section=runs&run_status=FAILED"],
    ["OWNER", "tab=spend", "/activity?mode=spend"],
    ["ADMIN", "tab=spend", "/activity?mode=spend"],
    ["MANAGER", "", "/activity"],
    ["MEMBER", "", "/activity"],
    ["VIEWER", "", "/activity"],
    ["MEMBER", "tab=spend", "/activity"],
    ["MANAGER", "tab=spend", "/activity"],
    ["MEMBER", "tab=runs&run_status=FAILED", "/activity?what=work&range=24h&view=failed"],
    ["MEMBER", "trace_id=run_1&severity=error", "/activity?run=run_1"],
  ])("%s /journal?%s → %s", async (role, search, target) => {
    m.role = role
    m.search = search
    const view = render(<JournalPage />)
    await waitFor(() => expect(m.replace).toHaveBeenCalledWith(target))
    // The page itself draws nothing — no log, no gate.
    expect(view.container.textContent).toBe("")
  })

  it("waits for the role before deciding", async () => {
    m.loading = true
    m.role = null
    m.search = "tab=spend"
    const view = render(<JournalPage />)
    await new Promise((r) => setTimeout(r, 10))
    expect(m.replace).not.toHaveBeenCalled()
    m.loading = false
    m.role = "ADMIN"
    view.rerender(<JournalPage />)
    await waitFor(() => expect(m.replace).toHaveBeenCalledWith("/activity?mode=spend"))
  })

  it("treats a missing role as below ADMIN", async () => {
    m.role = null
    m.search = "tab=spend"
    render(<JournalPage />)
    await waitFor(() => expect(m.replace).toHaveBeenCalledWith("/activity"))
  })

  it("resolves a member's crew and agent slugs before redirecting (Z29)", async () => {
    m.role = "MEMBER"
    m.search = "crew=engineering&agent=robin"
    m.lookup = { ...m.lookup, loading: true }
    const view = render(<JournalPage />)
    await new Promise((r) => setTimeout(r, 10))
    expect(m.replace).not.toHaveBeenCalled()
    m.lookup = {
      crews: new Map([["crew-1", { id: "crew-1", slug: "engineering" }]]),
      agents: new Map([["agent-7", { id: "agent-7", slug: "robin" }]]),
      missions: new Map(),
      loading: false,
      refresh: () => {},
    }
    view.rerender(<JournalPage />)
    await waitFor(() => expect(m.replace).toHaveBeenCalledWith("/activity?crew=crew-1&agent=agent-7"))
  })

  it("does not wait for the lookup when an admin keeps the slugs verbatim", async () => {
    m.role = "ADMIN"
    m.search = "crew=engineering"
    m.lookup = { ...m.lookup, loading: true }
    render(<JournalPage />)
    await waitFor(() => expect(m.replace).toHaveBeenCalledWith("/activity?section=log&crew=engineering"))
  })
})
