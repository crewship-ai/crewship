import { describe, it, expect } from "vitest"
import { describeRoutine, describeStep } from "@/lib/routine-readable"

describe("describeStep", () => {
  it("renders an agent_run step in plain language", () => {
    const step = describeStep(
      {
        id: "summarize",
        type: "agent_run",
        agent_slug: "alex",
        complexity: "fast",
        prompt: "Summarize each article in one sentence:\n\n{{ steps.fetch.output }}",
      },
      2,
    )
    expect(step.kind).toBe("agent_run")
    expect(step.position).toBe(2)
    expect(step.title).toBe("Ask alex")
    expect(step.detail).toContain("Summarize each article")
    // multi-line prompt collapses to its first line
    expect(step.detail).not.toContain("{{ steps.fetch.output }}")
    expect(step.technical).toBe("agent_run · fast")
  })

  it("falls back gracefully when agent_run has no agent or prompt", () => {
    const step = describeStep({ type: "agent_run" }, 1)
    expect(step.title).toBe("Ask an agent")
    expect(step.detail).toBeUndefined()
    expect(step.technical).toBe("agent_run · default")
  })

  it("renders an http GET as a fetch with the host", () => {
    const step = describeStep(
      { id: "fetch", type: "http", http: { method: "GET", url: "https://news.ycombinator.com" } },
      1,
    )
    expect(step.kind).toBe("http")
    expect(step.title).toBe("Fetch from Hacker News")
    expect(step.technical).toBe("http GET https://news.ycombinator.com")
  })

  it("renders an http GET to an unknown host with the bare host", () => {
    const step = describeStep(
      { type: "http", http: { method: "GET", url: "https://api.example.com/v1/items" } },
      1,
    )
    expect(step.title).toBe("Fetch from api.example.com")
  })

  it("renders a Slack webhook POST with the detected channel", () => {
    const step = describeStep(
      {
        type: "http",
        http: {
          method: "POST",
          url: "https://hooks.slack.com/services/T000/B000/xxx",
          body: '{"channel":"#standup","text":"hi"}',
        },
      },
      3,
    )
    expect(step.title).toBe("Slack → #standup")
    expect(step.technical).toContain("http POST")
  })

  it("renders a Slack POST without a channel as a send", () => {
    const step = describeStep(
      { type: "http", http: { method: "POST", url: "https://hooks.slack.com/services/x" } },
      1,
    )
    expect(step.title).toBe("Send to Slack")
  })

  it("handles a templated URL without throwing", () => {
    const step = describeStep({ type: "http", http: { method: "GET", url: "{{ inputs.url }}" } }, 1)
    expect(step.kind).toBe("http")
    expect(step.title).toContain("Fetch from")
  })

  it("renders a transform step with its expression", () => {
    const step = describeStep(
      { type: "transform", transform: { input: "{{ steps.a.output }}", expression: ".items | length" } },
      1,
    )
    expect(step.title).toBe("Transform data")
    expect(step.detail).toBe(".items | length")
  })

  it("renders a wait/approval step", () => {
    const step = describeStep(
      { type: "wait", wait: { kind: "approval", approval_prompt: "Ship it?" } },
      1,
    )
    expect(step.title).toBe("Wait for approval")
    expect(step.detail).toBe("Ship it?")
  })

  it("renders a wait/datetime and a wait/event step", () => {
    expect(describeStep({ type: "wait", wait: { kind: "datetime", until: "2026-01-01T00:00:00Z" } }, 1).title).toBe(
      "Wait until a set time",
    )
    expect(describeStep({ type: "wait", wait: { kind: "event", event_type: "deploy.done" } }, 1).title).toBe(
      "Wait for event: deploy.done",
    )
  })

  it("renders a code step with its runtime", () => {
    const step = describeStep({ type: "code", code: { runtime: "python", code: "print(1)" } }, 1)
    expect(step.title).toBe("Run python code")
    expect(step.technical).toBe("code · python")
  })

  it("renders a call_pipeline step", () => {
    const step = describeStep({ type: "call_pipeline", pipeline_slug: "nightly-cleanup" }, 1)
    expect(step.title).toBe("Call routine nightly-cleanup")
  })

  it("degrades unknown step types instead of throwing", () => {
    expect(describeStep({ type: "teleport", id: "z" }, 1).title).toBe("teleport step")
    expect(describeStep({ id: "z" }, 1).title).toBe("Step z")
    expect(describeStep(null, 1).title).toBe("Step")
    expect(describeStep("garbage", 1).kind).toBe("unknown")
  })
})

describe("describeRoutine", () => {
  const dsl = {
    dsl_version: "1.0",
    name: "hn-standup-digest",
    description: "Daily HN digest to Slack",
    integrations_required: ["slack", " github ", "slack"],
    inputs: [
      { name: "limit", type: "integer", required: true },
      { name: "noise", required: false },
      { type: "string" }, // no name → dropped
    ],
    steps: [
      { id: "fetch", type: "http", http: { method: "GET", url: "https://news.ycombinator.com" } },
      { id: "summarize", type: "agent_run", agent_slug: "alex", prompt: "Summarize." },
      { id: "post", type: "http", http: { method: "POST", url: "https://hooks.slack.com/x", body: "#standup" } },
    ],
  }

  it("produces a structured readable summary", () => {
    const r = describeRoutine(dsl)
    expect(r.name).toBe("hn-standup-digest")
    expect(r.description).toBe("Daily HN digest to Slack")
    expect(r.steps).toHaveLength(3)
    expect(r.steps.map((s) => s.position)).toEqual([1, 2, 3])
    expect(r.steps[0].title).toBe("Fetch from Hacker News")
    expect(r.steps[1].title).toBe("Ask alex")
    expect(r.steps[2].title).toBe("Slack → #standup")
  })

  it("trims, de-dupes and drops blank integrations", () => {
    const r = describeRoutine(dsl)
    expect(r.integrations).toEqual(["slack", "github"])
  })

  it("keeps only named inputs with a defaulted type", () => {
    const r = describeRoutine(dsl)
    expect(r.inputs).toEqual([
      { name: "limit", type: "integer", required: true },
      { name: "noise", type: "string", required: false },
    ])
  })

  it("derives a manual trigger when none is declared", () => {
    expect(describeRoutine(dsl).trigger).toBe("Runs manually / on demand")
  })

  it("derives a schedule trigger from embedded schedules", () => {
    const r = describeRoutine({ ...dsl, schedules: [{ cron_expr: "0 8 * * 1-5" }] })
    expect(r.trigger).toBe("On schedule (0 8 * * 1-5)")
  })

  it("derives a trigger from a structured trigger object", () => {
    expect(describeRoutine({ trigger: { type: "schedule", cron: "0 9 * * *" } }).trigger).toBe(
      "On schedule (0 9 * * *)",
    )
    expect(describeRoutine({ trigger: { type: "webhook" } }).trigger).toBe("When its webhook is called")
    expect(describeRoutine({ trigger: { type: "event", event: "push" } }).trigger).toBe(
      'When the "push" event fires',
    )
  })

  it("handles an empty / malformed DSL without throwing", () => {
    const empty = describeRoutine({})
    expect(empty.steps).toEqual([])
    expect(empty.integrations).toEqual([])
    expect(empty.inputs).toEqual([])
    expect(empty.trigger).toBe("Runs manually / on demand")

    const garbage = describeRoutine(null)
    expect(garbage.steps).toEqual([])
    expect(garbage.name).toBeUndefined()
  })
})
