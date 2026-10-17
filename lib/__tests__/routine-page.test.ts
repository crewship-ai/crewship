import { describe, expect, it } from "vitest"
import {
  definitionWithSteps,
  editorSteps,
  lastRunBars,
  legacyViewTarget,
  moveStep,
  parseImportedRoutine,
  parseTab,
  redactSecrets,
  routineStateBadges,
  routineTabHref,
  routineTriggerRows,
  runTraceMarkdown,
  stepsReferencing,
  timelineBars,
  visibleTabs,
} from "@/lib/routine-page"

describe("legacy ?view= links", () => {
  it.each([
    ["definition", { tab: "overview" }],
    ["plan", { tab: "triggers" }],
    ["edit", { tab: "settings" }],
    ["settings", { tab: "settings" }],
    ["versions", { tab: "versions", full: true }],
    ["publish", { tab: "overview", publish: true }],
    ["history", { tab: "overview", activity: true }],
  ])("view=%s lands on %o", (view, target) => {
    expect(legacyViewTarget(view)).toEqual(target)
  })

  it("ignores list views and unknown values", () => {
    expect(legacyViewTarget("drafts")).toBeNull()
    expect(legacyViewTarget(null)).toBeNull()
  })

  it("parses ?tab= and builds links without the default tab", () => {
    expect(parseTab("triggers")).toBe("triggers")
    expect(parseTab("nope")).toBe("overview")
    expect(routineTabHref("weekly")).toBe("/routines?slug=weekly")
    expect(routineTabHref("weekly", "versions", { version: "3" })).toBe("/routines?slug=weekly&tab=versions&version=3")
  })
})

describe("tabs and detail depth", () => {
  it("shows Versions only in Full detail, or while it is the open tab", () => {
    expect(visibleTabs("simple", "overview")).toEqual(["overview", "steps", "triggers", "settings"])
    expect(visibleTabs("full", "overview")).toEqual(["overview", "steps", "triggers", "versions", "settings"])
    expect(visibleTabs("simple", "versions")).toContain("versions")
  })
})

describe("Live / changes-not-live wording", () => {
  it("says Live and Changes not live, with versions only in Full detail", () => {
    expect(routineStateBadges({ headVersion: 4, draft: { revision: 2 } }, "simple")).toEqual({
      live: { label: "Live", tone: "success" },
      changes: { label: "Changes not live", tone: "purple" },
    })
    expect(routineStateBadges({ headVersion: 4, draft: { revision: 2 } }, "full")).toEqual({
      live: { label: "Live v4", tone: "success" },
      changes: { label: "Changes not live · draft r2", tone: "purple" },
    })
  })

  it("a routine that only exists as a draft is Not live yet, without a changes badge", () => {
    expect(routineStateBadges({ headVersion: 0, draft: { revision: 1 }, draftOnly: true }, "full")).toEqual({
      live: { label: "Not live yet", tone: "muted" },
      changes: null,
    })
  })
})

describe("last 20 runs", () => {
  it("puts the oldest on the left and tones each by how it ended", () => {
    const bars = lastRunBars([
      { id: "c", status: "waiting" },
      { id: "b", status: "completed", outcome: "FAILED" },
      { id: "a", status: "completed", outcome: "NO_CHANGE" },
    ])
    expect(bars.map((b) => [b.id, b.tone])).toEqual([
      ["a", "mut"],
      ["b", "bad"],
      ["c", "warn"],
    ])
  })

  it("keeps at most n runs", () => {
    const many = Array.from({ length: 30 }, (_, i) => ({ id: String(i), status: "completed" }))
    expect(lastRunBars(many)).toHaveLength(20)
    expect(lastRunBars(many)[19].id).toBe("0")
  })
})

describe("the one list of triggers", () => {
  const schedule = {
    id: "s1",
    name: "Weekly",
    cron_expr: "0 8 * * 4",
    timezone: "Europe/Prague",
    enabled: true,
    next_run_at: "2026-10-22T06:00:00Z",
    catchup_policy: "once",
    consecutive_failures: 0,
    max_consecutive_failures: 5,
  }

  it("lists schedule, webhook, chat command, event and the Run button in one list", () => {
    const rows = routineTriggerRows({
      slug: "weekly",
      lifecycle: "active",
      schedules: [schedule],
      webhooks: [{ id: "w1", name: "competitor-change", enabled: true, rate_limit_per_min: 60 }],
      automations: [{ id: "a1", name: "On new issue", enabled: false, event_type: "mission.created" }],
      slash: { enabled: true, label: "Scan" },
      formatTime: () => "Thu 08:00",
    })
    expect(rows.map((r) => r.kind)).toEqual(["schedule", "webhook", "chat", "event", "manual"])
    expect(rows[0]).toMatchObject({
      title: "Every Thursday at 08:00",
      detail: "Europe/Prague · next Thu 08:00 · if missed: run once · follows the published version",
      status: { label: "On", tone: "success" },
      anchor: "schedule-s1",
    })
    expect(rows[2].title).toBe("/weekly")
    expect(rows[3].status.label).toBe("Off")
    expect(rows[4].detail).toBe("crewship routine run weekly")
  })

  it("says why a schedule does not start: breaker, approval, proposed routine", () => {
    const breaker = routineTriggerRows({ slug: "x", lifecycle: "active", schedules: [{ ...schedule, enabled: false, disabled_reason: "circuit_breaker", consecutive_failures: 5 }], webhooks: [], automations: [] })
    expect(breaker[0].status).toEqual({ label: "Paused by circuit breaker", tone: "danger" })
    const draft = routineTriggerRows({ slug: "x", lifecycle: "active", schedules: [{ ...schedule, activation: "draft" }], webhooks: [], automations: [] })
    expect(draft[0].status.label).toBe("Waiting for approval")
    const proposed = routineTriggerRows({ slug: "x", lifecycle: "proposed", schedules: [schedule], webhooks: [], automations: [] })
    expect(proposed.map((r) => r.status.label)).toEqual(["Starts after approval", "Starts after approval"])
  })

  it("without anything else, only the Run button starts it", () => {
    const rows = routineTriggerRows({ slug: "x", lifecycle: "active", schedules: [], webhooks: [], automations: [] })
    expect(rows.map((r) => r.kind)).toEqual(["manual"])
  })
})

describe("run timeline", () => {
  it("places each step on the run's own clock and merges attempts", () => {
    const bars = timelineBars([
      { step_id: "a", status: "completed", started_at: "2026-10-17T08:00:00Z", ended_at: "2026-10-17T08:00:10Z" },
      { step_id: "b", status: "failed", attempt: 1, started_at: "2026-10-17T08:00:10Z", ended_at: "2026-10-17T08:00:20Z" },
      { step_id: "b", status: "completed", attempt: 2, started_at: "2026-10-17T08:00:20Z", ended_at: "2026-10-17T08:00:40Z" },
    ])
    expect(bars).toEqual([
      { stepId: "a", status: "completed", left: 0, width: 25, durationMs: 10_000 },
      { stepId: "b", status: "completed", left: 25, width: 75, durationMs: 30_000 },
    ])
  })

  it("a step still running reaches to now and has no duration yet", () => {
    const now = Date.parse("2026-10-17T08:01:00Z")
    const [bar] = timelineBars([{ step_id: "a", status: "running", started_at: "2026-10-17T08:00:00Z" }], now)
    expect(bar).toMatchObject({ left: 0, width: 100, durationMs: null })
  })

  it("is empty without executions", () => {
    expect(timelineBars([])).toEqual([])
  })
})

describe("Copy for AI", () => {
  it("copies a markdown trace without secrets", () => {
    const md = runTraceMarkdown(
      {
        id: "run_1",
        pipeline_slug: "weekly",
        pipeline_name: "Weekly scan",
        pipeline_version: 4,
        status: "failed",
        failed_at_step: "read",
        error_message: "401 Bearer abcdefghijklmnopqrstuvwxyz rejected",
        inputs: { sites: "a.com", api_token: "tok-123" },
        step_outputs: { settings: "ok" },
      },
      [
        { id: "settings", name: "Read settings", type: "code" },
        { id: "read", name: "Read new issues", type: "http" },
        { id: "label", name: "Label and assign", type: "agent_run" },
      ],
      [
        { step_id: "settings", status: "completed" },
        { step_id: "read", status: "failed", error: "HTTP 401 sk-abcdefghijklmnop" },
      ],
    )
    expect(md).toContain("# Routine run run_1")
    expect(md).toContain("Weekly scan (`weekly`) · v4")
    expect(md).toContain("1. Read settings (`settings`, code) — completed")
    expect(md).toContain("2. Read new issues (`read`, http) — failed")
    expect(md).toContain("3. Label and assign (`label`, agent_run) — not run")
    expect(md).toContain("- api_token: [left out]")
    expect(md).not.toContain("tok-123")
    expect(md).not.toContain("sk-abcdefghijklmnop")
    expect(md).not.toContain("abcdefghijklmnopqrstuvwxyz")
    expect(md).toContain("[redacted]")
  })

  it("masks common token shapes", () => {
    expect(redactSecrets("key ghp_abcdefghijklmnopqrstu end")).toBe("key [redacted] end")
  })
})

describe("import as draft", () => {
  const def = { name: "weekly", display_name: "Weekly", steps: [{ id: "a", type: "agent_run" }] }

  it("reads the routine out of an exported bundle", () => {
    const r = parseImportedRoutine(JSON.stringify({ format: "crewship-pipeline-bundle/v1", pipeline: { slug: "weekly", name: "Weekly scan", description: "d", definition: def } }))
    expect(r).toEqual({ ok: true, routine: { slug: "weekly", name: "Weekly scan", description: "d", definition: def } })
  })

  it("accepts a bare definition", () => {
    const r = parseImportedRoutine(JSON.stringify(def))
    expect(r.ok && r.routine.slug).toBe("weekly")
    expect(r.ok && r.routine.name).toBe("Weekly")
  })

  it("explains bad input in words", () => {
    expect(parseImportedRoutine("{")).toMatchObject({ ok: false, error: expect.stringContaining("not valid JSON") })
    expect(parseImportedRoutine("{\"a\":1}")).toMatchObject({ ok: false, error: expect.stringContaining("No routine definition") })
    expect(parseImportedRoutine(JSON.stringify({ definition: { steps: [] } }))).toMatchObject({ ok: false, error: "The definition has no steps." })
  })
})

describe("step editor round trip", () => {
  const definition = {
    dsl_version: "1.0",
    name: "weekly",
    max_cost_usd: 2,
    steps: [
      // No complexity: the wizard would add one if it rewrote the step.
      { id: "compare", type: "agent_run", agent_slug: "scout", prompt: "Compare" },
      { id: "gate", type: "transform", transform: { expr: "x" }, if: "{{ steps.compare.output }}" },
      { id: "ask", type: "wait", wait: { kind: "approval", approval_title: "Publish?", approval_prompt: "{{ steps.compare.output }}" } },
    ],
  }

  it("load then save without a change gives back the same definition", () => {
    expect(definitionWithSteps(definition, editorSteps(definition))).toEqual(definition)
  })

  it("rewrites only the step that changed and keeps code steps byte for byte", () => {
    const steps = editorSteps(definition)
    expect(steps[1].kind).toBe("other")
    steps[0] = { ...steps[0], prompt: "Compare each page" }
    const next = definitionWithSteps(definition, steps)
    const out = next.steps as Record<string, unknown>[]
    expect(out[0]).toMatchObject({ id: "compare", prompt: "Compare each page", complexity: "moderate" })
    expect(out[1]).toBe(definition.steps[1])
    expect(out[2]).toBe(definition.steps[2])
  })

  it("adds a web call's host to the declared egress", () => {
    const next = definitionWithSteps(definition, [...editorSteps(definition), { id: "call1", kind: "http", name: "Call", method: "GET", url: "https://erp.example.com/x" }])
    expect(next.egress_targets).toEqual(["erp.example.com"])
  })

  it("knows which steps read another step's result", () => {
    const steps = editorSteps(definition)
    expect(stepsReferencing(steps, "compare").map((s) => s.id)).toEqual(["gate", "ask"])
    expect(stepsReferencing(steps, "ask")).toEqual([])
  })

  it("moves a step", () => {
    expect(moveStep(["a", "b", "c"], 2, 0)).toEqual(["c", "a", "b"])
    expect(moveStep(["a", "b"], 0, 5)).toEqual(["a", "b"])
  })
})
