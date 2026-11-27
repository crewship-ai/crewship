import { describe, expect, it, vi } from "vitest"
vi.mock("@/components/ui/confirm-host", () => ({ confirmHostMounted: () => false, askConfirm: vi.fn() }))
import { confirmLeaveUnsavedSheet } from "../confirm-unsaved-sheet"

describe("leaving a Sheet with changes not saved yet", () => {
  it("without the app's dialog keeps the Sheet open and never asks the browser", async () => {
    // jsdom has no window.confirm: a stub stands for the browser's dialog.
    const native = vi.fn(() => true)
    vi.stubGlobal("confirm", native)
    window.confirm = native
    try {
      await expect(confirmLeaveUnsavedSheet()).resolves.toBe(false)
      expect(native).not.toHaveBeenCalled()
    } finally { vi.unstubAllGlobals() }
  })
})
