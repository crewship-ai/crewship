import type { TurnPart } from "@/hooks/use-chat"

/** A tool invocation: the call plus its matching result (result is absent while
 *  the tool is still running). */
export type ToolNode = { call: TurnPart; result?: TurnPart }

/** A render node for an assistant turn. Either a single passthrough part
 *  (text / thinking / status / error / …) or an "activity" group bundling one
 *  or more consecutive tool invocations into one collapsible unit. */
export type RenderNode =
  | { kind: "part"; part: TurnPart }
  | { kind: "activity"; tools: ToolNode[] }

/**
 * Group an ordered list of turn parts for rendering. Consecutive tool calls
 * (each paired with its following tool_result) collapse into a single
 * "activity" node so a run of tools renders as one quiet "Worked · N steps"
 * disclosure instead of a stack of raw cards. Any non-tool part (text,
 * thinking, status, error, image, result, …) ends the current activity run and
 * passes through as its own node, preserving the exact on-wire order — so text
 * interleaved between tools keeps its place and tools never float away from the
 * prose they belong to.
 *
 * Pairing prefers tool_id correlation; it falls back to "the immediately
 * following tool_result" when ids are missing (older adapters / partial data).
 */
/** Tools with bespoke, often interactive UI (a question prompt, a todo
 *  checklist, a delegation card) must stay prominent — never folded into a
 *  collapsed activity group. They render as their own passthrough part. */
const SPECIAL_TOOLS = new Set(["AskUserQuestion", "TodoWrite", "Task"])

function toolNameOf(p: TurnPart): string {
  return (typeof p.metadata?.tool_name === "string" ? (p.metadata.tool_name as string) : "") || p.content
}

/** Merge every thinking pass of a turn into ONE part at the position of the
 *  first pass. The model interleaves thinking ↔ text ↔ tools freely (a dozen
 *  short passes per reply on fast models), and one "Thought for Ns" card per
 *  pass floods the transcript — the user reads reasoning as a single chain.
 *  Passes are joined with a paragraph break; the merged part keeps the first
 *  pass's id (stable React key, no remount) and streams while ANY pass does. */
function mergeThinkingParts(parts: TurnPart[]): TurnPart[] {
  const passes = parts.filter((p) => p.type === "thinking")
  if (passes.length <= 1) return parts
  const merged: TurnPart = {
    ...passes[0],
    content: passes.map((p) => p.content.trim()).filter(Boolean).join("\n\n"),
    isStreaming: passes.some((p) => p.isStreaming),
  }
  const out: TurnPart[] = []
  for (const p of parts) {
    if (p.type !== "thinking") out.push(p)
    else if (p === passes[0]) out.push(merged)
  }
  return out
}

export function groupTurnParts(rawParts: TurnPart[]): RenderNode[] {
  const parts = mergeThinkingParts(rawParts)
  const nodes: RenderNode[] = []
  let pending: ToolNode[] = []

  const flush = () => {
    if (pending.length > 0) {
      nodes.push({ kind: "activity", tools: pending })
      pending = []
    }
  }

  const toolId = (p: TurnPart): string | undefined =>
    typeof p.metadata?.tool_id === "string" ? (p.metadata.tool_id as string) : undefined

  for (let i = 0; i < parts.length; i++) {
    const p = parts[i]

    if (p.type === "tool_call" && SPECIAL_TOOLS.has(toolNameOf(p))) {
      // Special interactive tool — keep it out of the activity group.
      flush()
      nodes.push({ kind: "part", part: p })
      continue
    }

    if (p.type === "tool_call") {
      const node: ToolNode = { call: p }
      const next = parts[i + 1]
      if (next && next.type === "tool_result") {
        const cid = toolId(p)
        const rid = toolId(next)
        // Pair when ids match, or when either id is absent (adjacency fallback).
        if (!cid || !rid || cid === rid) {
          node.result = next
          i++ // consume the result
        }
      }
      pending.push(node)
      continue
    }

    if (p.type === "tool_result") {
      // An orphan result (no preceding call in this turn) — attach as a
      // result-only tool node rather than dropping it.
      pending.push({ call: p })
      continue
    }

    // Any other part type ends the current tool run.
    flush()
    nodes.push({ kind: "part", part: p })
  }
  flush()
  return nodes
}
