import { describe, it, expect } from "vitest"
import type { TurnPart } from "@/hooks/use-chat"
import {
  parentToolIdOf, summarizeGroup, toolIdOf, toolState, needsAttention,
  settleRetries, humanToolSummary, currentStepLabel, stableStringify, type ToolStep,
  stepPhrase, formatElapsed, turnActivitySummary,
} from "../chat-activity"

const p = (type: TurnPart["type"], metadata: Record<string, unknown> = {}): TurnPart => ({
  id: Math.random().toString(36).slice(2),
  type,
  content: "",
  timestamp: new Date(),
  metadata,
})

describe("toolState", () => {
  it("maps results to succeeded / failed / denied", () => {
    const call = p("tool_call", { tool_id: "a" })
    expect(toolState({ call, result: p("tool_result") }, false)).toBe("succeeded")
    expect(toolState({ call, result: p("tool_result", { is_error: true }) }, false)).toBe("failed")
    expect(toolState({ call, result: p("tool_result", { denied: true, is_error: true }) }, false)).toBe("denied")
  })
  it("is running while the turn streams and unknown once it ended without a result", () => {
    const call = p("tool_call")
    expect(toolState({ call }, true)).toBe("running")
    expect(toolState({ call }, false)).toBe("unknown")
  })
  it("prefers the typed activity state", () => {
    const typed = (state: string) => ({ ...p("activity"), activity: { kind: "shell", state } }) as never
    expect(toolState({ call: typed("interrupted") }, true)).toBe("interrupted")
    expect(toolState({ call: typed("input_streaming") }, true)).toBe("running")
    expect(toolState({ call: typed("failed"), result: p("tool_result") }, false)).toBe("failed")
  })
  it("degrades a state token it does not know to unknown, never to success", () => {
    const typed = (state: string) => ({ ...p("activity"), activity: { kind: "shell", state } }) as never
    for (const state of ["paused", "SUCCEEDED", "constructor", "toString", "__proto__"]) {
      expect(toolState({ call: typed(state), result: p("tool_result") }, false)).toBe("unknown")
      expect(toolState({ call: typed(state) }, true)).toBe("unknown")
    }
    expect(summarizeGroup(["succeeded", toolState({ call: typed("paused") }, false)]).title).not.toBe("Worked")
  })
  it("a typed running step is unknown once the turn stopped streaming", () => {
    const typed = (state: string) => ({ ...p("activity"), activity: { kind: "shell", state } }) as never
    expect(toolState({ call: typed("running") }, true)).toBe("running")
    expect(toolState({ call: typed("running") }, false)).toBe("unknown")
    expect(toolState({ call: typed("input_streaming") }, false)).toBe("unknown")
  })
  it("flags approvals", () => {
    expect(needsAttention({ ...p("activity"), activity: { kind: "shell", state: "awaiting_approval" } } as never)).toBe(true)
    expect(needsAttention(p("tool_call"))).toBe(false)
  })
})

describe("summarizeGroup", () => {
  it("is ok only when every step succeeded", () => {
    expect(summarizeGroup(["succeeded", "succeeded"]).tone).toBe("ok")
  })
  it("never reads green with a failure inside", () => {
    const s = summarizeGroup(["succeeded", "failed", "denied"])
    expect(s.tone).toBe("attention")
    expect(s.title).toBe("Activity incomplete")
    expect(s.detail).toBe("1 failed, 1 denied")
  })
  it("a failure in a turn that ran to its end reads Finished with the failure named, never Worked or green", () => {
    const s = summarizeGroup(["succeeded", "failed"])
    expect(s.tone).toBe("attention")
    expect(s.title).toBe("Finished")
    expect(s.detail).toBe("1 failed")
  })
  it("a failure while steps still run is Working, not Worked", () => {
    expect(summarizeGroup(["failed", "running"]).title).toBe("Working")
  })
  it("unknown and interrupted are not success", () => {
    expect(summarizeGroup(["succeeded", "unknown"]).tone).toBe("neutral")
    expect(summarizeGroup(["succeeded", "unknown"]).title).not.toBe("Worked")
    expect(summarizeGroup(["interrupted"]).tone).toBe("attention")
  })
  it("running group stays running; approval takes precedence", () => {
    expect(summarizeGroup(["succeeded", "running"]).tone).toBe("running")
    expect(summarizeGroup(["running", "awaiting_approval"]).title).toBe("Needs approval")
  })
})

describe("correlation ids", () => {
  it("reads typed fields first, then the legacy wire keys", () => {
    expect(toolIdOf({ ...p("tool_call", { tool_id: "m" }), tool_call_id: "t" })).toBe("t")
    expect(toolIdOf(p("tool_result", { tool_use_id: "u" }))).toBe("u")
    expect(parentToolIdOf({ ...p("tool_call"), parent_tool_call_id: "t" })).toBe("t")
    expect(parentToolIdOf(p("tool_call", { parent_tool_use_id: "u" }))).toBe("u")
    expect(toolIdOf(p("tool_call"))).toBeUndefined()
  })
})

const step = (tool: string): ToolStep => ({ call: p("tool_call", { tool_name: tool }) })
const stepWith = (tool: string, input: Record<string, unknown>): ToolStep => ({ call: p("tool_call", { tool_name: tool, input }), result: p("tool_result") })

describe("settleRetries", () => {
  // A step is [call, result]; parts get their position in the turn.
  const ordered = (steps: ToolStep[]) => {
    const index = new Map<string, number>()
    steps.forEach((st, i) => {
      index.set(st.call.id, i * 2)
      if (st.result) index.set(st.result.id, i * 2 + 1)
    })
    return (part: TurnPart) => index.get(part.id)
  }

  it("counts a failed step done again by the same tool on the same input as retried", () => {
    const steps = [stepWith("Bash", { command: "npm test" }), stepWith("Bash", { command: "npm test" })]
    const r = settleRetries(steps, ["failed", "succeeded"], ordered(steps))
    expect(r.retried).toBe(1)
    expect(summarizeGroup(r.states).title).toBe("Worked")
    expect(summarizeGroup(r.states).tone).toBe("ok")
  })
  it("npm test failing and ls succeeding is NOT a retry: the failure stays", () => {
    const steps = [stepWith("Bash", { command: "npm test" }), stepWith("Bash", { command: "ls" })]
    const r = settleRetries(steps, ["failed", "succeeded"], ordered(steps))
    expect(r.retried).toBe(0)
    expect(r.states[0]).toBe("failed")
  })
  it("file tools match on the file path, not the whole input", () => {
    const steps = [stepWith("Edit", { file_path: "/a.ts", old_string: "x" }), stepWith("Edit", { file_path: "/a.ts", old_string: "y" })]
    expect(settleRetries(steps, ["failed", "succeeded"], ordered(steps)).retried).toBe(1)
    const other = [stepWith("Edit", { file_path: "/a.ts" }), stepWith("Edit", { file_path: "/b.ts" })]
    expect(settleRetries(other, ["failed", "succeeded"], ordered(other)).retried).toBe(0)
  })
  it("compares inputs at every depth: nested differences are different attempts", () => {
    const a = [stepWith("Bash", { opts: { x: 1 } }), stepWith("Bash", { opts: { x: 2 } })]
    expect(settleRetries(a, ["failed", "succeeded"], ordered(a)).retried).toBe(0)
    const same = [stepWith("Bash", { b: 1, a: { y: 2, x: 1 } }), stepWith("Bash", { a: { x: 1, y: 2 }, b: 1 })]
    expect(settleRetries(same, ["failed", "succeeded"], ordered(same)).retried).toBe(1)
    const lists = [stepWith("Bash", { args: [{ k: 1 }] }), stepWith("Bash", { args: [{ k: 2 }] })]
    expect(settleRetries(lists, ["failed", "succeeded"], ordered(lists)).retried).toBe(0)
  })
  it("stableStringify sorts keys deeply and handles plain strings", () => {
    expect(stableStringify({ b: 1, a: { d: 1, c: 2 } })).toBe('{"a":{"c":2,"d":1},"b":1}')
    expect(stableStringify("ls")).toBe('"ls"')
  })
  it("does not treat parallel calls as retries: the later call began before the result", () => {
    const a = stepWith("Bash", { command: "ls" })
    const b = stepWith("Bash", { command: "ls" })
    const index = new Map([[a.call.id, 0], [b.call.id, 1], [(a.result as TurnPart).id, 2], [(b.result as TurnPart).id, 3]])
    expect(settleRetries([a, b], ["failed", "succeeded"], (part) => index.get(part.id)).retried).toBe(0)
  })
  it("settles nothing without a turn order or without an input", () => {
    const steps = [stepWith("Bash", { command: "ls" }), stepWith("Bash", { command: "ls" })]
    expect(settleRetries(steps, ["failed", "succeeded"]).retried).toBe(0)
    const bare = [{ call: p("tool_call", { tool_name: "Bash" }), result: p("tool_result") } as ToolStep, { call: p("tool_call", { tool_name: "Bash" }), result: p("tool_result") } as ToolStep]
    expect(settleRetries(bare, ["failed", "succeeded"], ordered(bare)).retried).toBe(0)
  })
  it("does not let an earlier success recover a later failure, nor turn denied into success", () => {
    const steps = [stepWith("Bash", { command: "ls" }), stepWith("Bash", { command: "ls" })]
    expect(settleRetries(steps, ["succeeded", "failed"], ordered(steps)).retried).toBe(0)
    expect(settleRetries(steps, ["denied", "succeeded"], ordered(steps)).states[0]).toBe("denied")
  })
})

describe("humanToolSummary and currentStepLabel", () => {
  it("describes what the steps did without raw commands", () => {
    expect(humanToolSummary([step("Bash"), step("Bash"), step("Read")])).toBe("ran commands, read files")
    expect(humanToolSummary([step("Custom")])).toBe("used Custom")
  })
  it("shows an MCP tool by its own name and caps unknown tools at three", () => {
    expect(humanToolSummary([step("mcp__crewship__sheet_set_range")])).toBe("used sheet_set_range")
    expect(humanToolSummary([step("A"), step("B"), step("C"), step("D"), step("E")])).toBe("used A, used B, used C, +2")
  })
  it("names the step that is running now, as one sentence of what the agent does", () => {
    expect(currentStepLabel([step("Read"), step("Bash")], ["succeeded", "running"])).toBe("Running a command…")
    expect(currentStepLabel([step("Read")], ["succeeded"])).toBeUndefined()
  })
})

describe("the turn's one activity line", () => {
  const apply = (commands: unknown[]) => stepWith("mcp__crewship__craft_sheet_apply", { generation: "1", base_version: "3", request_id: "r", commands })
  it("says what a Sheet step does, with the sheet and the range", () => {
    expect(stepPhrase(stepWith("mcp__crewship__craft_sheet_read", { query: { command: "document.inspect", params: {} } }), "doing")).toBe("Looking at the Sheet")
    expect(stepPhrase(stepWith("craft_sheet_read", { query: { command: "sheet.read", params: { sheet: "L", range: "A1:D10" } } }), "doing")).toBe("Reading 'L'!A1:D10")
    expect(stepPhrase(stepWith("craft_sheet_read", { query: { command: "range.columnByHeader", params: { header: "Loan ID" } } }), "doing")).toBe("Looking for the column “Loan ID”")
    const one = apply([{ command: "range.setValues", params: { sheet: "L", range: "B8:B577", values: [[1]] } }])
    expect(stepPhrase(one, "doing")).toBe("Changing 'L'!B8:B577 (570 cells)")
    expect(stepPhrase(one, "done")).toBe("Changed 'L'!B8:B577 (570 cells)")
    expect(stepPhrase(apply([{ command: "cell.set", params: { sheet: "Q1", cell: "A1", input: "2" } }, { command: "range.clear", params: { sheet: "Q2", range: "B1:B9" } }]), "doing")).toBe("Changing 2 ranges in the Sheet (10 cells)")
    expect(stepPhrase(apply([{ command: "range.clear", params: { sheet: "it's", range: "C1:C4" } }]), "doing")).toBe("Clearing 'it''s'!C1:C4 (4 cells)")
    expect(stepPhrase(stepWith("craft_sheet_acquire", {}), "doing")).toBe("Taking over editing of the Sheet")
    expect(stepPhrase(stepWith("craft_sheet_release", { generation: "1" }), "done")).toBe("Handed the Sheet back")
    expect(stepPhrase(stepWith("craft_sheet_propose", {}), "doing")).toBe("Preparing a proposed change to the Sheet")
  })
  // Input shapes as the sidecar declares them (internal/sidecar/native_craft_mcp.go
  // nativeCraftToolSchema, craft_sheet_mcp.go craftSheetDescriptionText), under the
  // routines MCP server name (internal/sidecar/routine_mcp.go RoutinesMCPServerName).
  describe("craft_sheet tools as a real agent calls them", () => {
    const t = (op: string) => `mcp__crewship-routines__craft_sheet_${op}`
    const both = (name: string, input: Record<string, unknown>) => [stepPhrase(stepWith(name, input), "doing"), stepPhrase(stepWith(name, input), "done")]
    it("reads", () => {
      expect(both(t("read"), { query: { command: "sheet.read", params: { sheet: "L" } } })).toEqual(["Reading sheet L", "Read sheet L"])
      expect(both(t("read"), { query: { command: "sheet.read", params: { range: "A7:F577", sheet: "L", formulas: true } } })).toEqual(["Reading 'L'!A7:F577", "Read 'L'!A7:F577"])
      expect(both(t("read"), { query: { command: "cell.get", params: { cell: "B8", sheet: "L" } } })).toEqual(["Reading 'L'!B8", "Read 'L'!B8"])
      expect(both(t("read"), { query: { command: "sheet.read", params: { range: "A1:D10" } } })).toEqual(["Reading A1:D10", "Read A1:D10"])
      expect(both(t("read"), { query: { command: "document.inspect", params: {} } })).toEqual(["Looking at the Sheet", "Looked at the Sheet"])
      expect(both(t("read"), { query: { command: "range.columnByHeader", params: { header: "Loan ID", sheet: "L", matchCase: false } } })).toEqual(["Looking for the column “Loan ID”", "Looked for the column “Loan ID”"])
      expect(both(t("read"), {})).toEqual(["Reading the Sheet", "Read the Sheet"])
    })
    it("takes, keeps and hands back editing", () => {
      expect(both(t("acquire"), {})).toEqual(["Taking over editing of the Sheet", "Took over editing of the Sheet"])
      expect(both(t("renew"), { generation: "4" })).toEqual(["Keeping the Sheet open for editing", "Kept the Sheet open for editing"])
      expect(both(t("release"), { generation: "4" })).toEqual(["Handing the Sheet back", "Handed the Sheet back"])
    })
    it("changes, with where and how many cells", () => {
      const apply = (commands: unknown[]) => ({ generation: "4", base_version: "12", request_id: "number-loans-1", commands })
      expect(both(t("apply"), apply([{ command: "range.setValues", params: { range: "B8:B577", values: [["LN-0001"]], sheet: "L" } }]))).toEqual(["Changing 'L'!B8:B577 (570 cells)", "Changed 'L'!B8:B577 (570 cells)"])
      expect(both(t("apply"), apply([{ command: "cell.set", params: { cell: "B8", input: "LN-0001", sheet: "L" } }]))).toEqual(["Changing 'L'!B8", "Changed 'L'!B8"])
      expect(both(t("apply"), apply([{ command: "range.setValues", params: { range: "B8:C9", values: [[1, 2], [3, 4]], sheet: "L" } }, { command: "cell.set", params: { cell: "A1", input: "x", sheet: "Summary" } }]))).toEqual(["Changing 2 ranges in the Sheet (5 cells)", "Changed 2 ranges in the Sheet (5 cells)"])
      expect(both(t("apply"), apply([{ command: "range.clear", params: { range: "D8:D577", sheet: "L" } }]))).toEqual(["Clearing 'L'!D8:D577 (570 cells)", "Cleared 'L'!D8:D577 (570 cells)"])
      expect(both(t("apply"), {})).toEqual(["Changing the Sheet", "Changed the Sheet"])
      expect(both(t("propose"), { ...apply([{ command: "range.setValues", params: { range: "B8:B577", values: [[1]], sheet: "L" } }]), summary: "Number the loans" })).toEqual(["Preparing a proposed change to 'L'!B8:B577", "Proposed a change to 'L'!B8:B577"])
    })
    it("never names the MCP server or turns an input object into text", () => {
      const names = ["read", "acquire", "renew", "apply", "propose", "release", "undo"].flatMap(op => both(t(op), { query: { command: "x", params: { sheet: { a: 1 } } }, commands: [{ command: "range.setValues", params: { range: { a: 1 }, sheet: ["L"] } }] }))
      for (const phrase of names) {
        expect(phrase).not.toMatch(/mcp__|crewship-routines|\[object Object\]|craft_sheet/)
      }
      expect(both(t("undo"), {})).toEqual(["Working on the Sheet", "Worked on the Sheet"])
      expect(stepPhrase(stepWith("mcp__crewship-routines__routine_list", { query: { a: 1 } }), "done")).toBe("Used routine_list")
    })
  })
  it("reads a typed activity's string parameters as the JSON they hold", () => {
    const typed = { call: { ...p("activity"), activity: { kind: "tool", name: "mcp__crewship__craft_sheet_apply", state: "running", parameters: { commands: JSON.stringify([{ command: "range.setValues", params: { sheet: "L", range: "B8:B577" } }]) } } } } as ToolStep
    expect(stepPhrase(typed, "doing")).toBe("Changing 'L'!B8:B577 (570 cells)")
    const read = { call: { ...p("activity"), activity: { kind: "tool", name: "craft_sheet_read", state: "running", parameters: { query: "not json" } } } } as ToolStep
    expect(stepPhrase(read, "doing")).toBe("Reading the Sheet")
  })
  it("says what other steps do without raw commands or paths", () => {
    expect(stepPhrase(stepWith("Bash", { command: "rm -rf /workspace/inspect2" }), "doing")).toBe("Running a command")
    expect(stepPhrase(stepWith("Bash", { command: "ls" }), "done")).toBe("Ran a command")
    expect(stepPhrase(stepWith("Read", { file_path: "/workspace/loans.xlsx" }), "doing")).toBe("Reading the workbook")
    expect(stepPhrase(stepWith("Read", { file_path: "/workspace/notes/plan.md" }), "done")).toBe("Read plan.md")
    expect(stepPhrase(stepWith("Edit", { file_path: "/workspace/a.ts" }), "doing")).toBe("Editing a.ts")
    expect(stepPhrase(stepWith("WebFetch", { url: "https://example.test/x?y=1" }), "doing")).toBe("Opening example.test")
    expect(stepPhrase(step("mcp__crewship__sheet_set_range"), "doing")).toBe("Using sheet_set_range")
    expect(stepPhrase(step("mcp__crewship__sheet_set_range"), "done")).toBe("Used sheet_set_range")
  })
  it("a Sheet change that is running is the sentence of the turn", () => {
    const steps = [stepWith("craft_sheet_read", { query: { command: "document.inspect", params: {} } }), apply([{ command: "range.setValues", params: { sheet: "L", range: "B8:B577", values: [] } }])]
    expect(currentStepLabel(steps, ["succeeded", "running"])).toBe("Changing 'L'!B8:B577 (570 cells)…")
  })
  it("formats the elapsed time a person reads", () => {
    expect(formatElapsed(400)).toBe("1 s")
    expect(formatElapsed(41_000)).toBe("41 s")
    expect(formatElapsed(125_000)).toBe("2 min 5 s")
    expect(formatElapsed(120_000)).toBe("2 min")
  })
  it("says Done only when every step succeeded and the run completed", () => {
    expect(turnActivitySummary(["succeeded", "succeeded"], {})).toMatchObject({ tone: "ok", title: "Done", detail: "" })
    expect(turnActivitySummary(["succeeded", "succeeded"], { retried: 1 })).toMatchObject({ title: "Done", detail: "1 retried" })
    expect(turnActivitySummary(["succeeded", "failed"], {})).toMatchObject({ tone: "attention", title: "Finished", detail: "1 failed" })
    expect(turnActivitySummary(["succeeded", "unknown"], {}).title).not.toBe("Done")
    expect(turnActivitySummary(["succeeded", "denied"], {}).title).not.toBe("Done")
    expect(turnActivitySummary(["interrupted"], {}).title).not.toBe("Done")
    expect(turnActivitySummary(["succeeded", "running"], { streaming: true })).toMatchObject({ tone: "running", title: "Working" })
  })
  it("a run that failed, stopped or was interrupted never reads Done, even with every step succeeded", () => {
    expect(turnActivitySummary(["succeeded"], { turnState: "failed" })).toMatchObject({ tone: "attention", title: "Failed" })
    expect(turnActivitySummary(["succeeded"], { turnState: "cancelled" })).toMatchObject({ title: "Stopped" })
    expect(turnActivitySummary(["succeeded"], { turnState: "interrupted" })).toMatchObject({ title: "Interrupted" })
  })
})
