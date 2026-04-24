import { describe, it, expect, vi, afterEach } from "vitest"
import { renderHook, act } from "@testing-library/react"
import { useKeyboardShortcuts } from "@/hooks/use-keyboard-shortcuts"

function dispatch(key: string, target?: HTMLElement) {
  const event = new KeyboardEvent("keydown", { key, bubbles: true })
  if (target) {
    Object.defineProperty(event, "target", { value: target, enumerable: true })
    target.dispatchEvent(event)
  } else {
    document.dispatchEvent(event)
  }
}

describe("useKeyboardShortcuts", () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it("fires single-key handler", () => {
    const handler = vi.fn()
    renderHook(() =>
      useKeyboardShortcuts([{ keys: "Escape", handler }]),
    )
    act(() => dispatch("Escape"))
    expect(handler).toHaveBeenCalledTimes(1)
  })

  it("fires chord (g + s) when second key arrives in time", () => {
    const handler = vi.fn()
    renderHook(() =>
      useKeyboardShortcuts([{ keys: ["g", "s"], handler }]),
    )
    act(() => {
      dispatch("g")
      dispatch("s")
    })
    expect(handler).toHaveBeenCalledTimes(1)
  })

  it("ignores keypresses in input elements", () => {
    const handler = vi.fn()
    renderHook(() =>
      useKeyboardShortcuts([{ keys: "j", handler }]),
    )
    const input = document.createElement("input")
    document.body.appendChild(input)
    act(() => dispatch("j", input))
    expect(handler).not.toHaveBeenCalled()
    document.body.removeChild(input)
  })

  it("respects enabled: false", () => {
    const handler = vi.fn()
    renderHook(() =>
      useKeyboardShortcuts([{ keys: "j", handler, enabled: false }]),
    )
    act(() => dispatch("j"))
    expect(handler).not.toHaveBeenCalled()
  })

  it("does not fire when modifier keys are pressed", () => {
    const handler = vi.fn()
    renderHook(() =>
      useKeyboardShortcuts([{ keys: "k", handler }]),
    )
    const ev = new KeyboardEvent("keydown", { key: "k", metaKey: true })
    act(() => { document.dispatchEvent(ev) })
    expect(handler).not.toHaveBeenCalled()
  })
})
