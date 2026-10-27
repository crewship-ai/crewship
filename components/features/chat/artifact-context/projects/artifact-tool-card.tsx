"use client"
import type { TurnPart } from "@/hooks/use-chat"
import { toolIdOf } from "@/lib/chat-activity"
import type { RenderNode, ToolNode } from "../../turn-grouping"
import { ArtifactChatCard } from "./artifact-chat-card"
import { ArtifactStepCard } from "./artifact-step-card"
import { artifactToolActivity, isArtifactToolName } from "./tool-result"

const toolNameOf = (p: TurnPart): string =>
  p.activity?.name || (typeof p.metadata?.tool_name === "string" ? p.metadata.tool_name : "") || p.content

function resultOf(call: TurnPart, result?: TurnPart): { content: string; isError?: boolean } | null {
  if (result) return { content: result.content ?? "", isError: result.metadata?.is_error === true }
  // Typed activity parts carry their own outcome instead of a separate result part.
  const a = call.activity
  if (a && (a.state === "succeeded" || a.state === "failed") && a.output !== undefined) return { content: a.output, isError: a.state === "failed" }
  return null
}

interface ArtifactToolCardProps {
  call: TurnPart
  result?: TurnPart
  /** A call with no result on a turn that stopped streaming is not running any more; show no stale spinner. */
  turnStreaming?: boolean
}

/** The agent's artifact step on top, the project card below once the slug is known. */
export function ArtifactToolCard({ call, result, turnStreaming = true }: ArtifactToolCardProps) {
  const input = call.activity ? call.activity.parameters : call.metadata?.input
  const activity = artifactToolActivity({ toolName: toolNameOf(call), input }, resultOf(call, result))
  if (!activity) return null
  const running = activity.state === "running"
  const source = toolIdOf(call) ?? call.id
  // While the agent works only the progress card shows; the project card joins once the step settled.
  // A call left without a result on a finished turn shows no stale progress, just the project if known.
  return <div className="flex flex-col gap-2" data-testid="artifact-tool-card">
    {(!running || turnStreaming) && <ArtifactStepCard activity={activity} startedAt={running ? call.timestamp : undefined} source={source} />}
    {activity.slug && (!running || !turnStreaming) && <ArtifactChatCard slug={activity.slug} fallbackName={activity.name} activity={activity} source={source} />}
  </div>
}

export type ArtifactRenderNode = RenderNode | { kind: "artifact"; tool: ToolNode }

const isArtifactCall = (t: ToolNode) => t.call.type !== "tool_result" && isArtifactToolName(toolNameOf(t.call))

/**
 * Lifts artifact tool calls out of activity groups so the card is never hidden
 * inside a collapsed "Worked · N steps" disclosure. The surrounding run is split
 * at the card, which keeps the on-wire order. A result that the grouping could
 * not pair (orphan) but whose id belongs to a lifted call is dropped: the card
 * already shows it. Nested (subagent) steps stay with their parent.
 */
export function liftArtifactToolNodes(nodes: RenderNode[]): ArtifactRenderNode[] {
  const lifted = new Set<string>()
  for (const node of nodes) {
    if (node.kind !== "activity") continue
    for (const t of node.tools) {
      const id = isArtifactCall(t) ? toolIdOf(t.call) : undefined
      if (id) lifted.add(id)
    }
  }
  const out: ArtifactRenderNode[] = []
  for (const node of nodes) {
    if (node.kind !== "activity") { out.push(node); continue }
    let run: ToolNode[] = []
    const flush = () => { if (run.length) out.push({ kind: "activity", tools: run }); run = [] }
    for (const t of node.tools) {
      if (isArtifactCall(t)) { flush(); out.push({ kind: "artifact", tool: t }); continue }
      const id = t.call.type === "tool_result" ? toolIdOf(t.call) : undefined
      if (id && lifted.has(id)) continue
      run.push(t)
    }
    flush()
  }
  return out
}
