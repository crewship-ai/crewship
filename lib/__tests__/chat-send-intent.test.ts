import { describe, it, expect, vi } from "vitest"
import { createSendIntent, type AdmissionBody, type AdmissionResult, type MessageAdmissionClient } from "../chat-send-intent"

const body = (text = "hi"): AdmissionBody => ({ conversationId: "c1", text, attachmentPaths: [] })

function client(...results: Array<AdmissionResult | "hang" | "throw">) {
  const keys: string[] = []
  const impl: MessageAdmissionClient = {
    admit: vi.fn(async ({ intentKey }, signal) => {
      keys.push(intentKey)
      const r = results.shift() ?? { status: "accepted", messageId: "m" }
      if (r === "throw") throw new Error("net")
      if (r === "hang") return new Promise<AdmissionResult>((_, rej) => signal.addEventListener("abort", () => rej(new Error("abort"))))
      return r
    }),
  }
  return { impl, keys }
}

describe("durable send intent", () => {
  it("clears the draft only after acceptance", async () => {
    const c = client({ status: "accepted", messageId: "m1" })
    const i = createSendIntent({ client: c.impl, newKey: () => "k1" })
    const out = await i.submit(body())
    expect(out.clearDraft).toBe(true)
    expect(out.state).toEqual({ phase: "accepted", key: "k1", messageId: "m1" })
  })

  it("a timeout is unknown, keeps the draft, and the retry reuses the same key", async () => {
    const c = client("hang", { status: "accepted", messageId: "m1" })
    let n = 0
    const i = createSendIntent({ client: c.impl, timeoutMs: 5, newKey: () => `k${++n}` })
    const first = await i.submit(body())
    expect(first.state.phase).toBe("unknown")
    expect(first.clearDraft).toBe(false)
    const retry = await i.submit(body())
    expect(retry.clearDraft).toBe(true)
    expect(c.keys).toEqual(["k1", "k1"])
  })

  it("a changed body after an unknown outcome is a conflict, not a silent new message", async () => {
    const c = client("throw")
    const i = createSendIntent({ client: c.impl, newKey: () => "k1" })
    await i.submit(body("one"))
    const edited = await i.submit(body("two"))
    expect(edited.state).toEqual({ phase: "conflict", key: "k1" })
    expect(edited.clearDraft).toBe(false)
    expect(c.impl.admit).toHaveBeenCalledTimes(1)
    i.startNew()
    expect((await i.submit(body("two"))).clearDraft).toBe(true)
  })

  it("a server conflict keeps the draft; a definitive rejection frees the key", async () => {
    const c = client({ status: "conflict" }, { status: "rejected", reason: "revoked" }, { status: "accepted", messageId: "m" })
    let n = 0
    const i = createSendIntent({ client: c.impl, newKey: () => `k${++n}` })
    expect((await i.submit(body())).state.phase).toBe("conflict")
    expect((await i.submit(body())).state).toEqual({ phase: "rejected", reason: "revoked" })
    await i.submit(body())
    expect(c.keys).toEqual(["k1", "k1", "k2"])
  })

  it("ignores a second submit while one is in flight", async () => {
    const c = client("hang")
    const i = createSendIntent({ client: c.impl, timeoutMs: 20 })
    const p = i.submit(body())
    const second = await i.submit(body())
    expect(second.state.phase).toBe("sending")
    expect(c.impl.admit).toHaveBeenCalledTimes(1)
    await p
  })
})
