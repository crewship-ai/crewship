import { act } from "react"
import { createRoot } from "react-dom/client"
import { fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import AppError from "./error"
import GlobalError from "./global-error"
import NotFound from "./not-found"

const capture = vi.hoisted(() => vi.fn())
vi.mock("@sentry/nextjs", () => ({ captureException: capture }))
afterEach(() => { vi.restoreAllMocks() })

describe("application recovery screens", () => {
  it.each([undefined, "incident-42"])("reports the app boundary, keeps internal error text private and resets: %s", (digest) => {
    vi.spyOn(console, "error").mockImplementation(() => {})
    const error = Object.assign(new Error("internal database path /private/instance"), { digest })
    const reset = vi.fn()
    const { rerender } = render(<AppError error={error} reset={reset} />)
    expect(capture).toHaveBeenCalledExactlyOnceWith(error, { tags: { boundary: "app", digest: digest ?? "" } })
    expect(screen.getByRole("heading", { name: "Something went wrong" })).toBeInTheDocument()
    expect(screen.queryByText(/internal database path/)).not.toBeInTheDocument()
    if (digest) expect(screen.getByText(`Error ID: ${digest}`)).toBeInTheDocument()
    else expect(screen.queryByText(/Error ID:/)).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Try Again" }))
    expect(reset).toHaveBeenCalledTimes(1)
    rerender(<AppError error={error} reset={reset} />)
    expect(capture).toHaveBeenCalledTimes(1)
    const next = new Error("another failure")
    rerender(<AppError error={next} reset={reset} />)
    expect(capture).toHaveBeenLastCalledWith(next, { tags: { boundary: "app", digest: "" } })
  })
  it.each([undefined, "fatal-42"])("renders a standalone document after root-layout failure: %s", (digest) => {
    const frame = document.createElement("iframe")
    document.body.appendChild(frame)
    const doc = frame.contentDocument!
    const root = createRoot(doc)
    const error = Object.assign(new Error("private render detail"), { digest })
    const reset = vi.fn()
    try {
      act(() => { root.render(<GlobalError error={error} reset={reset} />) })
      const page = within(doc.body)
      expect(doc.documentElement.lang).toBe("en")
      expect(page.getByRole("heading", { name: "Critical Error" })).toBeTruthy()
      expect(page.queryByText("private render detail")).toBeNull()
      expect(capture).toHaveBeenCalledExactlyOnceWith(error, { tags: { boundary: "global", digest: digest ?? "" } })
      if (digest) expect(page.getByText(`Error ID: ${digest}`)).toBeTruthy()
      else expect(page.queryByText(/Error ID:/)).toBeNull()
      fireEvent.click(page.getByRole("button", { name: "Reload" }))
      expect(reset).toHaveBeenCalledTimes(1)
    } finally { act(() => { root.unmount() }); frame.remove() }
  })
  it("provides a dashboard escape route for unknown pages", () => {
    render(<NotFound />)
    expect(screen.getByRole("heading", { name: "Page Not Found" })).toBeInTheDocument()
    expect(screen.getByRole("link", { name: "Go to Dashboard" })).toHaveAttribute("href", "/")
    expect(capture).not.toHaveBeenCalled()
  })
})
