import { describe, expect, it } from "vitest"
import { statusInfo } from "../../../frame"
import { docsFrameStatus, outlineOf, readingStats, readingText, type DocsSyncFacts } from "../docs-model"

const facts = (over: Partial<DocsSyncFacts> = {}): DocsSyncFacts => ({
  hydrated: true, denied: false, available: true, restoring: false, status: "Saved", pending: 0, recovery: false,
  recoveredDrafts: 0, canWrite: true, durability: "confirmed", sentThisSession: false, ...over,
})

describe("docsFrameStatus", () => {
  it.each<[string, Partial<DocsSyncFacts>, string | null, string?]>([
    ["nothing confirmed yet", { hydrated: false, status: "Loading document…" }, null],
    ["opened, nothing sent", {}, "live"],
    ["a write in flight", { pending: 1, status: "Saving…", durability: "local" }, "saving"],
    // The receipt arrived and the session re-read the head: only now Saved.
    ["the receipt for this tab's write", { sentThisSession: true }, "saved"],
    ["Saved words while a write is still queued stay Saving", { pending: 2, status: "Saved", durability: "local", sentThisSession: true }, "saving"],
    ["no connection", { status: "Disconnected · changes are unsaved", pending: 3 }, "offline"],
    ["rate limited", { status: "Temporarily rate limited · changes are unsaved", pending: 1 }, "offline"],
    ["the service withdrew", { available: false, pending: 1 }, "offline"],
    ["a receipt that does not verify", { status: "Save confirmation unavailable · changes are unsaved", recovery: true, pending: 1 }, "unconfirmed"],
    ["a recovered draft", { recovery: true, recoveredDrafts: 1, status: "Unsaved document draft found · recover before reopening" }, "draft", "Recovered draft"],
    ["an unsaved draft after a rejection", { recovery: true, pending: 1, status: "Document update was rejected. Recover your draft before reopening." }, "draft", "Unsaved draft"],
    ["read access only", { canWrite: false, status: "Read only" }, "readonly"],
    ["a restore in flight", { restoring: true }, "saving", "Restoring…"],
    ["access lost", { denied: true, hydrated: false, status: "Document access is unavailable." }, "error", "No access"],
    ["words the chip does not know", { status: "Update confirmed · private storage cleanup failed" }, "error", "Needs attention"],
  ])("%s", (_name, over, key, label) => {
    const status = docsFrameStatus(facts(over))
    expect(status?.key ?? null).toBe(key)
    if (label) expect(status && statusInfo(status).label).toBe(label)
  })

  it("never says Saved before a receipt, whatever the words", () => {
    for (const pending of [1, 3]) expect(docsFrameStatus(facts({ pending, status: "Saved", sentThisSession: true }))?.key).toBe("saving")
    expect(docsFrameStatus(facts({ status: "Saved", sentThisSession: false }))?.key).toBe("live")
  })

  it("says an unjournalled write exists only in this tab", () => {
    expect(docsFrameStatus(facts({ pending: 1, durability: "unsaved", status: "Saving…" }))?.detail).toMatch(/only in this tab/)
  })
})

const heading = (level: number, text: string, id: string | null = `h-${text}`) => ({ type: "heading", attrs: { level, id }, content: text ? [{ type: "text", text }] : [] })
const paragraph = (text: string) => ({ type: "paragraph", attrs: { id: null }, content: [{ type: "text", text }] })

describe("outline and counts", () => {
  const doc = { type: "doc", content: [heading(1, "Goal"), paragraph("Ship the agency offer on 2 December."), heading(2, "Timeline"), heading(2, ""), heading(3, "Risks", null), paragraph("Legal check")] }

  it("lists the headings in order, without empty ones", () => {
    expect(outlineOf(doc)).toEqual([
      { id: "h-Goal", level: 1, text: "Goal" },
      { id: "h-Timeline", level: 2, text: "Timeline" },
      { id: null, level: 3, text: "Risks" },
    ])
    expect(outlineOf(undefined)).toEqual([])
  })

  it("counts words across blocks and rounds the read time", () => {
    expect(readingStats(doc)).toEqual({ words: 12, minutes: 1 })
    expect(readingText(readingStats(doc))).toBe("12 words · 1 min read")
    expect(readingText({ words: 640, minutes: 3 })).toBe("640 words · 3 min read")
    expect(readingText(readingStats({ type: "doc", content: [] }))).toBe("No words yet")
  })
})
