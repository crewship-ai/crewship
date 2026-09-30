// Review R2: the credential judge and its decision rules are instance-wide
// settings; the server lets an instance admin change them whatever their role
// in the workspace they stand in, and refuses everyone else. The cards have to
// agree — editable for an instance admin who is only a MEMBER here, read-only
// for a workspace ADMIN who is not an instance admin.
import { render, screen, cleanup, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const h = vi.hoisted(() => ({ fetch: vi.fn(), instanceAdmin: true as boolean | null, workspaceAdmin: false }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.fetch(...a) }))
vi.mock("@/lib/admin-api", () => ({ adminFetch: (...a: unknown[]) => h.fetch(...a) }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ abilities: { can: () => h.workspaceAdmin } }) }))
vi.mock("@/hooks/use-auth", () => ({ useIsInstanceAdmin: () => h.instanceAdmin }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

import { KeeperJudgeCard } from "../keeper-judge-card"
import { KeeperProfileCard } from "../keeper-profile-card"

const field = <T,>(value: T, source = "instance") => ({ value, source, editable: true })
const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body }) as unknown as Response
const PROFILE = {
  name: field("lean"), evidence: field(true), evidence_facts: field([]), hard_gate: field(true),
  escalate_from: field(0), precedent: field(false), precedent_n: field(3), consistency_samples: field(1),
  prompt_budget_tokens: field(3500), overridden: false, choices: ["lean", "standard"], available_facts: [], stamp: "",
}

beforeEach(() => {
  h.fetch.mockReset()
  h.fetch.mockResolvedValue(ok({
    enabled: field(true), judge_provider: field("ollama"), judge_endpoint_url: field("http://x:11434"),
    judge_wire: field("ollama"), judge_model: field("qwen3.5:9b"), judge_timeout_ms: field(20000),
    judge_profile: PROFILE, overridden: false, judge_configured: true,
  }))
})
afterEach(cleanup)

const controls = () => screen.getAllByRole("switch").concat(screen.queryAllByRole("combobox"))

describe.each([
  ["KeeperJudgeCard", () => <KeeperJudgeCard workspaceId="ws1" />],
  ["KeeperProfileCard", () => <KeeperProfileCard workspaceId="ws1" />],
])("%s", (_, card) => {
  it("is editable for an instance admin who is only a MEMBER here", async () => {
    h.instanceAdmin = true; h.workspaceAdmin = false
    render(card())
    await waitFor(() => expect(controls().length).toBeGreaterThan(0))
    expect(controls().some((c) => !(c as HTMLButtonElement).disabled)).toBe(true)
  })

  it("is read-only for a workspace ADMIN who is not an instance admin", async () => {
    h.instanceAdmin = false; h.workspaceAdmin = true
    render(card())
    await waitFor(() => expect(controls().length).toBeGreaterThan(0))
    expect(controls().every((c) => (c as HTMLButtonElement).disabled)).toBe(true)
  })
})
