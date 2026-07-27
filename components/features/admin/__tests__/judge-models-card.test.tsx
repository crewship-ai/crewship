import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup, waitFor } from "@testing-library/react"

import { JudgeModelsCard } from "../judge-models-card"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

function ok(body: unknown) {
  return { ok: true, status: 200, json: async () => body }
}

const HEALTHY = {
  id: "curator", label: "Skill review + memory consolidation",
  provider: "anthropic", model: "claude-haiku-4-5", timeout_ms: 30000,
  source: "explicit", healthy: true,
}
const BROKEN_JUDGE = {
  id: "access_gatekeeper", label: "Credential access judge",
  provider: "ollama", model: "qwen2.5:7b", source: "keeper_config",
  healthy: false, detail: "disabled by configuration (keeper.enabled = false)",
}

describe("JudgeModelsCard", () => {
  beforeEach(() => { cleanup(); apiFetch.mockReset() })

  it("names the subsystem, not just the slot id", async () => {
    apiFetch.mockResolvedValue(ok({ subsystems: [HEALTHY] }))
    render(<JudgeModelsCard workspaceId="ws1" />)
    // "curator" alone means nothing to an operator; the label says what it does.
    expect(await screen.findByText(/skill review/i)).toBeTruthy()
    expect(screen.getByText(/claude-haiku-4-5/)).toBeTruthy()
  })

  it("shows the reason a judge is not usable, not just a red dot", async () => {
    apiFetch.mockResolvedValue(ok({ subsystems: [BROKEN_JUDGE] }))
    render(<JudgeModelsCard workspaceId="ws1" />)
    // The whole point of the rework: the old card said "explicit" here and
    // left the operator to discover the judge was off by other means.
    expect(await screen.findByText(/disabled by configuration/i)).toBeTruthy()
  })

  it("marks an unhealthy subsystem as a problem, visibly", async () => {
    apiFetch.mockResolvedValue(ok({ subsystems: [HEALTHY, BROKEN_JUDGE] }))
    render(<JudgeModelsCard workspaceId="ws1" />)
    await screen.findByText(/credential access judge/i)
    const problems = screen.getAllByRole("status").filter((el) => /not running|unavailable|problem/i.test(el.textContent ?? ""))
    expect(problems.length).toBeGreaterThan(0)
  })

  it("does not fetch before a workspace is known", () => {
    render(<JudgeModelsCard workspaceId={null} />)
    // The endpoint 400s without workspace_id; firing anyway just logs noise.
    expect(apiFetch).not.toHaveBeenCalled()
  })

  it("passes the workspace through — the endpoint requires it", async () => {
    apiFetch.mockResolvedValue(ok({ subsystems: [HEALTHY] }))
    render(<JudgeModelsCard workspaceId="ws1" />)
    await waitFor(() =>
      expect(apiFetch).toHaveBeenCalledWith(expect.stringContaining("workspace_id=ws1")),
    )
  })

  it("surfaces a failed load rather than rendering an empty, reassuring card", async () => {
    apiFetch.mockResolvedValue({ ok: false, status: 500, json: async () => ({}) })
    render(<JudgeModelsCard workspaceId="ws1" />)
    expect(await screen.findByText(/couldn't load/i)).toBeTruthy()
  })

  it("says so when the server reports no subsystems at all", async () => {
    apiFetch.mockResolvedValue(ok({ subsystems: [] }))
    render(<JudgeModelsCard workspaceId="ws1" />)
    expect(await screen.findByText(/no judge/i)).toBeTruthy()
  })
})
