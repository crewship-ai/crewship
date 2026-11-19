import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { KeeperProfileCard } from "../keeper-profile-card"

const mockFetch = vi.fn()
vi.mock("@/lib/admin-api", () => ({
  adminFetch: (...args: unknown[]) => mockFetch(...args),
}))
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ abilities: { can: () => true } }),
}))
vi.mock("@/hooks/use-auth", () => ({ useIsInstanceAdmin: () => true }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

function field<T>(value: T, source = "default") {
  return { value, source, editable: true }
}

function profile(over: Record<string, unknown> = {}) {
  return {
    judge_profile: {
      name: field("lean"),
      evidence: field(true),
      evidence_facts: field(["credential_bound_to_agent", "prior_requests_same_pair"]),
      hard_gate: field(true),
      escalate_from: field(0),
      precedent: field(false),
      precedent_n: field(3),
      consistency_samples: field(1),
      prompt_budget_tokens: field(3500),
      overridden: false,
      choices: ["lean", "standard", "thorough"],
      available_facts: ["credential_bound_to_agent", "prior_requests_same_pair"],
      stamp: "lean evidence=on facts=all hard_gate=on escalate_from=tier",
      ...over,
    },
  }
}

function ok(body: unknown) {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}

beforeEach(() => {
  mockFetch.mockReset()
  mockFetch.mockResolvedValue(ok(profile()))
})

afterEach(cleanup)

describe("KeeperProfileCard", () => {
  it("renders the capabilities the judge is actually using", async () => {
    render(<KeeperProfileCard workspaceId="ws1" />)
    expect(await screen.findByText(/computed facts/i)).toBeInTheDocument()
    expect(screen.getByText(/refuse a credential bound to nothing/i)).toBeInTheDocument()
    expect(screen.getByRole("radiogroup", { name: "When a person confirms" })).toBeInTheDocument()
  })

  // Precedent and "ask more than once" are the API's own settings (judge_precedent,
  // judge_precedent_n, judge_consistency_samples): the card offers them, in words.
  it("offers facts, earlier decisions and asking more than once, in plain words", async () => {
    render(<KeeperProfileCard workspaceId="ws1" />)
    await screen.findByText(/computed facts/i)
    expect(screen.getByRole("group", { name: "Facts it may compute" })).toBeInTheDocument()
    expect(screen.getByRole("checkbox", { name: "Credential is bound to the agent" })).toBeChecked()
    expect(screen.getByText("Compare with earlier decisions")).toBeInTheDocument()
    expect(screen.getByRole("radiogroup", { name: "Ask more than once" })).toBeInTheDocument()
    expect(screen.getByRole("radiogroup", { name: "Level" })).toBeInTheDocument()
  })

  it("lists the four levels with the reason each needs, read-only", async () => {
    render(<KeeperProfileCard workspaceId="ws1" />)
    const card = (await screen.findByText("The four levels")).closest("section")!
    expect(card).toHaveTextContent("1 · Low")
    expect(card).toHaveTextContent("reason of at least 10 characters")
    expect(card).toHaveTextContent("reason of at least 35 characters")
    expect(card).toHaveTextContent("second approver always")
    expect(card.querySelector("button, input, [role=switch]")).toBeNull()
  })

  it("sends the new rules with the types the server validates", async () => {
    render(<KeeperProfileCard workspaceId="ws1" />)
    await screen.findByText(/computed facts/i)
    mockFetch.mockClear()
    mockFetch.mockResolvedValue(ok(profile()))
    fireEvent.click(screen.getByRole("checkbox", { name: "Credential is bound to the agent" }))
    fireEvent.click(screen.getByTestId("keeper-profile-precedent"))
    fireEvent.change(screen.getByTestId("keeper-profile-precedent-n"), { target: { value: "5" } })
    fireEvent.click(within(screen.getByRole("radiogroup", { name: "Ask more than once" })).getByRole("radio", { name: "3 times" }))
    fireEvent.click(within(screen.getByRole("radiogroup", { name: "When a person confirms" })).getByRole("radio", { name: /From level 3/ }))
    fireEvent.click(await screen.findByRole("button", { name: /save/i }))
    await waitFor(() => expect(mockFetch.mock.calls.some(([, , init]) => (init as RequestInit)?.method === "PUT")).toBe(true))
    const put = mockFetch.mock.calls.find(([, , init]) => (init as RequestInit)?.method === "PUT")!
    expect(JSON.parse((put[2] as RequestInit).body as string)).toEqual({
      judge_evidence_facts: "prior_requests_same_pair",
      judge_precedent: true,
      judge_precedent_n: 5,
      judge_consistency_samples: 3,
      judge_escalate_from: 3,
    })
  })

  it("refuses to save with no fact picked, since an empty list means every fact", async () => {
    render(<KeeperProfileCard workspaceId="ws1" />)
    await screen.findByText(/computed facts/i)
    fireEvent.click(screen.getByRole("checkbox", { name: "Credential is bound to the agent" }))
    fireEvent.click(screen.getByRole("checkbox", { name: "Earlier requests by the same pair" }))
    expect(await screen.findByTestId("facts-invalid")).toBeInTheDocument()
    expect((screen.getByRole("button", { name: /save/i }) as HTMLButtonElement).disabled).toBe(true)
  })

  // The whole reason provenance is on the wire: "off by default" and "somebody
  // turned this off here" look identical without it.
  it("shows where each value came from", async () => {
    mockFetch.mockResolvedValue(ok(profile({ hard_gate: field(false, "instance") })))
    render(<KeeperProfileCard workspaceId="ws1" />)
    expect(await screen.findByText("Set here")).toBeInTheDocument()
  })

  // Full autonomy removes a person from production credentials. It must not read
  // like any other row on the page.
  it("marks full autonomy as the exceptional setting it is", async () => {
    mockFetch.mockResolvedValue(ok(profile({ escalate_from: field(5, "instance") })))
    render(<KeeperProfileCard workspaceId="ws1" />)
    const warn = await screen.findByTestId("autonomy-warning")
    expect(warn).toBeInTheDocument()
    expect(warn.textContent ?? "").toMatch(/without a person|no human|every tier/i)
  })

  it("does not warn on the safe default", async () => {
    render(<KeeperProfileCard workspaceId="ws1" />)
    await screen.findByText(/computed facts/i)
    expect(screen.queryByTestId("autonomy-warning")).not.toBeInTheDocument()
  })

  // Partial patch, like the judge card next to it: sending fields the operator
  // did not touch would clobber whatever the CLI or another admin had set.
  it("sends only what changed", async () => {
    render(<KeeperProfileCard workspaceId="ws1" />)
    await screen.findByText(/computed facts/i)

    mockFetch.mockClear()
    mockFetch.mockResolvedValue(ok(profile({ hard_gate: field(false, "instance") })))
    fireEvent.click(screen.getByTestId("keeper-profile-hard-gate"))
    fireEvent.click(await screen.findByRole("button", { name: /save/i }))

    await waitFor(() => expect(mockFetch).toHaveBeenCalled())
    const put = mockFetch.mock.calls.find(([, , init]) => (init as RequestInit)?.method === "PUT")
    expect(put).toBeDefined()
    const body = JSON.parse((put![2] as RequestInit).body as string)
    expect(body).toHaveProperty("judge_hard_gate")
    expect(body).not.toHaveProperty("judge_evidence")
    expect(body).not.toHaveProperty("judge_prompt_budget_tokens")
  })

  // internal/api/admin_keeper_config.go (triFromJSON) accepts true, false or
  // null for the three toggles and answers 400 to anything else, "on" and "off"
  // included. The card used to send those strings, so flipping a toggle failed.
  it("sends the toggles as booleans, never as the strings on/off", async () => {
    render(<KeeperProfileCard workspaceId="ws1" />)
    await screen.findByText(/computed facts/i)
    mockFetch.mockClear()
    mockFetch.mockResolvedValue(ok(profile({ hard_gate: field(false, "instance") })))
    fireEvent.click(screen.getByTestId("keeper-profile-hard-gate"))
    fireEvent.click(await screen.findByRole("button", { name: /save/i }))
    await waitFor(() => expect(mockFetch.mock.calls.some(([, , init]) => (init as RequestInit)?.method === "PUT")).toBe(true))
    const put = mockFetch.mock.calls.find(([, , init]) => (init as RequestInit)?.method === "PUT")!
    const body = JSON.parse((put[2] as RequestInit).body as string)
    expect(body.judge_hard_gate).toBe(false)
  })

  // A response missing the profile block — an older server, a mangled proxy
  // response — must render an inert card rather than take the admin page down.
  // Somebody opens this page when something is already wrong.
  it("survives a response with no profile block", async () => {
    mockFetch.mockResolvedValue(ok({}))
    render(<KeeperProfileCard workspaceId="ws1" />)
    await waitFor(() => expect(mockFetch).toHaveBeenCalled())
    expect(screen.queryByTestId("autonomy-warning")).not.toBeInTheDocument()
  })


  // Stripping the minus was cosmetic, not corrective: "-1" became "1", which the
  // server refuses for the same reason — every non-zero value below 512 is
  // rejected — so the operator still paid the round trip the guard was meant to
  // save. The field has to hold the whole rule, not just the sign.
  it.each(["-1", "1", "511", "200000"])("blocks saving an out-of-range budget: %s", async (bad) => {
    render(<KeeperProfileCard workspaceId="ws1" />)
    await screen.findByText(/computed facts/i)

    fireEvent.change(screen.getByTestId("keeper-profile-budget"), { target: { value: bad } })
    expect(await screen.findByTestId("budget-invalid")).toBeInTheDocument()
    expect((screen.getByRole("button", { name: /save/i }) as HTMLButtonElement).disabled).toBe(true)
  })

  // 0 is the documented "no cap", and in-range values must stay savable — a
  // guard that blocks everything is not a guard.
  it.each(["0", "512", "3500", "131072"])("accepts a valid budget: %s", async (good) => {
    render(<KeeperProfileCard workspaceId="ws1" />)
    await screen.findByText(/computed facts/i)

    fireEvent.change(screen.getByTestId("keeper-profile-budget"), { target: { value: good } })
    expect(screen.queryByTestId("budget-invalid")).not.toBeInTheDocument()
  })
})
