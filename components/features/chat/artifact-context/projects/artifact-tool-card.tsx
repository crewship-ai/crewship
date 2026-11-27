"use client"
import type { TurnPart } from "@/hooks/use-chat"
import { toolIdOf } from "@/lib/chat-activity"
import type { RenderNode, ToolNode } from "../../turn-grouping"
import { ArtifactChatCard } from "./artifact-chat-card"
import { ArtifactStepCard } from "./artifact-step-card"
import { artifactToolActivity, isArtifactToolName } from "./tool-result"
import { SheetChatCard } from "../sheet/sheet-chat-card"
import { isSheetApplyToolName, sheetApplyActivity } from "../sheet/sheet-tool-result"

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
  if (isSheetApplyToolName(toolNameOf(call))) {
    return <div className="flex flex-col gap-2" data-testid="artifact-tool-card">
      <SheetChatCard activity={sheetApplyActivity(resultOf(call, result))} turnStreaming={turnStreaming} />
    </div>
  }
  const input = call.activity ? call.activity.parameters : call.metadata?.input
  const activity = artifactToolActivity({ toolName: toolNameOf(call), input }, resultOf(call, result))
  if (!activity) return null
  const running = activity.state === "running"
  const source = toolIdOf(call) ?? call.id
  // A create that did not report a version never made a project; a card would only fetch a missing one.
  const projectExists = activity.tool !== "create" || activity.state === "done"
  // While the agent works only the progress card shows; the project card joins once the step settled.
  // A call left without a result on a finished turn shows no stale progress, just the project if known.
  return <div className="flex flex-col gap-2" data-testid="artifact-tool-card">
    {(!running || turnStreaming) && <ArtifactStepCard activity={activity} startedAt={running ? call.timestamp : undefined} source={source} />}
    {activity.slug && projectExists && (!running || !turnStreaming) && <ArtifactChatCard slug={activity.slug} fallbackName={activity.name} activity={activity} source={source} />}
  </div>
}

export type ArtifactRenderNode = RenderNode | { kind: "artifact"; tool: ToolNode }

/** Which cards this chat shows; a kind that is off stays an ordinary step. */
export interface ArtifactCardKinds { projects: boolean; sheets: boolean }

const isArtifactCall = (t: ToolNode, kinds: ArtifactCardKinds) => t.call.type !== "tool_result"
  && ((kinds.projects && isArtifactToolName(toolNameOf(t.call))) || (kinds.sheets && isSheetApplyToolName(toolNameOf(t.call))))

/** The Sheet a finished apply step names, so repeated changes of one Sheet in a turn share one card. */
const sheetKey = (t: ToolNode): string | undefined => {
  if (!isSheetApplyToolName(toolNameOf(t.call))) return undefined
  const a = sheetApplyActivity(resultOf(t.call, t.result))
  return a.state === "done" ? (a.pageId ?? a.slug) : undefined
}

/**
 * Lifts artifact tool calls out of activity groups so the card is never hidden
 * inside a collapsed "Worked · N steps" disclosure. The surrounding run is split
 * at the card, which keeps the on-wire order. A result that the grouping could
 * not pair (orphan) but whose id belongs to a lifted call is dropped: the card
 * already shows it. Nested (subagent) steps stay with their parent.
 */
export function liftArtifactToolNodes(nodes: RenderNode[], kinds: ArtifactCardKinds = { projects: true, sheets: true }): ArtifactRenderNode[] {
  if (!kinds.projects && !kinds.sheets) return nodes
  const lifted = new Set<string>()
  for (const node of nodes) {
    if (node.kind !== "activity") continue
    for (const t of node.tools) {
      const id = isArtifactCall(t, kinds) ? toolIdOf(t.call) : undefined
      if (id) lifted.add(id)
    }
  }
  const out: ArtifactRenderNode[] = []
  const sheets = new Set<string>()
  for (const node of nodes) {
    if (node.kind !== "activity") { out.push(node); continue }
    let run: ToolNode[] = []
    const flush = () => { if (run.length) out.push({ kind: "activity", tools: run }); run = [] }
    for (const t of node.tools) {
      if (isArtifactCall(t, kinds)) {
        const key = sheetKey(t)
        // A second change of the same Sheet in the turn adds no card.
        if (key !== undefined) { if (sheets.has(key)) continue; sheets.add(key) }
        flush(); out.push({ kind: "artifact", tool: t }); continue
      }
      const id = t.call.type === "tool_result" ? toolIdOf(t.call) : undefined
      if (id && lifted.has(id)) continue
      run.push(t)
    }
    flush()
  }
  return out
}
