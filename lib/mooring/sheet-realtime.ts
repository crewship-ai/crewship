import type { NativeSaveReceipt, NativeSaveRequest } from "./single-editor-save"

import { validNativeVersion } from "./native-draft"

export const SHEET_REALTIME_CAPABILITIES = ["merge-capture-v1", "agent-wins-v1", "presence-v1"] as const
export type SheetRealtimeCapability = typeof SHEET_REALTIME_CAPABILITIES[number]
export type SheetRange = { sheet: string; a1: string }
export type SheetPresence = { actorId: string; label: string; activityId: string; ranges: SheetRange[]; ttlMs: number }

export const sheetRecord = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === "object" && !Array.isArray(value)
const identifier = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9_.:-]{1,128}$/.test(value)
const label = (value: unknown, limit: number): value is string => typeof value === "string" && value.length > 0 && value.length <= limit && !/[\x00-\x1f\x7f]/.test(value)
const range = /^\$?[A-Z]{1,3}\$?[1-9][0-9]{0,6}(?::\$?[A-Z]{1,3}\$?[1-9][0-9]{0,6})?$/
const hash = /^sha256:[a-f0-9]{64}$/
const MAX_WORKBOOK_BYTES = 64 * 1024 * 1024

export function readSheetRealtimeAuthority(raw: unknown, generation: string | undefined): { canEdit: boolean; generation?: string } | undefined {
  if (!sheetRecord(raw) || raw.realtime_sheet !== true || typeof raw.can_edit !== "boolean" || typeof raw.can_write !== "boolean" || raw.holder !== null) return
  const writable = generation && raw.can_edit && raw.can_write && raw.generation === generation
  return { canEdit: raw.can_edit, ...(writable ? { generation } : {}) }
}

export function sheetRanges(value: unknown, limit = 256): SheetRange[] | undefined {
  if (!Array.isArray(value) || value.length > limit) return
  const ranges: SheetRange[] = []
  for (const item of value) {
    if (!sheetRecord(item) || !label(item.sheet, 255) || typeof item.a1 !== "string" || !range.test(item.a1)) return
    ranges.push({ sheet: item.sheet, a1: item.a1 })
  }
  return ranges
}

export function readSheetPresence(value: unknown): { phase: "started" | "updated" | "ended"; presence: SheetPresence } | undefined {
  if (!sheetRecord(value) || !identifier(value.op_id) || !identifier(value.run_id) || !sheetRecord(value.agent)
    || !identifier(value.agent.id) || !label(value.agent.name, 128)) return
  const { phase, ttl_ms: ttl } = value
  if (phase !== "started" && phase !== "updated" && phase !== "ended") return
  const ranges = sheetRanges(value.ranges, 32)
  if (!ranges || typeof ttl !== "number" || !Number.isInteger(ttl) || ttl < 0 || ttl > 120_000) return
  return { phase, presence: { actorId: value.agent.id, label: value.agent.name, activityId: value.op_id, ranges, ttlMs: ttl } }
}

export type MergedSheetSave = { receipt: NativeSaveReceipt; contentHash: string; bytes: Uint8Array; agentRanges: SheetRange[] }

export async function readMergedSheetSave(raw: unknown, submitted: NativeSaveRequest): Promise<MergedSheetSave | undefined> {
  if (submitted.applicationId !== "gridcraft" || !sheetRecord(raw) || raw.kind !== "merged" || !sheetRecord(raw.submitted) || !sheetRecord(raw.receipt)) return
  const origin = raw.submitted, receipt = raw.receipt, contentHash = raw.head_content_hash
  if (!submitted.attemptId || origin.attempt_id !== submitted.attemptId || origin.base_version !== submitted.baseVersion
    || origin.editor_revision !== submitted.editorRevision || origin.content_hash !== submitted.contentHash) return
  const unchanged = receipt.unchanged === true
  if ("unchanged" in receipt && !unchanged) return
  if (typeof contentHash !== "string" || !hash.test(contentHash) || receipt.contentHash !== contentHash || receipt.candidateHash !== contentHash
    || !validNativeVersion(receipt.baseVersion) || !validNativeVersion(receipt.version) || BigInt(receipt.baseVersion) < BigInt(submitted.baseVersion)
    || BigInt(receipt.version) !== BigInt(receipt.baseVersion) + (unchanged ? 0n : 1n)
    || unchanged && BigInt(receipt.baseVersion) <= BigInt(submitted.baseVersion)
    || receipt.workspaceId !== submitted.workspaceId || receipt.pageId !== submitted.pageId || receipt.applicationId !== submitted.applicationId
    || receipt.enginePin !== submitted.enginePin || receipt.sessionId !== submitted.sessionId || receipt.generation !== submitted.generation
    || receipt.editorRevision !== submitted.editorRevision || receipt.issuedBy !== "crewship" || receipt.durability !== "node" || receipt.phase !== "committed") return
  const agentRanges = sheetRanges(raw.agent_ranges)
  if (!agentRanges || !sheetRanges(raw.overlap) || typeof raw.bytes !== "string" || raw.bytes.length > Math.ceil(MAX_WORKBOOK_BYTES / 3) * 4) return
  try {
    const decoded = atob(raw.bytes)
    if (!decoded.length || decoded.length > MAX_WORKBOOK_BYTES) return
    const bytes = Uint8Array.from(decoded, c => c.charCodeAt(0))
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes))
    const actual = "sha256:" + Array.from(digest, b => b.toString(16).padStart(2, "0")).join("")
    if (actual !== contentHash) return
    return { receipt: receipt as unknown as NativeSaveReceipt, contentHash, bytes, agentRanges }
  } catch { return }
}
