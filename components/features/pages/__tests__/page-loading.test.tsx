import React from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { PagePreviewFrame } from "../page-preview"

const port = { onmessage: null as ((event: { data: unknown }) => void) | null, start: vi.fn(), close: vi.fn(), postMessage: vi.fn() }
beforeEach(() => {
  vi.spyOn(navigator, "userAgent", "get").mockReturnValue("Mozilla/5.0 Chrome/140.0.0.0 Safari/537.36")
  vi.useFakeTimers()
  const w = window as unknown as { happyDOM?: { settings: { disableIframePageLoading: boolean } } }
  if (w.happyDOM) w.happyDOM.settings.disableIframePageLoading = true
  vi.stubGlobal("MessageChannel", class { port1 = port; port2 = { close: vi.fn() } })
})
afterEach(() => { cleanup(); vi.useRealTimers(); vi.unstubAllGlobals() })
const props = { page: { slug: "health", panels: [] }, artifact: { format: "crewship-page-preview/v1" as const, javascript: "void 0", css: "", toolchain: "test" }, runtimeURL: "https://pages.example.net/api/v1/pages/runtime/bootstrap" }
it("conceals the empty document until render readiness, then preserves the frame on data updates", () => {
  const { rerender } = render(<PagePreviewFrame {...props} />)
  const frame = screen.getByTitle("Application preview") as HTMLIFrameElement
  expect(frame.style.opacity).toBe("0")
  expect(frame.tabIndex).toBe(-1)
  expect(screen.getByRole("status").textContent).toContain("Loading")
  Object.defineProperty(frame, "contentWindow", { value: { postMessage: vi.fn() }, configurable: true })
  fireEvent.load(frame)
  act(() => vi.advanceTimersByTime(100))
  act(() => port.onmessage?.({ data: { type: "crewship.pages.snapshot-ack/v1", seq: 1 } }))
  expect(frame.style.opacity).toBe("0")
  act(() => port.onmessage?.({ data: { type: "crewship.pages.rendered/v1" } }))
  expect(frame.style.opacity).toBe("1")
  expect(frame.tabIndex).toBe(0)
  expect(screen.queryByRole("status")).toBeNull()
  rerender(<PagePreviewFrame {...props} page={{ slug: "health", name: "Updated", panels: [] }} />)
  expect(screen.getByTitle("Application preview")).toBe(frame)
  act(() => vi.advanceTimersByTime(21000))
  expect(screen.queryByRole("alert")).toBeNull()
})
it("replaces a stalled load with a recoverable error and removes executable content", () => {
  render(<PagePreviewFrame {...props} />)
  act(() => vi.advanceTimersByTime(20000))
  expect(screen.getByRole("alert").textContent).toContain("reopen")
  expect(screen.queryByTitle("Application preview")).toBeNull()
})
