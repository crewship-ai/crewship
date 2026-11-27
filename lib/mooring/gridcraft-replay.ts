import { NATIVE_EXECUTE_COMMANDS } from "./native-editor-port"
import type { ReplayAdapter } from "./native-rebase"

// Every cell or range names its sheet ('L'!B8 or L!B8): without it the editor would write into the sheet the person has open.
const QUALIFIED = /^(?:'(?:[^']|'')+'|[^'!\s][^!]*)!\$?[A-Z]{1,3}\$?[1-9][0-9]*(?::\$?[A-Z]{1,3}\$?[1-9][0-9]*)?$/

/** A cell or range that names its sheet, as the Sheet's adapter writes addresses. */
export const sheetAddress = (value: unknown): value is string => typeof value === "string" && value.length <= 300 && QUALIFIED.test(value)

/**
 * The cells a person's save changed since the saved version: addresses, or "unknown". Whatever cannot be
 * read as such (too many, malformed) is unknown, never nothing; an editor that does not say gives undefined.
 */
export type SheetTouched = string[] | "unknown"
export const SHEET_TOUCHED_MAX = 2000
export function sheetTouched(value: unknown): SheetTouched {
  if (!Array.isArray(value) || value.length > SHEET_TOUCHED_MAX || !value.every(sheetAddress)) return "unknown"
  return new TextEncoder().encode(JSON.stringify(value)).length > 64 * 1024 ? "unknown" : [...value]
}

/** The Sheet's adapter for the replay over unsaved work: its four replayable commands, each address naming its sheet. */
export const GRIDCRAFT_REPLAY: ReplayAdapter = {
  replayable(op) {
    if (!(NATIVE_EXECUTE_COMMANDS as readonly string[]).includes(op.command)) return false
    if (op.command === "formulas.calculateNow") return true
    const p = op.params ?? {}
    const address = op.command === "cell.set" ? p.cell : p.range
    return sheetAddress(address)
  },
}
