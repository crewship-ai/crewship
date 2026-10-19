import { describe, it, expect } from "vitest"
import {
  createUploadQueue,
  filesFromClipboard,
  validateAttachmentBatch,
  type AttachmentLimits,
} from "../chat-attachment-queue"

const file = (name: string, size: number) => new File([new Uint8Array(size)], name)
const limits: AttachmentLimits = { maxCount: 3, maxBytes: 100 }

describe("validateAttachmentBatch", () => {
  it("accepts in order and refuses by reason", () => {
    const r = validateAttachmentBatch(1, [file("a", 10), file("big", 101), file("e", 0), file("b", 5), file("c", 5)], limits)
    expect(r.accepted.map((f) => f.name)).toEqual(["a", "b"])
    expect(r.rejected.map((x) => [x.file.name, x.reason])).toEqual([
      ["big", "too_large"],
      ["e", "empty"],
      ["c", "too_many"],
    ])
  })
})

describe("filesFromClipboard", () => {
  it("returns nothing for a text paste", () => {
    expect(filesFromClipboard({ files: [] as unknown as FileList, items: [] as unknown as DataTransferItemList })).toEqual([])
    expect(filesFromClipboard(null)).toEqual([])
  })
  it("reads files, falling back to items", () => {
    const f = file("shot.png", 3)
    expect(filesFromClipboard({ files: [f] as unknown as FileList, items: [] as unknown as DataTransferItemList })).toEqual([f])
    const items = [{ kind: "file", getAsFile: () => f }, { kind: "string", getAsFile: () => null }]
    expect(filesFromClipboard({ files: [] as unknown as FileList, items: items as unknown as DataTransferItemList })).toEqual([f])
  })
})

describe("createUploadQueue", () => {
  it("runs one task at a time in order and survives a failing task", async () => {
    const q = createUploadQueue()
    const log: string[] = []
    let active = 0
    let maxActive = 0
    const task = (name: string, fail = false) => async () => {
      active++
      maxActive = Math.max(maxActive, active)
      await new Promise((r) => setTimeout(r, 5))
      log.push(name)
      active--
      if (fail) throw new Error("x")
    }
    await Promise.all([q.enqueue("a", task("a", true)), q.enqueue("b", task("b")), q.enqueue("c", task("c"))])
    expect(log).toEqual(["a", "b", "c"])
    expect(maxActive).toBe(1)
  })
  it("skips a task cancelled before it started", async () => {
    const q = createUploadQueue()
    const log: string[] = []
    const p1 = q.enqueue("a", async () => { await new Promise((r) => setTimeout(r, 5)); log.push("a") })
    const p2 = q.enqueue("b", async () => { log.push("b") })
    q.cancel("b")
    await Promise.all([p1, p2])
    expect(log).toEqual(["a"])
  })
})
