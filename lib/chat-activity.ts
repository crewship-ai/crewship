import type { TurnPart } from "@/hooks/use-chat"

/** What a tool step is doing, as the user should read it. `unknown` is the
 *  honest answer for a finished turn with no recorded outcome; it is never
 *  rendered as success. */
export type ActivityState =
  | "running"
  | "awaiting_approval"
  | "succeeded"
  | "failed"
  | "denied"
  | "interrupted"
  | "unknown"

/** User-facing label per state. The frontend owns this text, the server only
 *  sends the state token. */
export const ACTIVITY_STATE_LABEL: Record<ActivityState, string> = {
  running: "Running",
  awaiting_approval: "Awaiting approval",
  succeeded: "Completed",
  failed: "Failed",
  denied: "Denied",
  interrupted: "Interrupted",
  unknown: "Outcome unknown",
}

/** A tool step: the call (or typed activity part) plus its paired result and
 *  any correlated children. Structurally the same as turn-grouping's ToolNode. */
export interface ToolStep {
  call: TurnPart
  result?: TurnPart
  children?: ToolStep[]
}

/** Correlation id of a part: the typed `tool_call_id`, else `metadata.tool_id`,
 *  else `metadata.tool_use_id` (what legacy live results carry). */
export function toolIdOf(p: TurnPart): string | undefined {
  if (p.tool_call_id) return p.tool_call_id
  const m = p.metadata
  if (typeof m?.tool_id === "string" && m.tool_id) return m.tool_id
  if (typeof m?.tool_use_id === "string" && m.tool_use_id) return m.tool_use_id
  return undefined
}

/** Parent correlation id: the typed `parent_tool_call_id`, else the legacy
 *  `metadata.parent_tool_use_id` / `parent_tool_call_id`. */
export function parentToolIdOf(p: TurnPart): string | undefined {
  if (p.parent_tool_call_id) return p.parent_tool_call_id
  const m = p.metadata
  if (typeof m?.parent_tool_use_id === "string" && m.parent_tool_use_id) return m.parent_tool_use_id
  if (typeof m?.parent_tool_call_id === "string" && m.parent_tool_call_id) return m.parent_tool_call_id
  return undefined
}

/** Resolve the state of one tool step. Order: the typed activity state, then
 *  the paired result (is_error / denied), then whether the turn is still live. */
export function toolState(step: Pick<ToolStep, "call" | "result">, turnStreaming: boolean): ActivityState {
  const typed = step.call.activity?.state
  if (typed) return typed === "input_streaming" ? "running" : typed
  // A result with no call in this turn classifies itself.
  if (step.call.type === "tool_result" && !step.result) {
    const m = step.call.metadata
    if (m?.denied === true) return "denied"
    return m?.is_error === true ? "failed" : "succeeded"
  }
  if (step.result) {
    const r = step.result.metadata
    if (r?.denied === true) return "denied"
    if (r?.is_error === true) return "failed"
    return "succeeded"
  }
  return turnStreaming ? "running" : "unknown"
}

/** True for a call that is waiting on a human and must stay visible outside
 *  any collapsed group. */
export function needsAttention(part: TurnPart): boolean {
  return part.activity?.state === "awaiting_approval"
}

export function flattenSteps(steps: ToolStep[]): ToolStep[] {
  return steps.flatMap((s) => [s, ...flattenSteps(s.children ?? [])])
}

/** `neutral`: finished with steps that have no recorded result. That can be a
 *  turn rebuilt mid-run, so it is neither success nor an alarm. */
export type GroupTone = "running" | "ok" | "attention" | "neutral"

export interface GroupSummary {
  tone: GroupTone
  /** Headline word: Working / Worked / Needs approval / Activity incomplete. */
  title: string
  /** Counts of non-success outcomes, e.g. "1 failed, 1 denied". */
  detail: string
}

/** Summarise a group. Success is only claimed when EVERY step succeeded; one
 *  failed, denied, interrupted or unknown step makes the whole group read as
 *  needing attention. */
export function summarizeGroup(states: ActivityState[]): GroupSummary {
  const count = (s: ActivityState) => states.filter((x) => x === s).length
  const parts = (["failed", "denied", "interrupted", "unknown"] as const)
    .map((s) => [s, count(s)] as const)
    .filter(([, n]) => n > 0)
    .map(([s, n]) => `${n} ${s === "unknown" ? "unknown" : s}`)
  const detail = parts.join(", ")
  const hard = count("failed") + count("denied") + count("interrupted")
  if (count("awaiting_approval") > 0) return { tone: "attention", title: "Needs approval", detail }
  if (hard === 0 && count("unknown") > 0 && count("running") === 0) {
    return { tone: "neutral", title: "Finished", detail: `${count("unknown")} without a recorded result` }
  }
  if (parts.length > 0) {
    const settled = count("running") === 0
    return { tone: "attention", title: settled ? "Activity incomplete" : "Working", detail }
  }
  if (count("running") > 0) return { tone: "running", title: "Working", detail }
  return { tone: "ok", title: "Worked", detail }
}
