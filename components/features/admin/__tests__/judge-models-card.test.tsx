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

describe("BackgroundChecksCard", () => {
  beforeEach(() => { cleanup(); apiFetch.mockReset() })

  it("names the subsystem, not just the slot id", async () => {
    apiFetch.mockResolvedValue(ok({ subsystems: [HEALTHY] }))
    render(<JudgeModelsCard workspaceId="ws1" />)
    // "curator" alone means nothing to an operator; the label says what it does.
    expect(await screen.findByText(/skill review/i)).toBeTruthy()
  })

  // The credential judge is NOT on this card any more. It has its own card
  // directly above with its own Test, and the page's status strip already says
  // whether it is answering — this was the third copy of one fact and the least
  // actionable of the three, since it could be read and not changed. Asking one
  // question in three places is what made an operator conclude none of them was
  // the real setting.
  it("does not repeat the credential judge, which has its own card", async () => {
    apiFetch.mockResolvedValue(ok({ subsystems: [HEALTHY, BROKEN_JUDGE] }))
    render(<JudgeModelsCard workspaceId="ws1" />)
    await screen.findByText(/skill review/i)
    expect(screen.queryByText(/credential access judge/i)).toBeNull()
    // And its problems are not counted here either — a disabled engine is not a
    // broken background check.
    expect(screen.queryByText(/cannot run/i)).toBeNull()
  })

  it("marks an unrunnable background check as a problem, in plain words", async () => {
    const BROKEN_EVAL = {
      id: "curator", label: "Skill review + memory consolidation",
      provider: "anthropic", model: "claude-haiku-4-5", source: "explicit",
      healthy: false, detail: "ANTHROPIC_API_KEY env not set",
    }
    apiFetch.mockResolvedValue(ok({ subsystems: [BROKEN_EVAL] }))
    render(<JudgeModelsCard workspaceId="ws1" />)
    expect(await screen.findByText(/ANTHROPIC_API_KEY env not set/i)).toBeTruthy()
    // "fail closed" is a state; what an operator can act on is the consequence.
    expect(screen.getByText(/refused rather than allowed/i)).toBeTruthy()
  })

  it("does not fetch the workspace-scoped status before a workspace is known", () => {
    render(<JudgeModelsCard workspaceId={null} />)
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

  it("says so when the server reports nothing at all", async () => {
    apiFetch.mockResolvedValue(ok({ subsystems: [] }))
    render(<JudgeModelsCard workspaceId="ws1" />)
    expect(await screen.findByText(/no background checks/i)).toBeTruthy()
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

  /**
   * Open the per-evaluator detail.
   *
   * Collapsed is the default because on a stock instance the five background
   * checks are the same model five times, and that wall of identical technical
   * detail buries the credential judge above it. Every test that touches a slot
   * has to ask for the detail first — which is the flow a real operator follows.
   */
  async function openDetail() {
    fireEvent.click(await screen.findByTestId("keeper-aux-toggle"))
  }

  it("offers the provider and model as controls, not as text", async () => {
    routed()
    render(<JudgeModelsCard workspaceId="ws1" />)
    await openDetail()
    // The row an operator wants to change is the paid one.
    expect(await screen.findByLabelText(/skill review.*provider/i)).toBeTruthy()
    expect(screen.getByLabelText(/skill review.*model: claude-haiku-4-5/i)).toBeTruthy()
  })

  it("says which override needs a restart, per row", async () => {
    routed()
    render(<JudgeModelsCard workspaceId="ws1" />)
    await openDetail()
    // run_summary is captured into the pipeline executors at boot; without this
    // an operator changes it, sees nothing happen, and calls it broken.
    expect(await screen.findByText(/needs restart/i)).toBeTruthy()
  })

  it("shows where each model came from so reset has a visible referent", async () => {
    routed()
    render(<JudgeModelsCard workspaceId="ws1" />)
    await openDetail()
    expect(await screen.findByText(/shipped default/i)).toBeTruthy()
    expect(screen.getByText(/set here/i)).toBeTruthy()
  })

  it("resets one slot through the slot endpoint", async () => {
    routed()
    render(<JudgeModelsCard workspaceId="ws1" />)
    await openDetail()
    const reset = await screen.findByRole("button", { name: /^reset$/i })
    fireEvent.click(reset)
    await waitFor(() =>
      expect(apiFetch).toHaveBeenCalledWith(
        expect.stringContaining("/api/v1/admin/keeper/aux/run_summary?workspace_id=ws1"),
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
        expect.stringContaining("/api/v1/admin/keeper/aux/use-judge?workspace_id=ws1"),
        expect.objectContaining({ method: "POST" }),
      ),
    )
  })

  it("hides the local-judge action when no judge is configured", async () => {
    routed({ ...AUX, judge_model: "", judge_provider: "" })
    render(<JudgeModelsCard workspaceId="ws1" />)
    // The status row, which is present collapsed — not the aux slot label, which
    // has the same text and only appears once the detail is open.
    await screen.findAllByText(/skill review/i)
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

// Probing an evaluator on request.
//
// The card reported "not probed — Crewship does not call a paid API to render a
// status page" against every evaluator. That default is right and it was also a
// dead end: an operator could see five configured judges and had no way to learn
// whether any of them worked until a sweep ran and failed, which is the worst
// moment to find out.
describe("JudgeModelsCard — probing an evaluator", () => {
  beforeEach(() => { cleanup(); apiFetch.mockReset() })

  /** The per-slot controls sit behind "Change" — see the editing suite. */
  async function openDetail() {
    fireEvent.click(await screen.findByTestId("keeper-aux-toggle"))
  }

  const AUX_ONE = {
    slots: [{
      slot: "curator", label: "Skill review + memory consolidation", applies_at: "immediately",
      provider: { value: "anthropic", source: "default", editable: true },
      model: { value: "claude-haiku-4-5", source: "default", editable: true },
      timeout_ms: { value: 30000, source: "default", editable: true },
      overridden: false,
    }],
    providers: ["anthropic", "openai", "ollama"],
    judge_provider: "ollama",
    judge_model: "qwen2.5:7b",
    any_overridden: false,
  }

  function routeWithProbe(probeBody: unknown, probeOk = true) {
    apiFetch.mockImplementation((url: string) => {
      if (String(url).includes("aux-status")) return Promise.resolve(ok({ subsystems: [HEALTHY] }))
      if (String(url).includes("/probe")) {
        return Promise.resolve(probeOk ? ok(probeBody) : { ok: false, status: 502, json: async () => probeBody })
      }
      if (String(url).includes("/admin/keeper/aux")) return Promise.resolve(ok(AUX_ONE))
      return Promise.resolve(ok({ models: [] }))
    })
  }

  it("calls the slot's probe route and reports the verdict", async () => {
    routeWithProbe({ ok: true, stages: [{ ok: true, detail: "verdict: ALLOW" }, { ok: true, detail: "1.2s of a 20s budget — comfortable headroom" }] })
    render(<JudgeModelsCard workspaceId="ws1" />)

    await openDetail()
    fireEvent.click(await screen.findByTestId("keeper-aux-probe-curator"))
    await waitFor(() =>
      expect(apiFetch).toHaveBeenCalledWith(
        expect.stringContaining("/api/v1/admin/keeper/aux/curator/probe?workspace_id=ws1"),
        expect.objectContaining({ method: "POST" }),
      ),
    )
    // The LAST stage is the most specific good news; a passing probe should say
    // more than "ok".
    expect(await screen.findByText(/comfortable headroom/i)).toBeTruthy()
  })

  it("shows the first failing stage, not a generic failure", async () => {
    routeWithProbe({
      ok: false,
      stages: [
        { ok: true, detail: "verdict: ALLOW" },
        { ok: false, detail: "31.4s, and the budget is 20s — this judge would DENY every credential request." },
      ],
    })
    render(<JudgeModelsCard workspaceId="ws1" />)

    await openDetail()
    fireEvent.click(await screen.findByTestId("keeper-aux-probe-curator"))
    // Which stage failed is the whole diagnostic: "answered but too slowly" and
    // "never answered" send an operator to different fixes.
    expect(await screen.findByText(/would DENY every credential request/i)).toBeTruthy()
  })
})

// Not tiring an operator with five copies of the same technical detail.
//
// The card printed one row per background evaluator. On a stock instance those
// are the SAME model with different timeouts, so it rendered
// "anthropic / claude-haiku-4-5" five times and buried the one row that is in the
// path of every credential request. Reported as: "why do I have Skill review +
// memory consolidation when I can't change anything there — what use is that
// information to people?"
describe("JudgeModelsCard — how much detail it shows by default", () => {
  beforeEach(() => { cleanup(); apiFetch.mockReset() })

  const SAME = ["curator", "behavior", "memory_health", "negative", "run_summary"].map((slot) => ({
    slot, label: slot, applies_at: "immediately",
    provider: { value: "anthropic", source: "default", editable: true },
    model: { value: "claude-haiku-4-5", source: "default", editable: true },
    timeout_ms: { value: 30000, source: "default", editable: true },
    overridden: false,
  }))
  const MIXED = [
    { ...SAME[0], model: { value: "claude-opus-5", source: "instance", editable: true } },
    ...SAME.slice(1),
  ]

  function route(slots: unknown[]) {
    apiFetch.mockImplementation((url: string) => {
      if (String(url).includes("aux-status")) {
        return Promise.resolve(ok({ subsystems: [
          { id: "access_gatekeeper", label: "Credential access judge", provider: "ollama", model: "qwen2.5:7b", source: "keeper_config", healthy: true, reachable: true },
        ] }))
      }
      if (String(url).includes("/admin/keeper/aux")) {
        return Promise.resolve(ok({
          slots, providers: ["anthropic", "openai", "ollama"],
          judge_provider: "ollama", judge_model: "qwen2.5:7b", any_overridden: false,
        }))
      }
      return Promise.resolve(ok({ models: [] }))
    })
  }

  it("collapses identical evaluators into one line", async () => {
    route(SAME)
    render(<JudgeModelsCard workspaceId="ws1" />)

    // Five identical models are summarised, not listed.
    expect(await screen.findByText(/all 5 on anthropic \/ claude-haiku-4-5/i)).toBeTruthy()
    expect(screen.queryAllByTestId(/keeper-aux-probe-/)).toHaveLength(0)
  })

  it("expands on its own when the evaluators are NOT all the same", async () => {
    route(MIXED)
    render(<JudgeModelsCard workspaceId="ws1" />)

    // Here the difference IS the information, so hiding it would be the wrong
    // default — an operator who pinned one slot to Opus needs to see which.
    expect(await screen.findByTestId("keeper-aux-probe-curator")).toBeTruthy()
    expect(screen.queryByText(/all 5 on/i)).toBeNull()
  })

  it("says whether the background checks cost anything", async () => {
    route(SAME)
    render(<JudgeModelsCard workspaceId="ws1" />)
    // The decision behind this card is a spend decision, and "anthropic" does not
    // say that to someone who is not already fluent.
    expect(await screen.findByText(/cost money per run/i)).toBeTruthy()
  })

  it("loads the editable half only once a workspace is known", async () => {
    route(SAME)
    render(<JudgeModelsCard workspaceId={null} />)
    // The regression that made the whole card read-only: the aux route is behind
    // RequireWorkspace and 400s without workspace_id, and a 400 was indistinguishable
    // from "this server has no edit surface".
    const targets = apiFetch.mock.calls.map((c) => String(c[0]))
    expect(targets.some((t) => t.includes("/admin/keeper/aux"))).toBe(false)
  })

  it("sends workspace_id on the aux read, or the server refuses it", async () => {
    route(SAME)
    render(<JudgeModelsCard workspaceId="ws1" />)
    // Any call, not the first: the card reads two endpoints and their order is
    // not the property under test.
    await waitFor(() => {
      const targets = apiFetch.mock.calls.map((c) => String(c[0]))
      expect(targets.some((t) => t.includes("/admin/keeper/aux?workspace_id=ws1"))).toBe(true)
    })
  })
})
