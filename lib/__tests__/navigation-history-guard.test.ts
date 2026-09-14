import { it, expect, vi } from "vitest"
import { HISTORY_NAVIGATION_GUARD_SCRIPT, registerHistoryNavigationGuard } from "../navigation-history-guard"

it("asks registered guards before a later router listener can unmount the editor", () => {
  // Execute the exact static head script that ships in the export.
  new Function("window", HISTORY_NAVIGATION_GUARD_SCRIPT)(window)
  const router = vi.fn()
  window.addEventListener("popstate", router)
  const guard = vi.fn(() => false)
  const unregister = registerHistoryNavigationGuard(guard)
  try {
    window.dispatchEvent(new PopStateEvent("popstate"))
    expect(guard).toHaveBeenCalledOnce()
    expect(router).not.toHaveBeenCalled()
    guard.mockReturnValue(true)
    window.dispatchEvent(new PopStateEvent("popstate"))
    expect(router).toHaveBeenCalledOnce()
    unregister()
    window.dispatchEvent(new PopStateEvent("popstate"))
    expect(router).toHaveBeenCalledTimes(2)
    expect(guard).toHaveBeenCalledTimes(2)
  } finally {
    unregister()
    window.removeEventListener("popstate", router)
  }
})
