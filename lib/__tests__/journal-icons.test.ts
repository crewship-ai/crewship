import { describe, it, expect } from "vitest"
import { Activity, Play, CheckCircle, XCircle, Terminal, Globe } from "lucide-react"
import {
  iconForEntryType,
  JOURNAL_ENTRY_ICONS,
} from "@/lib/journal-icons"
import { JOURNAL_ENTRY_TYPES } from "@/lib/types/journal"

describe("iconForEntryType", () => {
  it("returns the right icon for run lifecycle types", () => {
    expect(iconForEntryType("run.started")).toBe(Play)
    expect(iconForEntryType("run.completed")).toBe(CheckCircle)
    expect(iconForEntryType("run.failed")).toBe(XCircle)
  })

  it("returns Terminal for exec.command", () => {
    expect(iconForEntryType("exec.command")).toBe(Terminal)
  })

  it("returns Globe for network.egress", () => {
    expect(iconForEntryType("network.egress")).toBe(Globe)
  })

  it("falls back to Activity for unknown types", () => {
    expect(iconForEntryType("future.unknown_event")).toBe(Activity)
    expect(iconForEntryType("")).toBe(Activity)
  })
})

describe("JOURNAL_ENTRY_ICONS coverage", () => {
  // Every type the backend can emit should have a dedicated icon —
  // missing keys still render (Activity fallback) but the user gets a
  // less informative card. This test surfaces the gap.
  it("covers every JOURNAL_ENTRY_TYPES entry", () => {
    const missing = JOURNAL_ENTRY_TYPES.filter(
      (t) => !(t in JOURNAL_ENTRY_ICONS),
    )
    expect(missing).toEqual([])
  })
})
