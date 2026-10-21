import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, waitFor, within } from "@testing-library/react"

import { CrewMemoryReview } from "@/components/features/crews/crew-memory-review"

afterEach(() => { cleanup(); vi.restoreAllMocks() })

const item = (id: string, proposal: string) => ({
  id, kind: "memory_consolidation", state: "unread", source_id: proposal, workspace_id: "ws1",
  title: "Memory consolidation: 3 rules pending review", created_at: "2026-10-17T09:00:00Z",
  payload: { proposal_id: proposal, rules_count: 3, entries_scanned: 40 },
})
const explain = (proposal: string, crew: string, status = "pending") => ({
  proposal_id: proposal, crew_id: crew, status, rules_count: 3, entries_scanned: 40, created_at: "2026-10-17T09:00:00Z",
})

type Call = { url: string; method: string; body: unknown }
function server(opts: { inbox?: unknown[]; explain?: Record<string, unknown>; inboxStatus?: number; diff?: unknown } = {}) {
  const calls: Call[] = []
  let rows = opts.inbox ?? [item("i1", "mp_1"), item("i2", "mp_2")]
  vi.spyOn(global, "fetch").mockImplementation(async (input, init) => {
    const url = String(input)
    const method = init?.method ?? "GET"
    calls.push({ url, method, body: init?.body ? JSON.parse(String(init.body)) : undefined })
    if (url.includes("/api/v1/inbox") && method === "GET") {
      if (opts.inboxStatus) return new Response("{}", { status: opts.inboxStatus })
      return new Response(JSON.stringify({ rows }))
    }
    if (url.includes("/explain")) {
      const id = /proposed\/([^/]+)\/explain/.exec(url)![1]
      const e = (opts.explain ?? { mp_1: explain("mp_1", "c1"), mp_2: explain("mp_2", "c2") })[id]
      return e ? new Response(JSON.stringify(e)) : new Response("{}", { status: 404 })
    }
    if (url.includes("/diff")) return new Response(JSON.stringify(opts.diff ?? { diff: "+- Use the EU price when both are shown", stats: { additions: 1, deletions: 0 } }))
    if (method === "POST") { rows = rows.filter((r) => (r as { source_id: string }).source_id !== /proposed\/([^/]+)\//.exec(url)![1]); return new Response("{}") }
    if (method === "PATCH") return new Response("{}")
    return new Response("{}", { status: 404 })
  })
  return calls
}

function view(canDecide = true) {
  render(<CrewMemoryReview workspaceId="ws1" crewId="c1" canDecide={canDecide} />)
}

describe("Memory › Waiting for review", () => {
  it("lists this crew's pending proposals and not another crew's", async () => {
    const calls = server()
    view()
    const card = await screen.findByRole("region", { name: "Waiting for review · 1" })
    expect(within(card).getByText(/3 rules/)).toBeInTheDocument()
    expect(calls.some((c) => c.url.includes("/api/v1/inbox") && c.url.includes("kind=memory_consolidation"))).toBe(true)
  })

  it("leaves a proposal out once it is no longer pending", async () => {
    server({ explain: { mp_1: explain("mp_1", "c1", "approved"), mp_2: explain("mp_2", "c2") } })
    view()
    await waitFor(() => expect(global.fetch).toHaveBeenCalled())
    await new Promise((r) => setTimeout(r, 30))
    expect(screen.queryByRole("region", { name: /Waiting for review/ })).toBeNull()
  })

  it("is not on the page at all when nothing waits (proposals exist only with HITL on)", async () => {
    server({ inbox: [] })
    view()
    await new Promise((r) => setTimeout(r, 30))
    expect(screen.queryByRole("region", { name: /Waiting for review/ })).toBeNull()
    expect(screen.queryByText(/could not be loaded/i)).toBeNull()
  })

  it("says so when the list cannot be read, rather than showing an empty queue", async () => {
    server({ inboxStatus: 500 })
    view()
    expect(await screen.findByRole("alert")).toHaveTextContent(/could not be loaded/i)
  })

  it("Accept approves the proposal, resolves its inbox item and drops the row", async () => {
    const calls = server()
    view()
    const card = await screen.findByRole("region", { name: "Waiting for review · 1" })
    fireEvent.click(within(card).getByRole("button", { name: "Accept" }))
    await waitFor(() => expect(screen.queryByRole("region", { name: /Waiting for review/ })).toBeNull())
    const post = calls.find((c) => c.method === "POST")!
    expect(post.url).toContain("/api/v1/consolidate/proposed/mp_1/approve")
    const patch = calls.find((c) => c.method === "PATCH")!
    expect(patch.url).toContain("/api/v1/inbox/i1")
    expect(patch.body).toEqual({ state: "resolved", resolved_action: "approved" })
  })

  it("Reject rejects it", async () => {
    const calls = server()
    view()
    const card = await screen.findByRole("region", { name: "Waiting for review · 1" })
    fireEvent.click(within(card).getByRole("button", { name: "Reject" }))
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.includes("/mp_1/reject"))).toBe(true))
    expect(calls.find((c) => c.method === "PATCH")!.body).toEqual({ state: "resolved", resolved_action: "rejected" })
  })

  it("a refused decision keeps the row and says why", async () => {
    const calls = server()
    const base = vi.mocked(global.fetch).getMockImplementation()!
    vi.mocked(global.fetch).mockImplementation(async (input, init) =>
      init?.method === "POST" ? new Response(JSON.stringify({ error: "memory proposal already decided" }), { status: 409 }) : base(input, init))
    view()
    const card = await screen.findByRole("region", { name: "Waiting for review · 1" })
    fireEvent.click(within(card).getByRole("button", { name: "Accept" }))
    expect(await screen.findByText(/already decided/)).toBeInTheDocument()
    expect(screen.getByRole("region", { name: "Waiting for review · 1" })).toBeInTheDocument()
    expect(calls.some((c) => c.method === "PATCH")).toBe(false)
  })

  it("View changes loads the diff of what Accept would append", async () => {
    server()
    view()
    const card = await screen.findByRole("region", { name: "Waiting for review · 1" })
    fireEvent.click(within(card).getByRole("button", { name: /View changes/ }))
    expect(await screen.findByText(/Use the EU price when both are shown/)).toBeInTheDocument()
  })

  it("only owners and admins decide: others get the Inbox link and no buttons", async () => {
    server()
    view(false)
    const card = await screen.findByRole("region", { name: "Waiting for review · 1" })
    expect(within(card).queryByRole("button", { name: "Accept" })).toBeNull()
    expect(within(card).queryByRole("button", { name: "Reject" })).toBeNull()
    expect(within(card).getByText(/owners and admins/i)).toBeInTheDocument()
    expect(within(card).getByRole("link", { name: /Inbox/ })).toHaveAttribute("href", expect.stringContaining("/inbox"))
  })
})

describe("Memory › Waiting for review › skill proposals", () => {
  const skill = { file_name: "skill-g2-scraper.md", name: "g2-scraper", description: "Reads G2 reviews for a product", description_quality: "ok", category: "research" }

  function skillServer(opts: { skills?: unknown; skillsStatus?: number; decide?: Response } = {}) {
    const calls: Call[] = []
    vi.spyOn(global, "fetch").mockImplementation(async (input, init) => {
      const url = String(input)
      const method = init?.method ?? "GET"
      calls.push({ url, method, body: init?.body ? JSON.parse(String(init.body)) : undefined })
      if (url.includes("/api/v1/inbox")) return new Response(JSON.stringify({ rows: [] }))
      if (url.includes("/api/v1/skills/proposed/") && method === "POST") return opts.decide ?? new Response("{}")
      if (url.includes("/api/v1/skills/proposed")) return opts.skillsStatus ? new Response("{}", { status: opts.skillsStatus }) : new Response(JSON.stringify(opts.skills ?? [skill]))
      return new Response("{}", { status: 404 })
    })
    return calls
  }
  const skillView = (canReviewSkills = true) => render(<CrewMemoryReview workspaceId="ws1" crewId="c1" canDecide={false} canReviewSkills={canReviewSkills} />)

  it("lists the skills the crew proposes next to the memory notes", async () => {
    const calls = skillServer()
    skillView()
    const card = await screen.findByRole("region", { name: "Waiting for review · 1" })
    expect(within(card).getByText("Skill “g2-scraper”")).toBeInTheDocument()
    expect(within(card).getByText(/Reads G2 reviews for a product/)).toBeInTheDocument()
    expect(calls.some((c) => c.url.includes("/api/v1/skills/proposed?crew_id=c1"))).toBe(true)
  })

  it("Accept imports the skill; Reject discards it; both name the crew and the file", async () => {
    const calls = skillServer()
    skillView()
    const card = await screen.findByRole("region", { name: "Waiting for review · 1" })
    fireEvent.click(within(card).getByRole("button", { name: "Reject" }))
    await waitFor(() => expect(screen.queryByRole("region", { name: /Waiting for review/ })).toBeNull())
    expect(calls.find((c) => c.method === "POST")).toMatchObject({ url: expect.stringContaining("/api/v1/skills/proposed/reject"), body: { crew_id: "c1", file_name: "skill-g2-scraper.md" } })
    cleanup()
    const again = skillServer()
    skillView()
    fireEvent.click(within(await screen.findByRole("region", { name: "Waiting for review · 1" })).getByRole("button", { name: "Accept" }))
    await waitFor(() => expect(again.some((c) => c.method === "POST" && c.url.includes("/skills/proposed/approve"))).toBe(true))
  })

  it("asks nothing for someone who may not review skills (Manager and above)", async () => {
    const calls = skillServer()
    skillView(false)
    await new Promise((r) => setTimeout(r, 30))
    expect(calls.some((c) => c.url.includes("/skills/proposed"))).toBe(false)
    expect(screen.queryByRole("region", { name: /Waiting for review/ })).toBeNull()
  })

  it("a skill list that fails does not hide the memory proposals and says so", async () => {
    skillServer({ skillsStatus: 500 })
    skillView()
    expect(await screen.findByText(/Skill proposals could not be loaded/)).toBeInTheDocument()
  })

  it("a refused decision keeps the row and says why", async () => {
    skillServer({ decide: new Response(JSON.stringify({ error: "staged skill not found" }), { status: 404 }) })
    skillView()
    const card = await screen.findByRole("region", { name: "Waiting for review · 1" })
    fireEvent.click(within(card).getByRole("button", { name: "Accept" }))
    expect(await screen.findByText(/staged skill not found/)).toBeInTheDocument()
    expect(screen.getByRole("region", { name: "Waiting for review · 1" })).toBeInTheDocument()
  })
})
