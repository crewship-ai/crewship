/** Browser environment boundary only; actual SDK transport tests are separate. */
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { EditorSDKOptions } from "../../../lib/mooring/editor-sdk"
const transport = vi.hoisted(() => ({ options: undefined as EditorSDKOptions | undefined }))
vi.mock("../../../lib/mooring/editor-sdk", () => ({ createEditorSDK(options: EditorSDKOptions) {
  transport.options = options
  return { hello() {}, close() {}, draft() {}, command() { return true } }
} }))
const parentOrigin = "https://studio.example.test"
let unsubscribe: (() => void) | undefined
beforeEach(async () => {
  vi.resetModules()
  window.history.replaceState(null, "", `/b/${"a".repeat(64)}/index.html?parent_origin=${encodeURIComponent(parentOrigin)}`)
  document.body.innerHTML = '<textarea></textarea><button>Save</button><output></output><div id="confirmed"></div><div id="revision"></div>'
  await import("./editor")
})
afterEach(() => { unsubscribe?.(); unsubscribe = undefined; window.history.replaceState(null, "", "/") })
it("forwards messages only from the configured parent window and origin", () => {
  const handler = vi.fn()
  unsubscribe = transport.options!.environment.listen(handler)
  window.dispatchEvent(new MessageEvent("message", { source: window.parent, origin: "https://foreign.example.test", data: { type: "mooring:connect", protocol: 1 } }))
  window.dispatchEvent(new MessageEvent("message", { source: {} as Window, origin: parentOrigin, data: { type: "mooring:connect", protocol: 1 } }))
  expect(handler).not.toHaveBeenCalled()
  window.dispatchEvent(new MessageEvent("message", { source: window.parent, origin: parentOrigin, data: { type: "mooring:connect", protocol: 1 } }))
  expect(handler).toHaveBeenCalledOnce()
  expect(handler).toHaveBeenCalledWith(expect.objectContaining({ source: window.parent, origin: parentOrigin }))
})
