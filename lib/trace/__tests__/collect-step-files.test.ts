import { describe, it, expect } from "vitest"
import { collectStepFiles, basename } from "@/lib/trace/collect-step-files"
import type { SubSpan } from "@/lib/trace/types"

function span(partial: Partial<SubSpan> & { artifact_path?: string }): SubSpan {
  const { artifact_path, ...rest } = partial
  return {
    kind: "write",
    name: "Write",
    status: "ok",
    attributes: artifact_path ? { artifact_path } : {},
    ...rest,
  } as SubSpan
}

describe("basename", () => {
  it("extracts the trailing path segment", () => {
    expect(basename("/workspace/sysfacts.yml")).toBe("sysfacts.yml")
    expect(basename("a/b/c.txt")).toBe("c.txt")
    expect(basename("plain.md")).toBe("plain.md")
    expect(basename("C:\\win\\file.json")).toBe("file.json")
  })
})

describe("collectStepFiles", () => {
  it("collects distinct artifact paths from sub-spans with provenance", () => {
    const files = collectStepFiles(
      [
        span({ kind: "write", name: "Write", artifact_path: "/workspace/sysfacts.yml" }),
        span({ kind: "read", name: "Read", artifact_path: "/workspace/sysfacts.yml" }),
        span({ kind: "bash", name: "ansible", artifact_path: "/workspace/play.yml" }),
      ],
      "agent_run",
      undefined,
    )
    expect(files).toHaveLength(2)
    const sysfacts = files.find((f) => f.name === "sysfacts.yml")!
    expect(sysfacts.source).toBe("action")
    expect(sysfacts.fetchable).toBe(true)
    expect(sysfacts.touchedBy).toEqual([
      { kind: "write", name: "Write" },
      { kind: "read", name: "Read" },
    ])
  })

  it("dedupes identical touches on the same file", () => {
    const files = collectStepFiles(
      [
        span({ kind: "read", name: "Read", artifact_path: "/a.txt" }),
        span({ kind: "read", name: "Read", artifact_path: "/a.txt" }),
      ],
      "agent_run",
      undefined,
    )
    expect(files[0].touchedBy).toEqual([{ kind: "read", name: "Read" }])
  })

  it("ignores sub-spans without an artifact_path", () => {
    const files = collectStepFiles(
      [span({ kind: "think", name: "thinking" })],
      "agent_run",
      undefined,
    )
    expect(files).toEqual([])
  })

  it("adds inferred file refs from output as fetchable, output-sourced", () => {
    const files = collectStepFiles([], "agent_run", "wrote results to report.md and data.csv")
    const names = files.map((f) => f.name)
    expect(names).toContain("report.md")
    expect(names).toContain("data.csv")
    expect(files.every((f) => f.source === "output" && f.fetchable)).toBe(true)
  })

  it("carries a JSON output artifact inline (not fetchable)", () => {
    const files = collectStepFiles([], "http", '{"ok":true,"n":2}')
    const json = files.find((f) => f.inlineKind === "json")
    expect(json).toBeTruthy()
    expect(json!.fetchable).toBe(false)
    expect(json!.inlineContent).toEqual({ ok: true, n: 2 })
  })

  it("lets action files win over output refs of the same path", () => {
    const files = collectStepFiles(
      [span({ kind: "write", name: "Write", artifact_path: "report.md" })],
      "agent_run",
      "see report.md",
    )
    const report = files.filter((f) => f.name === "report.md")
    expect(report).toHaveLength(1)
    expect(report[0].source).toBe("action")
    expect(report[0].touchedBy).toEqual([{ kind: "write", name: "Write" }])
  })

  it("orders action files before output-inferred files", () => {
    const files = collectStepFiles(
      [span({ kind: "write", name: "Write", artifact_path: "/out/a.yml" })],
      "agent_run",
      "also touched b.txt",
    )
    expect(files[0].name).toBe("a.yml")
    expect(files[0].source).toBe("action")
    expect(files.some((f) => f.name === "b.txt" && f.source === "output")).toBe(true)
  })

  it("handles undefined sub-spans + empty output", () => {
    expect(collectStepFiles(undefined, "agent_run", undefined)).toEqual([])
  })
})
