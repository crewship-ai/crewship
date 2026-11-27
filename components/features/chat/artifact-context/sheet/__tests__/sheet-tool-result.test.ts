import { describe, it, expect } from "vitest"
import type { TurnPart } from "@/hooks/use-chat"
import { liftArtifactToolNodes } from "../../projects/artifact-tool-card"
import { isSheetApplyToolName, sheetApplyActivity } from "../sheet-tool-result"

describe("isSheetApplyToolName", () => {
  it("takes the bare name and the MCP-prefixed one, nothing else", () => {
    expect(isSheetApplyToolName("craft_sheet_apply")).toBe(true)
    expect(isSheetApplyToolName("mcp__crewship__craft_sheet_apply")).toBe(true)
    expect(isSheetApplyToolName("mcp__crewship-sheet__craft_sheet_apply")).toBe(true)
    expect(isSheetApplyToolName("craft_sheet_read")).toBe(false)
    expect(isSheetApplyToolName("mcp__x__craft_sheet_apply_all")).toBe(false)
    expect(isSheetApplyToolName(undefined)).toBe(false)
  })
})

describe("sheetApplyActivity", () => {
  // The result says what changed, counted by the server, and the operation to undo.
  it("reads the change summary and the operation id, and ignores a malformed summary", () => {
    const body = { page_id: "pg_1", version: 7, base_version: 6, operation_id: "op-9", summary: { cells_changed: 570, sheets: ["L"], ranges: ["'L'!L2:L571"] } }
    expect(sheetApplyActivity({ content: JSON.stringify(body) })).toMatchObject({ state: "done", pageId: "pg_1", operationId: "op-9", baseVersion: "6", summary: { cellsChanged: 570, sheets: ["L"], ranges: ["'L'!L2:L571"], truncated: false } })
    for (const summary of [{ cells_changed: -1, sheets: [], ranges: [] }, { cells_changed: 1, sheets: "L", ranges: [] }, { cells_changed: 1, sheets: Array(21).fill("L"), ranges: [] }])
      expect(sheetApplyActivity({ content: JSON.stringify({ ...body, summary }) }).summary, JSON.stringify(summary)).toBeUndefined()
    expect(sheetApplyActivity({ content: JSON.stringify({ ...body, operation_id: "op 9" }) }).operationId).toBeUndefined()
    expect(sheetApplyActivity({ content: JSON.stringify(body) }).undoable, "Undo only when the server says it can").toBeUndefined()
    expect(sheetApplyActivity({ content: JSON.stringify({ ...body, undoable: true }) }).undoable).toBe(true)
    // A change the server says it cannot undo is named so, to be explained on the card.
    expect(sheetApplyActivity({ content: JSON.stringify({ ...body, undoable: false }) }).undoable).toBe(false)
    // The server's empty lists may come as null (Go nil slices): they mean none.
    expect(sheetApplyActivity({ content: JSON.stringify({ ...body, summary: { cells_changed: 0, sheets: null, ranges: null } }) }).summary).toEqual({ cellsChanged: 0, sheets: [], ranges: [], truncated: false })
  })
  const done = (body: unknown) => sheetApplyActivity({ content: typeof body === "string" ? body : JSON.stringify(body) })
  it("is running without a result and failed on an error", () => {
    expect(sheetApplyActivity(null).state).toBe("running")
    expect(sheetApplyActivity({ content: "boom", isError: true }).state).toBe("failed")
  })
  it("reads only which Sheet changed", () => {
    expect(done({ page_id: "pg_1", slug: "plan", version: 3, changed_cells: 12, receipt: { any: "thing" } })).toEqual({ state: "done", pageId: "pg_1", slug: "plan", saved: { version: "3" } })
    expect(done({ slug: "plan" })).toEqual({ state: "done", slug: "plan", pageId: undefined, saved: undefined })
    const hash = "sha256:" + "a".repeat(64)
    expect(done({ page_id: "pg_1", version: 5, content_hash: hash, base_version: 4, commands: [{ command: "cell.set", params: { a: 1 } }] }).saved)
      .toEqual({ version: "5", contentHash: hash, baseVersion: "4", commands: [{ command: "cell.set", params: { a: 1 } }] })
  })
  it("unwraps a text-content result", () => {
    expect(done([{ type: "text", text: JSON.stringify({ page_id: "pg_2" }) }]).pageId).toBe("pg_2")
    expect(done({ content: [{ type: "text", text: JSON.stringify({ slug: "s" }) }] }).slug).toBe("s")
  })
  it("passes on the server's reason from its closed list only", () => {
    const refused = (body: unknown) => sheetApplyActivity({ content: JSON.stringify(body), isError: true })
    expect(refused({ error: "x", reason: "agent_policy" })).toEqual({ state: "failed", reason: "agent_policy" })
    expect(refused({ error: "x", reason: "holder_human" }).reason).toBe("holder_human")
    expect(refused({ error: "x", reason: "busy" }).reason).toBe("busy")
    expect(refused({ error: "x", reason: "<script>" })).toEqual({ state: "failed" })
    expect(refused({ error: "x", reason: "constructor" })).toEqual({ state: "failed" })
    expect(refused("plain text")).toEqual({ state: "failed" })
  })
  it("never claims a Sheet it cannot name, and refuses unsafe references", () => {
    expect(done("not json").state).toBe("failed")
    expect(done({ changed_cells: 3 }).state).toBe("failed")
    expect(done({ page_id: "../etc" }).state).toBe("failed")
    expect(done({ slug: "a b" }).state).toBe("failed")
  })
})

let n = 0
const part = (type: TurnPart["type"], metadata: Record<string, unknown>, content = ""): TurnPart => ({ id: `p${++n}`, type, content, metadata, timestamp: new Date() })
const apply = (id: string, body: unknown) => ({
  call: part("tool_call", { tool_id: id, tool_name: "craft_sheet_apply" }, "craft_sheet_apply"),
  result: part("tool_result", { tool_id: id }, JSON.stringify(body)),
})

describe("lifting Sheet cards out of an activity group", () => {
  const cards = (nodes: ReturnType<typeof liftArtifactToolNodes>) => nodes.filter((x) => x.kind === "artifact")
  it("shows one card for repeated changes of the same Sheet in a turn", () => {
    const out = liftArtifactToolNodes([{ kind: "activity", tools: [apply("a", { page_id: "pg_1" }), apply("b", { page_id: "pg_1" })] }])
    expect(cards(out)).toHaveLength(1)
  })
  it("shows one card per Sheet", () => {
    const out = liftArtifactToolNodes([{ kind: "activity", tools: [apply("a", { page_id: "pg_1" }), apply("b", { page_id: "pg_2" })] }])
    expect(cards(out)).toHaveLength(2)
  })
  it("keeps other tools in their group, around the card", () => {
    const bash = { call: part("tool_call", { tool_id: "x", tool_name: "Bash" }, "Bash") }
    const out = liftArtifactToolNodes([{ kind: "activity", tools: [bash, apply("a", { page_id: "pg_1" }), bash] }])
    expect(out.map((x) => x.kind)).toEqual(["activity", "artifact", "activity"])
  })
})
