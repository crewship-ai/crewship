import { renderHook } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
import { useLeaveGuard } from "../use-leave-guard"

const leave = () => {
  const event = new Event("beforeunload", { cancelable: true }) as BeforeUnloadEvent
  window.dispatchEvent(event)
  return event.defaultPrevented
}

describe("useLeaveGuard", () => {
  it.each([[false, false], [true, true]])("losesWork %s → browser prompt %s", (losesWork, prompted) => {
    const view = renderHook(() => useLeaveGuard(losesWork))
    expect(leave()).toBe(prompted)
    view.unmount()
    expect(leave()).toBe(false)
  })
  it("registers the prompt only while work would be lost", () => {
    const view = renderHook(({ losesWork }) => useLeaveGuard(losesWork), { initialProps: { losesWork: false } })
    expect(leave()).toBe(false)
    view.rerender({ losesWork: true })
    expect(leave()).toBe(true)
    view.rerender({ losesWork: false })
    expect(leave()).toBe(false)
  })
  it("reports to the host's in-app guard and clears it on unmount", () => {
    const onDirtyChange = vi.fn()
    const view = renderHook(({ losesWork }) => useLeaveGuard(losesWork, onDirtyChange), { initialProps: { losesWork: false } })
    expect(onDirtyChange).toHaveBeenLastCalledWith(false)
    view.rerender({ losesWork: true })
    expect(onDirtyChange).toHaveBeenLastCalledWith(true)
    view.unmount()
    expect(onDirtyChange).toHaveBeenLastCalledWith(false)
  })
})
