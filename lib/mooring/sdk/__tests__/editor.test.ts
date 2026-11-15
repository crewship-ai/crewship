/** Contract of the in-frame editor surface, asserted through the public SDK. */
import { describe, expect, it, vi } from "vitest"
import * as root from "../.."
import * as sdkEditor from "../editor"
import { createEditorSDK, type CapabilityPort, type EditorSaveStatus, type WindowEnvelope } from "../editor"

const receipt = (rev: number) => ({ rev, outcome: "committed", issued_by: "crewship", durability: "node" })
function editor() {
  const parent = {}, revs: number[] = [], statuses: EditorSaveStatus[] = []
  let listener: (event: WindowEnvelope & { ports?: CapabilityPort[] }) => void = () => {}
  const port: CapabilityPort = { onmessage: null, close: vi.fn(), postMessage: vi.fn() }
  const sdk = createEditorSDK({ bundle: "a".repeat(64), parentOrigin: "https://studio.example.test", environment: { parent, hello: vi.fn(), listen(fn) { listener = fn; return vi.fn() } }, onState: s => revs.push(s.rev), onStatus: s => statuses.push(s) })
  listener({ source: parent, origin: "https://studio.example.test", data: { type: "mooring:connect", protocol: 1 }, ports: [port] })
  const receive = (data: object) => port.onmessage?.({ data: JSON.stringify({ protocol: 1, ...data }) })
  return { sdk, revs, statuses, receive }
}

describe("Mooring SDK in-frame editor", () => {
  it("never rolls confirmed state back on an old snapshot or a delayed acknowledgement", () => {
    const e = editor()
    e.receive({ type: "snapshot", rev: 3, generation: 1, snapshot: { text: "three" } })
    e.receive({ type: "committed", rev: 2, generation: 1, snapshot: { text: "two" } })
    expect(e.sdk.state?.rev).toBe(3)
    expect(e.revs).toEqual([3])
  })

  it("keeps a local draft visible when a receipt for an earlier command arrives", () => {
    const e = editor()
    e.receive({ type: "snapshot", rev: 1, generation: 1, snapshot: { text: "" } })
    expect(e.sdk.command("set", { text: "first" })).toBe(true)
    e.sdk.draft(true)
    e.receive({ type: "ack", seq: 1, rev: 2, receipt: receipt(2) })
    e.receive({ type: "committed", rev: 2, generation: 1, snapshot: { text: "first" } })
    expect(e.statuses.at(-1)).toBe("draft")
  })

  it("refuses a push from another lease generation", () => {
    const e = editor()
    e.receive({ type: "snapshot", rev: 1, generation: 1, snapshot: {} })
    e.receive({ type: "committed", rev: 2, generation: 2, snapshot: {} })
    expect(e.statuses.at(-1)).toBe("error")
    expect(e.sdk.state?.generation).toBe(1)
  })

  it("is exactly the root in-frame surface", () => {
    expect(Object.keys(sdkEditor).sort()).toEqual(Object.keys(root).sort())
    for (const name of Object.keys(root)) expect((sdkEditor as Record<string, unknown>)[name], name).toBe((root as Record<string, unknown>)[name])
  })
})
