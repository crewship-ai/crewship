import { render, screen, waitFor, cleanup } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { KeeperJudgeCard } from "../keeper-judge-card"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetch(...args),
}))
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ abilities: { can: () => true } }),
}))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

function field<T>(value: T, source = "instance") {
  return { value, source, editable: true }
}

function ok(body: unknown) {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}

beforeEach(() => {
  apiFetch.mockReset()
  apiFetch.mockResolvedValue(ok({
    enabled: field(true),
    judge_provider: field("ollama"),
    judge_endpoint_url: field("http://x:11434"),
    judge_wire: field("ollama"),
    judge_model: field("qwen3.5:9b"),
    judge_timeout_ms: field(20000),
    judge_profile: {
      name: field("lean"), evidence: field(true),
      evidence_facts: field([]), hard_gate: field(true),
      escalate_from: field(3), prompt_budget_tokens: field(3500),
      overridden: true, choices: [], available_facts: [], stamp: "",
    },
    overridden: true,
    judge_configured: true,
  }))
})

afterEach(cleanup)

// The reset is DELETE /admin/keeper/config, and that drops the whole
// keeper_runtime_settings row — which since the judge profile landed also holds
// the evidence block, the unbound-credential refusal, the escalation floor and
// the prompt budget.
//
// So a button on THIS card silently reverts settings shown on the card beneath
// it, two of which are security controls. "Reset to inherited" read as though it
// scoped to the fields above it, which was true when it was written and stopped
// being true when the profile card was added. The label has to name what it
// actually clears.
describe("KeeperJudgeCard reset scope", () => {
  it("says that resetting also clears the judge profile", async () => {
    render(<KeeperJudgeCard workspaceId="ws1" />)
    const btn = await screen.findByTestId("keeper-judge-reset")
    await waitFor(() => expect(apiFetch).toHaveBeenCalled())

    const described = `${btn.textContent ?? ""} ${btn.getAttribute("title") ?? ""}`.toLowerCase()
    expect(described).toMatch(/profile|all keeper|everything/)
    expect(described).not.toBe("reset to inherited")
  })
})
