import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { KeeperProfileCard } from "../keeper-profile-card"

const mockFetch = vi.fn()
vi.mock("@/lib/admin-api", () => ({
  adminFetch: (...args: unknown[]) => mockFetch(...args),
}))
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ abilities: { can: () => true } }),
}))
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
    expect(screen.getByText(/refuse an unbound credential/i)).toBeInTheDocument()
    expect(screen.getByText(/human approval/i)).toBeInTheDocument()
  })

  // precedent and consistency-samples are accepted and stored by the API but
  // nothing implements them yet. A switch that does nothing is a promise the
  // product does not keep, and this card is where an operator would believe it.
  it("does not offer the capabilities that are not implemented", async () => {
    render(<KeeperProfileCard workspaceId="ws1" />)
    await screen.findByText(/computed facts/i)
    expect(screen.queryByText(/precedent/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/self-consistency|majority vote/i)).not.toBeInTheDocument()
  })

  // The whole reason provenance is on the wire: "off by default" and "somebody
  // turned this off here" look identical without it.
  it("shows where each value came from", async () => {
    mockFetch.mockResolvedValue(ok(profile({ hard_gate: field(false, "instance") })))
    render(<KeeperProfileCard workspaceId="ws1" />)
    expect(await screen.findByText(/instance override/i)).toBeInTheDocument()
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

  // A response missing the profile block — an older server, a mangled proxy
  // response — must render an inert card rather than take the admin page down.
  // Somebody opens this page when something is already wrong.
  it("survives a response with no profile block", async () => {
    mockFetch.mockResolvedValue(ok({}))
    render(<KeeperProfileCard workspaceId="ws1" />)
    await waitFor(() => expect(mockFetch).toHaveBeenCalled())
    expect(screen.queryByTestId("autonomy-warning")).not.toBeInTheDocument()
  })
})
