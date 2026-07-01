import { describe, it, expect } from "vitest"
import { describeStep, hostOf } from "@/lib/routine-step-describe"

// The shared module is the single source of truth for per-step prose + URL
// parsing across every routine surface (readable summary, plain-step list,
// flow diagram, mini-trace). routine-readable.test.ts and routine-flow.test.ts
// exercise it transitively; these tests pin the canonical behaviors directly
// so a regression here is caught at the source.

describe("hostOf (canonical)", () => {
  it("extracts the hostname and strips a leading www.", () => {
    expect(hostOf("https://www.example.com/a/b?c=1")).toBe("example.com")
    expect(hostOf("https://news.ycombinator.com")).toBe("news.ycombinator.com")
  })

  it("falls back to a cleaned raw string for templated / scheme-less URLs (never throws)", () => {
    expect(() => hostOf("{{ inputs.url }}")).not.toThrow()
    expect(hostOf("https://{{ inputs.host }}/x")).toContain("{{")
    expect(hostOf("")).toBe("")
  })
})

describe("describeStep (canonical)", () => {
  it("applies the known-integration label via hostOf", () => {
    const post = describeStep(
      { type: "http", http: { method: "POST", url: "https://hooks.slack.com/services/x" } },
      1,
    )
    expect(post.title).toBe("Send to Slack")
    const hn = describeStep(
      { type: "http", http: { method: "GET", url: "https://news.ycombinator.com" } },
      1,
    )
    expect(hn.title).toBe("Fetch from Hacker News")
  })

  it("renders the remaining step kinds in plain language", () => {
    expect(describeStep({ type: "agent_run", agent_slug: "alex" }, 1).title).toBe("Ask alex")
    expect(describeStep({ type: "transform", transform: { expression: ".x" } }, 1).detail).toBe(".x")
    expect(describeStep({ type: "code", code: { runtime: "python" } }, 1).title).toBe("Run python code")
    expect(describeStep({ type: "call_pipeline", pipeline_slug: "n" }, 1).title).toBe("Call routine n")
  })

  it("never throws on malformed input", () => {
    expect(describeStep(null, 1).kind).toBe("unknown")
    expect(describeStep("garbage", 1).kind).toBe("unknown")
  })
})
