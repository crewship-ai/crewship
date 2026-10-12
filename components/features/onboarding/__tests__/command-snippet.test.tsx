import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { CommandSnippet, copyText } from "../command-snippet"

afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers() })
it("copies the displayed command and restores its affordance after feedback", async () => {
  vi.useFakeTimers(); vi.stubGlobal("isSecureContext", true)
  const write = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined)
  render(<CommandSnippet command="crewship login" caption="Sign in" />)
  expect(screen.getByText("Sign in")).toBeVisible()
  await act(async () => { fireEvent.click(screen.getByRole("button")) })
  expect(write).toHaveBeenCalledWith("crewship login")
  await act(async () => { await vi.advanceTimersByTimeAsync(1500) })
  expect(screen.getByRole("button")).toBeEnabled()
})
it.each([true, false, "throws"])("falls back to legacy copy in insecure contexts (%s)", (outcome) => {
  vi.stubGlobal("isSecureContext", false)
  const exec = vi.fn(() => { if (outcome === "throws") throw new Error("blocked"); return outcome })
  Object.defineProperty(document, "execCommand", { configurable: true, value: exec })
  const success = vi.fn(); copyText("crewship setup", success)
  expect(exec).toHaveBeenCalledWith("copy")
  expect(success).toHaveBeenCalledTimes(outcome === true ? 1 : 0)
  expect(document.querySelector("textarea")).toBeNull()
  delete (document as unknown as Record<string, unknown>).execCommand
})
it("uses legacy copy when modern clipboard permission is denied", async () => {
  vi.stubGlobal("isSecureContext", true)
  vi.spyOn(navigator.clipboard, "writeText").mockRejectedValue(new Error("denied"))
  Object.defineProperty(document, "execCommand", { configurable: true, value: vi.fn(() => true) })
  const success = vi.fn(); await act(async () => copyText("crewship login", success))
  expect(success).toHaveBeenCalledOnce(); expect(document.querySelector("textarea")).toBeNull()
  delete (document as unknown as Record<string, unknown>).execCommand
})
