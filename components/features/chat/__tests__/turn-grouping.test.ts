import { describe, it, expect } from "vitest"
import { groupTurnParts } from "../turn-grouping"
import type { TurnPart } from "@/hooks/use-chat"

function part(p: Partial<TurnPart> & { type: TurnPart["type"] }): TurnPart {
  return { id: p.id ?? Math.random().toString(36).slice(2), content: "", timestamp: new Date(), ...p }
}

describe("groupTurnParts", () => {
  it("pairs a tool_call with its following tool_result into one tool node", () => {
    const nodes = groupTurnParts([
      part({ type: "tool_call", content: "Bash", metadata: { tool_id: "t1", tool_name: "Bash" } }),
      part({ type: "tool_result", content: "ok", metadata: { tool_id: "t1" } }),
    ])
    expect(nodes).toHaveLength(1)
    expect(nodes[0].kind).toBe("activity")
    if (nodes[0].kind === "activity") {
      expect(nodes[0].tools).toHaveLength(1)
      expect(nodes[0].tools[0].call.content).toBe("Bash")
      expect(nodes[0].tools[0].result?.content).toBe("ok")
    }
  })

  it("groups consecutive tools into one activity node", () => {
    const nodes = groupTurnParts([
      part({ type: "tool_call", content: "Bash", metadata: { tool_id: "a" } }),
      part({ type: "tool_result", content: "1", metadata: { tool_id: "a" } }),
      part({ type: "tool_call", content: "Bash", metadata: { tool_id: "b" } }),
      part({ type: "tool_result", content: "2", metadata: { tool_id: "b" } }),
      part({ type: "tool_call", content: "Bash", metadata: { tool_id: "c" } }),
      part({ type: "tool_result", content: "3", metadata: { tool_id: "c" } }),
    ])
    expect(nodes).toHaveLength(1)
    expect(nodes[0].kind).toBe("activity")
    if (nodes[0].kind === "activity") expect(nodes[0].tools).toHaveLength(3)
  })

  it("keeps text/thinking as their own nodes and breaks tool grouping", () => {
    const nodes = groupTurnParts([
      part({ type: "thinking", content: "hmm" }),
      part({ type: "text", content: "intro" }),
      part({ type: "tool_call", content: "Bash", metadata: { tool_id: "a" } }),
      part({ type: "tool_result", content: "1", metadata: { tool_id: "a" } }),
      part({ type: "text", content: "between" }),
      part({ type: "tool_call", content: "Bash", metadata: { tool_id: "b" } }),
      part({ type: "tool_result", content: "2", metadata: { tool_id: "b" } }),
      part({ type: "text", content: "outro" }),
    ])
    // thinking, text, activity[a], text, activity[b], text
    expect(nodes.map((n) => n.kind)).toEqual([
      "part", "part", "activity", "part", "activity", "part",
    ])
    const acts = nodes.filter((n) => n.kind === "activity")
    expect(acts).toHaveLength(2)
  })

  it("handles a tool_call with no result yet (streaming)", () => {
    const nodes = groupTurnParts([
      part({ type: "tool_call", content: "Bash", metadata: { tool_id: "a" } }),
    ])
    expect(nodes).toHaveLength(1)
    if (nodes[0].kind === "activity") {
      expect(nodes[0].tools[0].call.content).toBe("Bash")
      expect(nodes[0].tools[0].result).toBeUndefined()
    }
  })

  it("passes through a plain text-only turn unchanged", () => {
    const nodes = groupTurnParts([part({ type: "text", content: "hi" })])
    expect(nodes).toHaveLength(1)
    expect(nodes[0].kind).toBe("part")
  })

  it("never groups special interactive tools (AskUserQuestion/TodoWrite/Task)", () => {
    const nodes = groupTurnParts([
      part({ type: "tool_call", content: "Bash", metadata: { tool_id: "a", tool_name: "Bash" } }),
      part({ type: "tool_result", content: "1", metadata: { tool_id: "a" } }),
      part({ type: "tool_call", content: "AskUserQuestion", metadata: { tool_id: "q", tool_name: "AskUserQuestion" } }),
      part({ type: "tool_call", content: "Bash", metadata: { tool_id: "b", tool_name: "Bash" } }),
      part({ type: "tool_result", content: "2", metadata: { tool_id: "b" } }),
    ])
    // activity[Bash a], part(AskUserQuestion), activity[Bash b]
    expect(nodes.map((n) => n.kind)).toEqual(["activity", "part", "activity"])
    if (nodes[1].kind === "part") {
      expect(nodes[1].part.metadata?.tool_name).toBe("AskUserQuestion")
    }
  })
})

// The model interleaves thinking ↔ text ↔ tools freely (Haiku produces a dozen
// short passes in one reply). Rendering each pass as its own "Thought for Ns"
// card floods the transcript — ALL thinking of a turn must merge into ONE
// chain-of-thought block at the position of the first pass, passes separated
// by a paragraph break. Everything else keeps its on-wire order.
describe("groupTurnParts thinking merge", () => {
  it("merges interleaved thinking passes into a single node at the first position", () => {
    const nodes = groupTurnParts([
      part({ type: "thinking", content: "pass one" }),
      part({ type: "text", content: "some prose" }),
      part({ type: "thinking", content: "pass two" }),
      part({ type: "text", content: "more prose" }),
      part({ type: "thinking", content: "pass three" }),
    ])
    const thinkingNodes = nodes.filter((n) => n.kind === "part" && n.part.type === "thinking")
    expect(thinkingNodes).toHaveLength(1)
    expect(nodes[0].kind).toBe("part")
    if (nodes[0].kind === "part") {
      expect(nodes[0].part.type).toBe("thinking")
      expect(nodes[0].part.content).toBe("pass one\n\npass two\n\npass three")
    }
    // prose keeps its order after the merged block
    const texts = nodes.filter((n) => n.kind === "part" && n.part.type === "text")
    expect(texts).toHaveLength(2)
  })

  it("merged block streams while any pass is still streaming", () => {
    const nodes = groupTurnParts([
      part({ type: "thinking", content: "done pass" }),
      part({ type: "text", content: "prose" }),
      part({ type: "thinking", content: "live pass", isStreaming: true }),
    ])
    const thinking = nodes.find((n) => n.kind === "part" && n.part.type === "thinking")
    expect(thinking && thinking.kind === "part" ? thinking.part.isStreaming : false).toBe(true)
  })

  it("keeps a stable id (the first pass's id) so the React key doesn't remount", () => {
    const nodes = groupTurnParts([
      part({ id: "th-1", type: "thinking", content: "a" }),
      part({ type: "text", content: "t" }),
      part({ id: "th-2", type: "thinking", content: "b" }),
    ])
    const thinking = nodes.find((n) => n.kind === "part" && n.part.type === "thinking")
    expect(thinking && thinking.kind === "part" ? thinking.part.id : "").toBe("th-1")
  })

  it("leaves a single thinking pass untouched", () => {
    const nodes = groupTurnParts([
      part({ id: "only", type: "thinking", content: "solo" }),
      part({ type: "text", content: "t" }),
    ])
    const thinking = nodes.find((n) => n.kind === "part" && n.part.type === "thinking")
    expect(thinking && thinking.kind === "part" ? thinking.part.content : "").toBe("solo")
  })
})
