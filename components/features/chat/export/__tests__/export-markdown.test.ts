import { describe, expect, it } from "vitest"
import type { TurnPart } from "@/hooks/use-chat"
import { turnsToMarkdown } from "../export-dialog"

const at = new Date("2026-11-26T10:00:00Z")
const part = (type: TurnPart["type"], content: string, metadata: Record<string, unknown> = {}): TurnPart => ({ id: `${type}-${content}`, type, content, metadata, timestamp: at })

describe("exported conversation", () => {
  it("names a step as a person reads it, never by its MCP name or a raw input object", () => {
    const md = turnsToMarkdown([
      { id: "u", role: "user", parts: [part("text", "Number the loans in sheet L.")], timestamp: at, isStreaming: false },
      { id: "a", role: "assistant", timestamp: at, isStreaming: false, parts: [
        part("tool_call", "mcp__crewship-routines__craft_sheet_read", { tool_name: "mcp__crewship-routines__craft_sheet_read", input: { query: { command: "sheet.read", params: { sheet: "L" } } } }),
        part("tool_call", "Bash", { tool_name: "Bash", input: { command: "ls" } }),
        part("text", "Done."),
      ] },
    ], "Jamie")
    expect(md).toContain("> _Step:_ Read sheet L")
    expect(md).toContain("> _Step:_ Ran a command")
    expect(md).not.toMatch(/mcp__|crewship-routines|\[object Object\]/)
  })
})
