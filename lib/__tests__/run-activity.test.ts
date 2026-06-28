import { describe, it, expect } from "vitest"
import type { JournalEntry } from "@/lib/types/journal"
import { humanizeEntry, humanizeRun, formatBytes, formatDuration } from "@/lib/run-activity"

/** Minimal JournalEntry factory — fills required fields, lets tests override. */
function entry(over: Partial<JournalEntry> & Pick<JournalEntry, "entry_type">): JournalEntry {
  return {
    id: over.id ?? "j_" + Math.random().toString(16).slice(2),
    workspace_id: "ws_1",
    ts: over.ts ?? "2026-06-26T10:31:00.000Z",
    entry_type: over.entry_type,
    severity: over.severity ?? "info",
    actor_type: over.actor_type ?? "agent",
    summary: over.summary ?? "",
    ...over,
  }
}

describe("formatBytes", () => {
  it("renders B / KB / MB", () => {
    expect(formatBytes(412)).toBe("412 B")
    expect(formatBytes(2048)).toBe("2.0 KB")
    expect(formatBytes(5 * 1024 * 1024)).toBe("5.0 MB")
  })
  it("is defensive about junk", () => {
    expect(formatBytes(undefined)).toBeNull()
    expect(formatBytes(-1)).toBeNull()
    expect(formatBytes(NaN)).toBeNull()
  })
})

describe("formatDuration", () => {
  it("renders ms / s", () => {
    expect(formatDuration(820)).toBe("820ms")
    expect(formatDuration(1200)).toBe("1.2s")
    expect(formatDuration(65000)).toBe("1m 5s")
  })
  it("is defensive", () => {
    expect(formatDuration(undefined)).toBeNull()
    expect(formatDuration(-5)).toBeNull()
  })
  it("spills rounding into the minute form — never 60.0s or 1m 60s", () => {
    expect(formatDuration(59_960)).toBe("1m 0s")
    expect(formatDuration(119_600)).toBe("2m 0s")
  })
})

describe("humanizeEntry", () => {
  it("run.started → active opener with actor", () => {
    const row = humanizeEntry(entry({ entry_type: "run.started", actor_id: "Riley", payload: { trigger_type: "issue" } }))
    expect(row).not.toBeNull()
    expect(row!.title).toBe("Run started")
    expect(row!.tone).toBe("active")
  })

  it("run.completed → success with cost + steps", () => {
    const row = humanizeEntry(entry({ entry_type: "run.completed", payload: { cost_usd: 0.0021, steps: 4, duration_ms: 7000 } }))
    expect(row!.title).toBe("Completed")
    expect(row!.tone).toBe("success")
    expect(row!.meta).toContain("$0.0021")
    expect(row!.meta).toContain("4 steps")
  })

  it("run.failed → error tone, error message as detail", () => {
    const row = humanizeEntry(entry({ entry_type: "run.failed", severity: "error", payload: { error: "boom" } }))
    expect(row!.tone).toBe("error")
    expect(row!.detail).toBe("boom")
  })

  it("network.egress → host title + status meta", () => {
    const row = humanizeEntry(entry({ entry_type: "network.egress", payload: { host: "news.ycombinator.com", method: "GET", status_code: 200 } }))
    expect(row!.title).toContain("news.ycombinator.com")
    expect(row!.meta).toContain("200")
  })

  it("exec.command → command as detail, exit/duration meta, error tone on nonzero exit", () => {
    const ok = humanizeEntry(entry({ entry_type: "exec.command", payload: { command: "curl -s x | grep y", exit_code: 0, duration_ms: 1200 } }))
    expect(ok!.detail).toContain("curl")
    expect(ok!.meta).toContain("1.2s")
    expect(ok!.tone).toBe("default")
    const bad = humanizeEntry(entry({ entry_type: "exec.command", payload: { command: "false", exit_code: 1 } }))
    expect(bad!.tone).toBe("error")
    expect(bad!.meta).toContain("exit 1")
  })

  it("exec.command accepts cmd alias for command", () => {
    const row = humanizeEntry(entry({ entry_type: "exec.command", payload: { cmd: "ls -la", exit_code: 0 } }))
    expect(row!.detail).toContain("ls -la")
  })

  it("file.written → path detail + size meta, delete uses op", () => {
    const wrote = humanizeEntry(entry({ entry_type: "file.written", payload: { path: "/tmp/x.txt", size: 412, op: "created" } }))
    expect(wrote!.title).toBe("Wrote file")
    expect(wrote!.detail).toBe("/tmp/x.txt")
    expect(wrote!.meta).toBe("412 B")
    const del = humanizeEntry(entry({ entry_type: "file.written", payload: { path: "/tmp/x.txt", op: "deleted" } }))
    expect(del!.title).toBe("Deleted file")
  })

  it("noise types are dropped (null)", () => {
    for (const t of ["exec.output_chunk", "container.metrics", "container.snapshot", "llm.cache_hit", "agent.status_change"]) {
      expect(humanizeEntry(entry({ entry_type: t }))).toBeNull()
    }
  })

  it("unknown-but-not-noise type falls back to summary text", () => {
    const row = humanizeEntry(entry({ entry_type: "memory.updated", summary: "Saved a fact about the user" }))
    expect(row).not.toBeNull()
    expect(row!.title).toBe("Saved a fact about the user")
  })

  it("unknown type with no summary is dropped", () => {
    expect(humanizeEntry(entry({ entry_type: "totally.unknown", summary: "" }))).toBeNull()
  })
})

describe("humanizeEntry — pipeline (routine) runs", () => {
  it("pipeline.run.started → active opener with step count", () => {
    const row = humanizeEntry(entry({ entry_type: "pipeline.run.started", payload: { step_count: 2, run_id: "r1" } }))
    expect(row!.title).toBe("Routine started")
    expect(row!.tone).toBe("active")
    expect(row!.meta).toContain("2 steps")
  })

  it("pipeline.run.completed → success with total cost + duration", () => {
    const row = humanizeEntry(entry({ entry_type: "pipeline.run.completed", payload: { total_cost_usd: 0.0001, total_duration_ms: 820 } }))
    expect(row!.title).toBe("Completed")
    expect(row!.tone).toBe("success")
    expect(row!.meta).toContain("$0.0001")
    expect(row!.meta).toContain("820ms")
  })

  it("pipeline.run.failed → error tone with error + failed step", () => {
    const row = humanizeEntry(entry({ entry_type: "pipeline.run.failed", severity: "error", payload: { error_message: "404 page not found", failed_at_step: "convert" } }))
    expect(row!.tone).toBe("error")
    expect(row!.detail).toBe("404 page not found")
    expect(row!.meta).toContain("convert")
  })

  it("pipeline.step.completed → step id title, output preview detail, cost/duration meta", () => {
    const row = humanizeEntry(entry({ entry_type: "pipeline.step.completed", payload: { step_id: "convert", output_preview: "[{...}]", duration_ms: 1200, cost_usd: 0.0001 } }))
    expect(row!.title).toContain("convert")
    expect(row!.detail).toBe("[{...}]")
    expect(row!.meta).toContain("1.2s")
  })

  it("pipeline.step.failed → error tone with class + message", () => {
    const row = humanizeEntry(entry({ entry_type: "pipeline.step.failed", severity: "error", payload: { step_id: "convert", error_class: "http", error_message_preview: "boom" } }))
    expect(row!.tone).toBe("error")
    expect(row!.title).toContain("convert")
    expect(row!.detail).toBe("boom")
  })

  it("pipeline.step.started and pipeline.dry_run are dropped as noise", () => {
    expect(humanizeEntry(entry({ entry_type: "pipeline.step.started", payload: { step_id: "x" } }))).toBeNull()
    expect(humanizeEntry(entry({ entry_type: "pipeline.dry_run" }))).toBeNull()
  })
})

describe("humanizeRun", () => {
  it("filters noise, keeps order ascending by ts", () => {
    const rows = humanizeRun([
      entry({ entry_type: "run.completed", ts: "2026-06-26T10:31:09Z", payload: { cost_usd: 0.002 } }),
      entry({ entry_type: "container.metrics", ts: "2026-06-26T10:31:05Z" }),
      entry({ entry_type: "run.started", ts: "2026-06-26T10:31:02Z" }),
      entry({ entry_type: "file.written", ts: "2026-06-26T10:31:08Z", payload: { path: "/a" } }),
    ])
    expect(rows.map((r) => r.title)).toEqual(["Run started", "Wrote file", "Completed"])
  })

  it("orders by parsed time across mixed RFC3339 precision", () => {
    const rows = humanizeRun([
      entry({ entry_type: "run.completed", ts: "2026-06-26T10:31:09.500Z", payload: { cost_usd: 0.002 } }),
      entry({ entry_type: "exec.command", ts: "2026-06-26T10:31:09Z", payload: { command: "x" } }),
    ])
    // 09Z precedes 09.5Z, even though a raw string sort would flip them.
    expect(rows.map((r) => r.title)).toEqual(["Ran command", "Completed"])
  })
})
