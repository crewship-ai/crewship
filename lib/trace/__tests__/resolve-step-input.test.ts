import { describe, it, expect } from "vitest"
import { resolveStepInput } from "@/lib/trace/resolve-step-input"
import type { TraceStep } from "@/lib/trace/types"

describe("resolveStepInput", () => {
  it("returns the declared prompt for an agent_run step", () => {
    const step: TraceStep = { id: "summarize", type: "agent_run", prompt: "Summarize the repo." }
    const entries = resolveStepInput(step, {})
    expect(entries).toEqual([{ key: "prompt", value: "Summarize the repo.", hasRefs: false }])
  })

  it("substitutes a {{ steps.X.output }} ref with the upstream string output", () => {
    const step: TraceStep = {
      id: "review",
      type: "agent_run",
      prompt: "Review this:\n{{ steps.fetch.output }}",
    }
    const entries = resolveStepInput(step, {
      stepOutputs: { fetch: "diff --git a b" },
    })
    expect(entries[0].key).toBe("prompt")
    expect(entries[0].value).toBe("Review this:\ndiff --git a b")
    expect(entries[0].hasRefs).toBe(true)
  })

  it("walks a dotted output path", () => {
    const step: TraceStep = {
      id: "notify",
      type: "http",
      http: { method: "POST", url: "https://api/{{ steps.fetch.output.id }}" },
    }
    const entries = resolveStepInput(step, {
      stepOutputs: { fetch: { id: "abc123", name: "x" } },
    })
    const url = entries.find((e) => e.key === "http.url")
    expect(url?.value).toBe("https://api/abc123")
    expect(url?.hasRefs).toBe(true)
  })

  it("returns a raw object when the whole string is a single object-valued ref", () => {
    const step: TraceStep = {
      id: "shape",
      type: "transform",
      transform: { input: "{{ steps.fetch.output }}", expression: ".body" },
    }
    const obj = { body: { url: "https://x" }, status: 200 }
    const entries = resolveStepInput(step, { stepOutputs: { fetch: obj } })
    const input = entries.find((e) => e.key === "transform.input")
    expect(input?.value).toEqual(obj)
    expect(input?.hasRefs).toBe(true)
  })

  it("resolves {{ inputs.Y }} against run inputs", () => {
    const step: TraceStep = {
      id: "greet",
      type: "agent_run",
      prompt: "Hello {{ inputs.name }}, welcome",
    }
    const entries = resolveStepInput(step, { inputs: { name: "Ada" } })
    expect(entries[0].value).toBe("Hello Ada, welcome")
  })

  it("leaves unresolved refs literal (no upstream value yet)", () => {
    const step: TraceStep = {
      id: "review",
      type: "agent_run",
      prompt: "x = {{ steps.missing.output }}",
    }
    const entries = resolveStepInput(step, { stepOutputs: {} })
    expect(entries[0].value).toBe("x = {{ steps.missing.output }}")
    expect(entries[0].hasRefs).toBe(true)
  })

  it("leaves non steps/inputs refs (e.g. secrets) literal", () => {
    const step: TraceStep = {
      id: "call",
      type: "http",
      http: { url: "https://api", headers: { Authorization: "Bearer {{ secrets.token }}" } },
    }
    const entries = resolveStepInput(step, {})
    const headers = entries.find((e) => e.key === "http.headers")
    expect(headers?.value).toEqual({ Authorization: "Bearer {{ secrets.token }}" })
  })

  it("deep-resolves nested objects (headers values)", () => {
    const step: TraceStep = {
      id: "call",
      type: "http",
      http: {
        url: "https://api",
        headers: { "X-Id": "{{ steps.fetch.output.id }}", "X-Static": "v" },
      },
    }
    const entries = resolveStepInput(step, { stepOutputs: { fetch: { id: "42" } } })
    const headers = entries.find((e) => e.key === "http.headers")
    expect(headers?.value).toEqual({ "X-Id": "42", "X-Static": "v" })
    expect(headers?.hasRefs).toBe(true)
  })

  it("flattens call_pipeline inputs into inputs.<key> entries", () => {
    const step: TraceStep = {
      id: "sub",
      type: "call_pipeline",
      pipeline_slug: "deploy",
      inputs: { env: "prod", ref: "{{ steps.build.output.sha }}" },
    }
    const entries = resolveStepInput(step, { stepOutputs: { build: { sha: "deadbeef" } } })
    expect(entries.map((e) => e.key)).toEqual(["pipeline_slug", "inputs.env", "inputs.ref"])
    expect(entries.find((e) => e.key === "inputs.ref")?.value).toBe("deadbeef")
  })

  it("drops empty / nullish declared fields", () => {
    const step: TraceStep = {
      id: "call",
      type: "http",
      http: { method: "GET", url: "https://api", body: "", headers: {} },
    }
    const entries = resolveStepInput(step, {})
    expect(entries.map((e) => e.key)).toEqual(["http.method", "http.url"])
  })

  it("never throws on a malformed step", () => {
    // @ts-expect-error — exercising the defensive catch path
    expect(() => resolveStepInput(null, {})).not.toThrow()
    // @ts-expect-error
    expect(resolveStepInput(null, {})).toEqual([])
  })

  it("returns [] for step types with no declared input", () => {
    const step = { id: "x", type: "unknown" } as unknown as TraceStep
    expect(resolveStepInput(step, {})).toEqual([])
  })
})
