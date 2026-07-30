import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup, waitFor, fireEvent } from "@testing-library/react"

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

  it("does not fetch the workspace-scoped status before a workspace is known", () => {
    render(<JudgeModelsCard workspaceId={null} />)
    // aux-status 400s without workspace_id; firing anyway just logs noise. The
    // evaluator config is instance-scoped and takes no workspace, so that one
    // may load — the card's edit half must not wait on a workspace it does not
    // need.
    const targets = apiFetch.mock.calls.map((c) => String(c[0]))
    expect(targets.some((t) => t.includes("aux-status"))).toBe(false)
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

// "Configured" and "answering" are different questions and the card has to
// keep them apart: a judge whose provider builds fine can still be pointing
// at a model server that is not running — which is exactly what dev3 did.
describe("JudgeModelsCard — reachability", () => {
  beforeEach(() => { cleanup(); apiFetch.mockReset() })

  const UNREACHABLE = {
    id: "access_gatekeeper", label: "Credential access judge",
    provider: "ollama", model: "qwen2.5:7b", source: "keeper_config",
    healthy: true, reachable: false, reach_detail: "no response from http://127.0.0.1:11434",
  }
  const REACHABLE = { ...UNREACHABLE, reachable: true, reach_detail: "" }
  const UNPROBED = {
    id: "curator", label: "Skill review + memory consolidation",
    provider: "anthropic", model: "claude-haiku-4-5", source: "explicit",
    healthy: true, reach_detail: "not probed — Crewship does not call a paid API to render a status page",
  }

  it("flags a configured judge whose model server is not answering", async () => {
    apiFetch.mockResolvedValue(ok({ subsystems: [UNREACHABLE] }))
    render(<JudgeModelsCard workspaceId="ws1" />)
    expect(await screen.findByText(/no response from/i)).toBeTruthy()
  })

  it("does not call an unreachable judge healthy just because it is configured", async () => {
    apiFetch.mockResolvedValue(ok({ subsystems: [UNREACHABLE] }))
    render(<JudgeModelsCard workspaceId="ws1" />)
    await screen.findByText(/no response from/i)
    // The summary line is what an admin scans; it must count this one.
    expect(screen.getByText(/cannot run right now/i)).toBeTruthy()
  })

  it("stays quiet when the judge answers", async () => {
    apiFetch.mockResolvedValue(ok({ subsystems: [REACHABLE] }))
    render(<JudgeModelsCard workspaceId="ws1" />)
    await screen.findByText(/credential access judge/i)
    expect(screen.queryByText(/cannot run right now/i)).toBeNull()
  })

  it("says a paid provider was not probed rather than guessing", async () => {
    apiFetch.mockResolvedValue(ok({ subsystems: [UNPROBED] }))
    render(<JudgeModelsCard workspaceId="ws1" />)
    await screen.findByText(/skill review/i)
    // Neither green-with-confidence nor red-with-alarm: unknown, and why.
    expect(screen.getByText(/not probed/i)).toBeTruthy()
    expect(screen.queryByText(/cannot run right now/i)).toBeNull()
  })
})

// The card stopped being read-only because what read-only left was five paid
// evaluators an operator could see and not change. These pin the edit half:
// that a row becomes editable, that a save reaches the slot endpoint, and that
// a restart-scoped slot says so.
describe("JudgeModelsCard — editing the evaluator models", () => {
  beforeEach(() => { cleanup(); apiFetch.mockReset() })

  const AUX = {
    slots: [
      {
        slot: "curator", label: "Skill review + memory consolidation", applies_at: "immediately",
        provider: { value: "anthropic", source: "default", editable: true },
        model: { value: "claude-haiku-4-5", source: "default", editable: true },
        timeout_ms: { value: 30000, source: "default", editable: true },
        overridden: false,
      },
      {
        slot: "run_summary", label: "Run summary verdicts", applies_at: "restart",
        provider: { value: "ollama", source: "instance", editable: true },
        model: { value: "qwen2.5:7b", source: "instance", editable: true },
        timeout_ms: { value: 15000, source: "default", editable: true },
        overridden: true,
      },
    ],
    providers: ["anthropic", "openai", "ollama"],
    judge_provider: "ollama",
    judge_model: "qwen2.5:7b",
    any_overridden: true,
  }

  /** Route each call by URL — the card reads two endpoints for one card. */
  function routed(aux: unknown = AUX, subsystems: unknown[] = [HEALTHY]) {
    apiFetch.mockImplementation((url: string) => {
      if (String(url).includes("aux-status")) return Promise.resolve(ok({ subsystems }))
      // GET, PUT, DELETE and use-judge all answer with the same payload shape,
      // which is what lets the card re-render from the write's response.
      if (String(url).includes("/admin/keeper/aux")) return Promise.resolve(ok(aux))
      return Promise.resolve(ok({ models: [] }))
    })
  }

  it("offers the provider and model as controls, not as text", async () => {
    routed()
    render(<JudgeModelsCard workspaceId="ws1" />)
    // The row an operator wants to change is the paid one.
    expect(await screen.findByLabelText(/skill review.*provider/i)).toBeTruthy()
    expect(screen.getByLabelText(/skill review.*model: claude-haiku-4-5/i)).toBeTruthy()
  })

  it("says which override needs a restart, per row", async () => {
    routed()
    render(<JudgeModelsCard workspaceId="ws1" />)
    // run_summary is captured into the pipeline executors at boot; without this
    // an operator changes it, sees nothing happen, and calls it broken.
    expect(await screen.findByText(/needs restart/i)).toBeTruthy()
  })

  it("shows where each model came from so reset has a visible referent", async () => {
    routed()
    render(<JudgeModelsCard workspaceId="ws1" />)
    expect(await screen.findByText(/shipped default/i)).toBeTruthy()
    expect(screen.getByText(/set here/i)).toBeTruthy()
  })

  it("resets one slot through the slot endpoint", async () => {
    routed()
    render(<JudgeModelsCard workspaceId="ws1" />)
    const reset = await screen.findByRole("button", { name: /^reset$/i })
    fireEvent.click(reset)
    await waitFor(() =>
      expect(apiFetch).toHaveBeenCalledWith(
        "/api/v1/admin/keeper/aux/run_summary",
        expect.objectContaining({ method: "PUT" }),
      ),
    )
  })

  it("offers the one-press cost decision only when a judge exists", async () => {
    routed()
    render(<JudgeModelsCard workspaceId="ws1" />)
    const btn = await screen.findByRole("button", { name: /use local judge for all/i })
    fireEvent.click(btn)
    await waitFor(() =>
      expect(apiFetch).toHaveBeenCalledWith(
        "/api/v1/admin/keeper/aux/use-judge",
        expect.objectContaining({ method: "POST" }),
      ),
    )
  })

  it("hides the local-judge action when no judge is configured", async () => {
    routed({ ...AUX, judge_model: "", judge_provider: "" })
    render(<JudgeModelsCard workspaceId="ws1" />)
    await screen.findByText(/skill review/i)
    // A button that can only 400 is worse than no button.
    expect(screen.queryByRole("button", { name: /use local judge for all/i })).toBeNull()
  })

  it("stays a working status card when the edit surface is unavailable", async () => {
    apiFetch.mockImplementation((url: string) => {
      if (String(url).includes("aux-status")) return Promise.resolve(ok({ subsystems: [HEALTHY] }))
      return Promise.resolve({ ok: false, status: 503, json: async () => ({}) })
    })
    render(<JudgeModelsCard workspaceId="ws1" />)
    // An older server, or a router with no store wired: the health rows are the
    // reason an operator opened this, so a missing edit half must not bury them.
    expect(await screen.findByText(/claude-haiku-4-5/)).toBeTruthy()
    expect(screen.queryByLabelText(/provider/i)).toBeNull()
  })
})
