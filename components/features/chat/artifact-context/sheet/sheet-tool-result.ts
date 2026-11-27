/**
 * Reads the agent's Sheet change out of a chat tool part. The result only says
 * WHICH Sheet changed (page id or slug); the card loads its name, kind and the
 * reader's right from the server, so nothing in the tool text is ever shown as
 * a claim about the Sheet.
 */

import { parseAgentVersionFields, type AgentVersionEvent } from "@/lib/mooring/native-agent-version"

export interface SheetApplyActivity {
  state: "running" | "done" | "failed"
  slug?: string
  pageId?: string
  /** The version the agent saved and what it needs to show it live (absent when the result carries none). */
  saved?: Omit<AgentVersionEvent, "pageId">
  /** Why the server refused, from its closed list; never the engine's own text. */
  reason?: SheetRefusalReason
  /** The agent's operation, to undo it. */
  operationId?: string
  /** What the server says about undoing this change: true offers Undo, false is explained on the card; absent, nothing. */
  undoable?: boolean
  /** The version the change was made on. */
  baseVersion?: string
  /** What changed, as the server counted it from the operations it ran. */
  summary?: SheetChangeSummary
}
export interface SheetChangeSummary { cellsChanged: number; sheets: string[]; ranges: string[]; truncated: boolean }
const MAX_SUMMARY_ITEMS = 20
const names = (value: unknown): value is string[] => Array.isArray(value) && value.length <= MAX_SUMMARY_ITEMS && value.every(v => typeof v === "string" && v.length > 0 && v.length <= 300)
function summaryOf(value: unknown): SheetChangeSummary | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined
  const raw = value as Record<string, unknown>
  // Empty lists may come as null (Go nil slices).
  const s: Record<string, unknown> = { ...raw, sheets: raw.sheets === null ? [] : raw.sheets, ranges: raw.ranges === null ? [] : raw.ranges }
  if (!Number.isSafeInteger(s.cells_changed) || (s.cells_changed as number) < 0 || !names(s.sheets) || !names(s.ranges) || (s.truncated !== undefined && typeof s.truncated !== "boolean")) return undefined
  return { cellsChanged: s.cells_changed as number, sheets: [...s.sheets], ranges: [...s.ranges], truncated: s.truncated === true }
}
const versionText = (value: unknown): string | undefined => typeof value === "number" && Number.isSafeInteger(value) && value > 0 ? String(value) : typeof value === "string" && /^[1-9][0-9]{0,18}$/.test(value) ? value : undefined

/** The server's closed list of reasons for refusing a Sheet tool call. */
export const SHEET_REFUSAL_TEXT = {
  feature_off: "Letting an agent change Sheets is not turned on in this workspace.",
  agent_policy: "This agent's crew is not allowed to change artifacts in this workspace.",
  not_attached: "No Sheet of yours is attached to this message.",
  holder_human: "Someone is editing this Sheet now.",
  proposal_required: "The agent may only propose changes to this Sheet, not make them.",
  stale: "The Sheet changed meanwhile; the agent has to read it again.",
  too_large: "The request was too large for one change.",
  formula_not_allowed: "Formulas are not allowed in this request.",
  invalid_request: "The agent's request was not valid.",
  engine_failed: "The Sheet engine did not finish the change.",
  busy: "The Sheet engine is busy with other Sheets; nothing was changed. Try again shortly.",
} as const

export type SheetRefusalReason = keyof typeof SHEET_REFUSAL_TEXT

export const isSheetRefusalReason = (value: unknown): value is SheetRefusalReason =>
  typeof value === "string" && Object.hasOwn(SHEET_REFUSAL_TEXT, value)

// Bare name, or the MCP-prefixed form a runtime reports for a tool of an MCP server.
const TOOL = /^(?:mcp__[A-Za-z0-9_.-]*[A-Za-z0-9.-]__)?craft_sheet_apply$/

export function isSheetApplyToolName(name: string | undefined | null): boolean {
  return typeof name === "string" && TOOL.test(name)
}

const object = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === "object" && !Array.isArray(value)
// A page id or slug as the server issues them: no path separators or spaces.
const ref = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9_.@-]{1,128}$/.test(value)

function json(value: string): unknown {
  try { return JSON.parse(value) } catch { return undefined }
}

/** Unwraps `[{type:"text",text}]` and `{content:[…]}` down to the JSON the tool returned. */
function unwrap(raw: string): unknown {
  let current: unknown = json(raw)
  for (let depth = 0; depth < 3; depth++) {
    if (Array.isArray(current) && object(current[0]) && typeof current[0].text === "string") { current = json(current[0].text); continue }
    if (object(current) && Array.isArray(current.content)) { current = current.content; continue }
    if (typeof current === "string") { current = json(current); continue }
    break
  }
  return current
}

/**
 * The Sheet step of one tool call: running without a result, failed on an
 * error result, done only when the result names a Sheet (page_id or slug).
 */
export function sheetApplyActivity(result: { content: string; isError?: boolean } | null): SheetApplyActivity {
  if (!result) return { state: "running" }
  if (result.isError) {
    // An error says why through the server's closed list; anything else is not shown.
    const body = unwrap(result.content)
    return object(body) && isSheetRefusalReason(body.reason) ? { state: "failed", reason: body.reason } : { state: "failed" }
  }
  const body = unwrap(result.content)
  if (!object(body)) return { state: "failed" }
  const slug = ref(body.slug) ? body.slug : undefined
  const pageId = ref(body.page_id) ? body.page_id : undefined
  if (!slug && !pageId) return { state: "failed" }
  const operationId = typeof body.operation_id === "string" && /^[A-Za-z0-9_.:-]{1,128}$/.test(body.operation_id) ? body.operation_id : undefined
  const baseVersion = versionText(body.base_version), summary = summaryOf(body.summary)
  return { state: "done", slug, pageId, saved: parseAgentVersionFields(body), ...(operationId ? { operationId } : {}), ...(operationId && typeof body.undoable === "boolean" ? { undoable: body.undoable } : {}), ...(baseVersion ? { baseVersion } : {}), ...(summary ? { summary } : {}) }
}
