import { act } from "react"
import { createRoot } from "react-dom/client"
import { fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import AppError from "./error"
import DashboardError from "./(dashboard)/error"
import GlobalError from "./global-error"
import NotFound from "./not-found"
import { globalErrorScheme } from "@/lib/theme/global-error-scheme"
import { errorDetails } from "@/components/layout/copy-error-details"

const capture = vi.hoisted(() => vi.fn())
vi.mock("@sentry/nextjs", () => ({ captureException: capture }))
afterEach(() => { vi.restoreAllMocks(); capture.mockReset() })

describe("application recovery screens", () => {
  it.each([undefined, "incident-42"])("reports the app boundary, keeps internal error text private and resets: %s", (digest) => {
    vi.spyOn(console, "error").mockImplementation(() => {})
    const error = Object.assign(new Error("internal database path /private/instance"), { digest })
    const reset = vi.fn()
    const { rerender } = render(<AppError error={error} reset={reset} />)
    expect(capture).toHaveBeenCalledExactlyOnceWith(error, { tags: { boundary: "app", digest: digest ?? "" } })
    const card = screen.getByRole("alert")
    expect(card).toHaveAttribute("data-state", "error")
    expect(card).toHaveTextContent("Crewship hit an error")
    // Outside the app frame: the lockup on the ground, no rail.
    expect(screen.getByRole("main")).toHaveClass("bare-state")
    expect(screen.queryByText(/internal database path/)).not.toBeInTheDocument()
    if (digest) expect(card).toHaveTextContent(`Error ID ${digest}`)
    else expect(card).not.toHaveTextContent(/Error ID/)
    fireEvent.click(screen.getByRole("button", { name: "Reload" }))
    expect(reset).toHaveBeenCalledTimes(1)
    rerender(<AppError error={error} reset={reset} />)
    expect(capture).toHaveBeenCalledTimes(1)
    const next = new Error("another failure")
    rerender(<AppError error={next} reset={reset} />)
    expect(capture).toHaveBeenLastCalledWith(next, { tags: { boundary: "app", digest: "" } })
  })

  it("a crashed screen keeps the frame: one Try again, the error id, Copy details and a quiet way home", () => {
    vi.spyOn(console, "error").mockImplementation(() => {})
    const error = Object.assign(new Error("secret"), { digest: "dig-7" })
    const reset = vi.fn()
    render(<DashboardError error={error} reset={reset} />)
    expect(capture).toHaveBeenCalledExactlyOnceWith(error, { tags: { boundary: "dashboard", digest: "dig-7" } })
    const card = screen.getByRole("alert")
    expect(card).toHaveTextContent("This page hit an error")
    expect(card).toHaveTextContent("Error ID dig-7")
    expect(screen.queryByRole("main")).toBeNull()
    // Exactly one filled action.
    const buttons = screen.getAllByRole("button")
    expect(buttons.map((b) => b.textContent)).toEqual(["Try again", "Copy details"])
    expect(screen.getByRole("link", { name: "Go to Dashboard" })).toHaveAttribute("href", "/")
    fireEvent.click(screen.getByRole("button", { name: "Try again" }))
    expect(reset).toHaveBeenCalledOnce()
    expect(screen.queryByText("secret")).toBeNull()
  })

  it("copy details carries the id, the page and the time — never the message", () => {
    const text = errorDetails("dig-7", new Date("2026-10-17T10:00:00Z"))
    expect(text).toContain("Error ID: dig-7")
    expect(text).toContain("Time: 2026-10-17T10:00:00.000Z")
    expect(errorDetails(undefined)).toContain("Error ID: none")
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
      expect(page.getByRole("alert").textContent).toContain("Crewship couldn’t start this page")
      expect(page.queryByText("private render detail")).toBeNull()
      expect(capture).toHaveBeenCalledExactlyOnceWith(error, { tags: { boundary: "global", digest: digest ?? "" } })
      if (digest) expect(page.getByRole("alert").textContent).toContain(`Error ID ${digest}`)
      else expect(page.getByRole("alert").textContent).not.toContain("Error ID")
      // No inline colours: the page paints from the app's tokens.
      expect(doc.body.innerHTML).not.toMatch(/style="[^"]*#[0-9a-f]{3,6}/i)
      fireEvent.click(page.getByRole("button", { name: "Reload" }))
      expect(reset).toHaveBeenCalledTimes(1)
    } finally { act(() => { root.unmount() }); frame.remove() }
  })

  it("the crash page follows the stored theme, then the system scheme", () => {
    const get = vi.mocked(localStorage.getItem)
    const media = (dark: boolean) => vi.spyOn(window, "matchMedia").mockReturnValue({ matches: dark } as MediaQueryList)
    get.mockReturnValueOnce("light"); media(true)
    expect(globalErrorScheme()).toBe("light")
    get.mockReturnValueOnce("dark"); media(false)
    expect(globalErrorScheme()).toBe("dark")
    get.mockReturnValueOnce("system"); media(true)
    expect(globalErrorScheme()).toBe("dark")
    get.mockReturnValueOnce(null); media(false)
    expect(globalErrorScheme()).toBe("light")
  })

  it("an unknown page outside a session is the bare not-found state with a way to the dashboard", () => {
    render(<NotFound />)
    const state = screen.getByText("This page doesn’t exist").closest("[data-state=not-found]")!
    expect(state).toBeTruthy()
    // Not found is a solid frame and never offers to create anything.
    expect(state.querySelector(".page-es")).toHaveClass("page-es-solid")
    expect(screen.getByText("The link may be old, or the page was moved.")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: "Go to Dashboard" })).toHaveAttribute("href", "/")
    expect(screen.getByRole("main")).toHaveClass("bare-state")
    expect(capture).not.toHaveBeenCalled()
  })
})
