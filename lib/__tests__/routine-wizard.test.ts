import { describe, it, expect } from "vitest"

import {
  SLUG_RE,
  STEP_KINDS,
  buildDefinition,
  buildDraftDocument,
  buildSaveBody,
  buildTrigger,
  cronFor,
  describeCliCommands,
  describeMessage,
  insertableRefs,
  leadOf,
  newStep,
  onceProblem,
  parseValidationErrors,
  requiresReview,
  stepFromDsl,
  stepProblems,
  stepToDsl,
  whenText,
  wizardFromDefinition,
  type WizardState,
  type WizardStep,
} from "../routine-wizard"

function state(over: Partial<WizardState> = {}): WizardState {
  return {
    name: "Weekly report",
    slug: "weekly-report",
    description: "",
    crewId: "crew_1",
    trigger: { kind: "manual", preset: "week", minute: 0, time: "08:00", weekday: 4, cron: "0 8 * * 4", timezone: "Europe/Prague", catchup: "once", maxFailures: 5, fireAt: "" },
    slash: false,
    inputs: [],
    steps: [],
    maxCost: "",
    concurrency: "queue",
    sideBySide: 3,
    extra: {},
    ...over,
  }
}

describe("slug", () => {
  it("accepts only the server's shape", () => {
    expect(SLUG_RE.test("weekly-report")).toBe(true)
    expect(SLUG_RE.test("a_1")).toBe(true)
    expect(SLUG_RE.test("Weekly")).toBe(false)
    expect(SLUG_RE.test("-lead")).toBe(false)
    expect(SLUG_RE.test("a".repeat(65))).toBe(false)
  })
})

describe("cronFor", () => {
  it.each([
    ["hour", { minute: 15 }, "15 * * * *"],
    ["day", { time: "08:30" }, "30 8 * * *"],
    ["weekdays", { time: "09:00" }, "0 9 * * 1-5"],
    ["week", { time: "08:00", weekday: 4 }, "0 8 * * 4"],
    ["week", { time: "07:05", weekday: 0 }, "5 7 * * 0"],
    ["month", { time: "08:00" }, "0 8 1 * *"],
    ["custom", { cron: "*/10 * * * *" }, "*/10 * * * *"],
  ] as const)("%s → cron", (preset, over, cron) => {
    const t = { ...state().trigger, kind: "schedule" as const, preset, ...over }
    expect(cronFor(t)).toBe(cron)
  })
})

describe("whenText", () => {
  it("says it in words", () => {
    expect(whenText(state().trigger)).toBe("When someone starts it")
    expect(whenText({ ...state().trigger, kind: "schedule", preset: "week", weekday: 4, time: "08:00" })).toBe("Every Thursday at 08:00")
    expect(whenText({ ...state().trigger, kind: "schedule", preset: "weekdays", time: "09:00" })).toBe("Weekdays at 09:00")
    expect(whenText({ ...state().trigger, kind: "schedule", preset: "custom", cron: "0 8 * * 4" })).toBe("Cron 0 8 * * 4")
    expect(whenText({ ...state().trigger, kind: "app" })).toBe("When an app calls (added after saving)")
  })
})

// Each kind the wizard offers must land as a step the server accepts. The
// required fields here are the validator's (internal/pipeline), not ours.
describe("stepToDsl", () => {
  const cases: [string, Partial<WizardStep>, Record<string, unknown>][] = [
    ["agent", { id: "task1", name: "Draft", agentSlug: "writer", prompt: "Write {{ inputs.topic }}", effort: "smart", onFail: "retry", timeoutMin: 10 },
      { id: "task1", name: "Draft", type: "agent_run", agent_slug: "writer", prompt: "Write {{ inputs.topic }}", complexity: "smart", retry: { max_attempts: 3 }, timeout_seconds: 600 }],
    ["agent", { id: "task2", name: "Stop", agentSlug: "writer", prompt: "x", effort: "moderate", onFail: "abort" },
      { id: "task2", name: "Stop", type: "agent_run", agent_slug: "writer", prompt: "x", complexity: "moderate", on_fail: "abort" }],
    ["ask", { id: "ask1", name: "Publish?", question: "Publish?", shows: "{{ steps.task1.output }}", risk: "destructive", timeoutHours: 24 },
      { id: "ask1", name: "Publish?", type: "wait", wait: { kind: "approval", approval_title: "Publish?", approval_prompt: "{{ steps.task1.output }}", risk_level: "destructive", timeout_sec: 86400 } }],
    ["notify", { id: "notify1", name: "Tell", to: "role:MANAGER", priority: "high", message: "Done" },
      { id: "notify1", name: "Tell", type: "notify", notify: { to: "role:MANAGER", title: "Tell", body: "Done", priority: "high" } }],
    ["wait", { id: "wait1", name: "Later", waitKind: "datetime", until: "2026-11-01T08:00:00.000Z" },
      { id: "wait1", name: "Later", type: "wait", wait: { kind: "datetime", until: "2026-11-01T08:00:00.000Z" } }],
    ["wait", { id: "wait2", name: "On event", waitKind: "event", eventType: "issue.created" },
      { id: "wait2", name: "On event", type: "wait", wait: { kind: "event", event_type: "issue.created" } }],
    ["issue", { id: "issue1", name: "Open", issueAction: "issue.create", title: "Follow up", body: "See report" },
      { id: "issue1", name: "Open", type: "crewship", action: "issue.create", args: { title: "Follow up", description: "See report" } }],
    ["issue", { id: "issue2", name: "Comment", issueAction: "issue.comment", identifier: "ENG-1", body: "Report ready" },
      { id: "issue2", name: "Comment", type: "crewship", action: "issue.comment", args: { identifier: "ENG-1", body: "Report ready" } }],
    ["http", { id: "call1", name: "Post", method: "POST", url: "https://tracker.example.com/api", credentialType: "tracker" },
      { id: "call1", name: "Post", type: "http", http: { method: "POST", url: "https://tracker.example.com/api", credential_ref: { type: "tracker", inject_as: "bearer" } } }],
    ["sub", { id: "sub1", name: "Digest", pipelineSlug: "daily-digest", passInputs: "topic = {{ steps.task1.output }}\nlimit=3" },
      { id: "sub1", name: "Digest", type: "call_pipeline", pipeline_slug: "daily-digest", inputs: { topic: "{{ steps.task1.output }}", limit: "3" } }],
    ["panel", { id: "panel1", name: "Status", pageSlug: "ops-board", panelId: "status", panelData: '{"state":"green","count":3}' },
      { id: "panel1", name: "Status", type: "crewship", action: "page.write", args: { page: "ops-board", panel: "status", data: { state: "green", count: 3 } } }],
  ]
  it.each(cases)("%s", (kind, fields, dsl) => {
    const step = { ...newStep(kind as WizardStep["kind"], []), ...fields }
    expect(stepToDsl(step)).toEqual(dsl)
  })

  it("round-trips through the copy reader for every kind it writes", () => {
    for (const [kind, fields] of cases) {
      const step = { ...newStep(kind as WizardStep["kind"], []), ...fields }
      const back = stepFromDsl(stepToDsl(step), "k")
      expect(stepToDsl(back)).toEqual(stepToDsl(step))
    }
  })

  it("keeps a kind it cannot edit exactly as it was", () => {
    const raw = { id: "shape", type: "transform", transform: { input: "{{ steps.a.output }}", expression: "length" } }
    const step = stepFromDsl(raw, "k")
    expect(step.kind).toBe("other")
    expect(stepToDsl(step)).toEqual(raw)
  })
})

describe("panel step (page.write)", () => {
  it("is offered as 'Update an artifact panel'", () => {
    expect(STEP_KINDS.find((k) => k.kind === "panel")?.label).toBe("Update an artifact panel")
  })
  it("needs the page, the panel and a JSON object", () => {
    const s = newStep("panel", [])
    expect(s.id).toBe("panel1")
    expect(stepProblems(s)).toEqual(["Name the page", "Name the panel"])
    expect(stepProblems({ ...s, panelData: "" })).toContain("Write the panel data as a JSON object")
    expect(stepProblems({ ...s, pageSlug: "p", panelId: "x", panelData: "[1]" })).toEqual(["Write the panel data as a JSON object"])
    expect(stepProblems({ ...s, pageSlug: "p", panelId: "x", panelData: "{oops" })).toEqual(["Write the panel data as a JSON object"])
    expect(stepProblems({ ...s, pageSlug: "p", panelId: "x", panelData: '{"a":"{{ steps.t.output }}"}' })).toEqual([])
  })
  it("keeps a page.write step with extra args (state) as it was", () => {
    const raw = { id: "w", type: "crewship", action: "page.write", args: { page: "p", panel: "x", data: {}, state: "failed" } }
    const step = stepFromDsl(raw, "k")
    expect(step.kind).toBe("other")
    expect(stepToDsl(step)).toEqual(raw)
  })
})

describe("newStep", () => {
  it("gives each step a stable id that is unique and DSL-shaped", () => {
    const a = newStep("agent", [])
    const b = newStep("agent", [a])
    expect(a.id).toBe("task1")
    expect(b.id).toBe("task2")
    expect(newStep("ask", [a, b]).id).toBe("ask1")
  })
})

describe("stepProblems", () => {
  it("names the field each kind is missing", () => {
    expect(stepProblems(newStep("agent", []))).toEqual(["Pick an agent", "Write the instructions"])
    expect(stepProblems(newStep("ask", []))).toEqual(["Write the question", "Say what they see"])
    expect(stepProblems({ ...newStep("notify", []), message: "" })).toEqual(["Write the message"])
    expect(stepProblems({ ...newStep("http", []), url: "" })).toEqual(["Enter the URL"])
    expect(stepProblems({ ...newStep("sub", []) })).toEqual(["Pick the routine to run"])
    expect(stepProblems({ ...newStep("agent", []), agentSlug: "w", prompt: "x" })).toEqual([])
  })
})

describe("insertableRefs", () => {
  it("offers inputs and the outputs of earlier steps only", () => {
    const a = { ...newStep("agent", []), id: "collect" }
    const b = { ...newStep("agent", [a]), id: "draft" }
    const c = { ...newStep("notify", [a, b]), id: "tell" }
    const refs = insertableRefs([a, b, c], 1, [{ name: "pages", type: "string" }])
    expect(refs).toEqual(["{{ inputs.pages }}", "{{ steps.collect.output }}"])
    expect(insertableRefs([a, b, c], 0, [])).toEqual([])
  })
})

describe("buildDefinition", () => {
  it("names the definition after the slug and carries limits, slash and inputs", () => {
    const s = state({
      slash: true,
      inputs: [{ name: "topic", type: "string", default: "pricing" }],
      steps: [{ ...newStep("agent", []), agentSlug: "writer", prompt: "Write" }],
      maxCost: "2",
      concurrency: "parallel",
      sideBySide: 3,
      description: "Weekly look",
    })
    expect(buildDefinition(s)).toEqual({
      dsl_version: "1.0",
      name: "weekly-report",
      display_name: "Weekly report",
      description: "Weekly look",
      inputs: [{ name: "topic", type: "string", default: "pricing" }],
      steps: [{ id: "task1", name: "Agent task", type: "agent_run", agent_slug: "writer", prompt: "Write", complexity: "moderate", retry: { max_attempts: 3 } }],
      max_cost_usd: 2,
      concurrency_key: "weekly-report",
      max_concurrent: 3,
      slash: { enabled: true, label: "Weekly report" },
    })
  })

  it("Wait in line is one run at a time", () => {
    const d = buildDefinition(state({ steps: [{ ...newStep("agent", []), agentSlug: "w", prompt: "x" }] }))
    expect(d.concurrency_key).toBe("weekly-report")
    expect(d.max_concurrent).toBe(1)
  })

  it("adds the host of a web call to the allowed outside hosts", () => {
    const d = buildDefinition(state({ steps: [{ ...newStep("http", []), url: "https://api.example.com/x" }], extra: { egress_targets: ["other.example.org"] } }))
    expect(d.egress_targets).toEqual(["other.example.org", "api.example.com"])
  })

  it("keeps the copied routine's other settings", () => {
    const d = buildDefinition(state({ steps: [{ ...newStep("agent", []), agentSlug: "w", prompt: "x" }], extra: { execution_tier: { preferred: "smart" }, name: "old-name", steps: [] } }))
    expect(d.execution_tier).toEqual({ preferred: "smart" })
    expect(d.name).toBe("weekly-report")
    expect((d.steps as unknown[]).length).toBe(1)
  })
})

describe("requiresReview", () => {
  it("is true for web calls, credentials, outside hosts and code", () => {
    expect(requiresReview({ steps: [{ type: "agent_run" }] })).toBe(false)
    expect(requiresReview({ steps: [{ type: "http" }] })).toBe(true)
    expect(requiresReview({ steps: [], credentials_required: [{ type: "x" }] })).toBe(true)
    expect(requiresReview({ steps: [], egress_targets: ["x.com"] })).toBe(true)
    expect(requiresReview({ steps: [{ type: "code" }] })).toBe(true)
  })
})

describe("triggers", () => {
  it("builds the save trigger the server accepts", () => {
    expect(buildTrigger(state().trigger, [])).toEqual({ kind: "manual" })
    expect(buildTrigger({ ...state().trigger, kind: "app" }, [])).toEqual({ kind: "manual" })
    expect(buildTrigger({ ...state().trigger, kind: "schedule" }, [{ name: "topic", type: "string", default: "pricing" }])).toEqual({
      kind: "schedule", cron: "0 8 * * 4", timezone: "Europe/Prague", catchup_policy: "once", max_consecutive_failures: 5, inputs: { topic: "pricing" },
    })
    const fire = new Date(Date.now() + 86_400_000).toISOString()
    expect(buildTrigger({ ...state().trigger, kind: "once", fireAt: fire }, [])).toEqual({ kind: "once", fire_at: fire })
  })

  it("once must be in the future and within two years", () => {
    const now = new Date("2026-10-17T10:00:00Z")
    expect(onceProblem("", now)).toBe("Pick a date and time")
    expect(onceProblem("2026-10-16T10:00:00Z", now)).toBe("Pick a time in the future")
    expect(onceProblem("2029-01-01T10:00:00Z", now)).toBe("Pick a time within the next two years")
    expect(onceProblem("2026-10-20T10:00:00Z", now)).toBeNull()
  })
})

describe("save payloads", () => {
  it("save sends the token, the trigger, the crew and the tested definition", () => {
    const s = state({ steps: [{ ...newStep("agent", []), agentSlug: "w", prompt: "x" }], description: "Hi" })
    const definition = buildDefinition(s)
    expect(buildSaveBody(s, definition, "tok")).toEqual({
      slug: "weekly-report",
      name: "Weekly report",
      description: "Hi",
      definition,
      author_crew_id: "crew_1",
      save_token: "tok",
      trigger: { kind: "manual" },
      change_summary: "initial version",
    })
  })

  it("a comment step makes the crew lead the acting agent", () => {
    const s = state({ steps: [{ ...newStep("issue", []), issueAction: "issue.comment", identifier: "ENG-1", body: "x" }] })
    expect(buildSaveBody(s, buildDefinition(s), "t", "agent_lead").author_agent_id).toBe("agent_lead")
  })

  it("a draft is a new draft envelope without a trigger", () => {
    const s = state({ steps: [{ ...newStep("agent", []), agentSlug: "w", prompt: "x" }] })
    const doc = buildDraftDocument(s, buildDefinition(s), { icon: "rocket", color: "blue" })
    expect(doc).toEqual({ slug: "weekly-report", name: "Weekly report", description: "", definition: buildDefinition(s), author_crew_id: "crew_1", icon: "rocket", color: "blue" })
    expect(doc).not.toHaveProperty("trigger")
  })
})

describe("parseValidationErrors", () => {
  it("splits the server's flattened 422 into paths with a step number", () => {
    const msg = "2 validation errors:\n  - /steps/0/agent_slug: pipeline: step \"task1\" agent_slug required\n  - /max_cost_usd: must be positive"
    expect(parseValidationErrors(msg)).toEqual([
      { path: "/steps/0/agent_slug", message: "pipeline: step \"task1\" agent_slug required", stepIndex: 0 },
      { path: "/max_cost_usd", message: "must be positive", stepIndex: null },
    ])
  })
  it("keeps a single unstructured message", () => {
    expect(parseValidationErrors("pipeline: steps required")).toEqual([{ path: "", message: "pipeline: steps required", stepIndex: null }])
  })
})

describe("copy", () => {
  it("reads steps, inputs and limits but not the identity", () => {
    const w = wizardFromDefinition({
      name: "source", display_name: "Source", max_cost_usd: 1.5, max_concurrent: 2, concurrency_key: "source",
      inputs: [{ name: "a", type: "string" }],
      steps: [{ id: "x", type: "agent_run", agent_slug: "w", prompt: "p" }],
      execution_tier: { preferred: "fast" },
    })
    expect(w.steps.map((s) => s.id)).toEqual(["x"])
    expect(w.inputs).toEqual([{ name: "a", type: "string" }])
    expect(w.maxCost).toBe("1.5")
    expect(w.concurrency).toBe("parallel")
    expect(w.sideBySide).toBe(2)
    expect(w.extra).toEqual({ execution_tier: { preferred: "fast" } })
  })
})

describe("lead and hand-off", () => {
  it("picks the LEAD of a crew, else any agent in it", () => {
    const agents = [{ id: "1", name: "A", slug: "a", agent_role: "WORKER", crew_id: "c" }, { id: "2", name: "B", slug: "b", agent_role: "LEAD", crew_id: "c" }]
    expect(leadOf(agents, "c")?.slug).toBe("b")
    expect(leadOf(agents.slice(0, 1), "c")?.slug).toBe("a")
    expect(leadOf(agents, null)).toBeNull()
  })
  it("the lead gets the person's own words, not tool names", () => {
    const m = describeMessage("Contract check", "Find contracts ending in 60 days")
    expect(m).toContain("Find contracts ending in 60 days")
    expect(m).toContain("Contract check")
    expect(m).toContain("draft")
    expect(m).not.toMatch(/save_routine|get_routine_draft|editor_url|outcomes\.required/)
  })
  it("spells only CLI commands that exist", () => {
    const text = describeCliCommands("weekly-report", "Weekly report", "research").map((c) => c.command).join("\n")
    expect(text).toContain("crewship routine init -o weekly-report.json")
    expect(text).toContain("crewship routine validate weekly-report.json")
    expect(text).toContain('crewship routine save --name "Weekly report" --definition weekly-report.json --author-crew research')
    expect(text).not.toContain("draft save weekly-report")
  })
})
