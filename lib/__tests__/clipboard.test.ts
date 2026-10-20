import { afterEach, expect, it, vi } from "vitest"
import { copyToClipboard } from "../clipboard"

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); delete (document as unknown as Record<string, unknown>).execCommand })
const exec = (result: boolean | "throws") => {
  const fn = vi.fn(() => { if (result === "throws") throw new Error("blocked"); return result })
  Object.defineProperty(document, "execCommand", { configurable: true, value: fn })
  return fn
}

it("answers true only after the clipboard accepted the text", async () => {
  vi.stubGlobal("isSecureContext", true)
  let accept!: () => void
  vi.spyOn(navigator.clipboard, "writeText").mockReturnValue(new Promise<void>(resolve => { accept = resolve }))
  let settled: boolean | undefined
  const copying = copyToClipboard("hello").then(ok => { settled = ok })
  await Promise.resolve()
  expect(settled).toBeUndefined()
  accept(); await copying
  expect(settled).toBe(true)
})
it("falls back to the legacy copy when the clipboard refuses, and reports its real result", async () => {
  vi.stubGlobal("isSecureContext", true)
  vi.spyOn(navigator.clipboard, "writeText").mockRejectedValue(new Error("denied"))
  exec(true)
  expect(await copyToClipboard("hello")).toBe(true)
  exec(false)
  expect(await copyToClipboard("hello")).toBe(false)
  exec("throws")
  expect(await copyToClipboard("hello")).toBe(false)
  expect(document.querySelector("textarea")).toBeNull()
})
it("uses the legacy copy outside a secure context", async () => {
  vi.stubGlobal("isSecureContext", false)
  const write = vi.spyOn(navigator.clipboard, "writeText")
  const fn = exec(true)
  expect(await copyToClipboard("hello")).toBe(true)
  expect(fn).toHaveBeenCalledWith("copy")
  expect(write).not.toHaveBeenCalled()
})
