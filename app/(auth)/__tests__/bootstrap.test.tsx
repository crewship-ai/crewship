import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import BootstrapPage from "../bootstrap/page"
import { serverFetch } from "@/lib/server-base"

// First-run setup in the Harbor 2 auth card (#3410): the gate, the field
// errors and the server band. The request body and the redirect targets are
// unchanged and pinned here so the redesign cannot move them.

const nav = vi.hoisted(() => ({ router: { replace: vi.fn(), push: vi.fn() } }))
const auth = vi.hoisted(() => ({ refresh: vi.fn() }))
vi.mock("next/navigation", () => ({ useRouter: () => nav.router }))
vi.mock("@/hooks/use-auth", () => ({ useAuth: () => ({ refresh: auth.refresh }) }))
vi.mock("@/lib/server-base", () => ({ serverFetch: vi.fn() }))

beforeEach(() => {
  vi.mocked(serverFetch).mockReset()
  nav.router.replace.mockReset()
  auth.refresh.mockReset()
})
afterEach(() => cleanup())

async function renderOpen() {
  vi.mocked(serverFetch).mockResolvedValueOnce(Response.json({ needs_bootstrap: true }))
  render(<BootstrapPage />)
  await screen.findByRole("heading", { name: "Create the administrator account" })
}

function fill(name = "Alex Johnson", password = "long-enough-pw", confirmation = password) {
  fireEvent.change(screen.getByLabelText("Full name"), { target: { value: name } })
  fireEvent.change(screen.getByLabelText("Email"), { target: { value: "admin@example.test" } })
  fireEvent.change(screen.getByLabelText("Password", { exact: true }), { target: { value: password } })
  fireEvent.change(screen.getByLabelText("Confirm password"), { target: { value: confirmation } })
}

async function submit() {
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Continue" })) })
}

describe("bootstrap card", () => {
  it("redirects to /login when setup is not needed", async () => {
    vi.mocked(serverFetch).mockResolvedValueOnce(Response.json({ needs_bootstrap: false }))
    render(<BootstrapPage />)
    await vi.waitFor(() => expect(nav.router.replace).toHaveBeenCalledWith("/login"))
    expect(screen.queryByLabelText("Full name")).not.toBeInTheDocument()
  })

  it("renders one primary Continue", async () => {
    await renderOpen()
    const buttons = screen.getAllByRole("button")
    expect(buttons).toHaveLength(1)
    expect(buttons[0]).toHaveAccessibleName("Continue")
  })

  it.each([
    ["A", "long-enough-pw", "long-enough-pw", "Full name", "Name must be at least 2 characters."],
    ["Alex Johnson", "short", "short", "Password", "Password must be at least 8 characters."],
    ["Alex Johnson", "long-enough-pw", "long-enough-px", "Confirm password", "Those passwords don't match."],
  ])("shows a wrong value under its field (%s / %s)", async (name, password, confirmation, field, message) => {
    await renderOpen()
    vi.mocked(serverFetch).mockClear()
    fill(name, password, confirmation)
    await submit()
    const alert = screen.getByRole("alert")
    expect(alert).toHaveTextContent(message)
    const input = screen.getByLabelText(field, { exact: true })
    expect(input).toHaveAttribute("aria-invalid", "true")
    expect(input).toHaveAttribute("aria-describedby", alert.id)
    expect(serverFetch).not.toHaveBeenCalled()
  })

  it("puts a server refusal in the band above the footer", async () => {
    await renderOpen()
    vi.mocked(serverFetch).mockResolvedValueOnce(Response.json({ error: "Bootstrap is closed" }, { status: 410 }))
    fill()
    await submit()
    const alert = screen.getByRole("alert")
    expect(alert).toHaveTextContent("Bootstrap is closed")
    expect(document.querySelector("[data-slot=auth-card-body]")!.contains(alert)).toBe(false)
  })

  it("sends the same body and goes on to /onboarding", async () => {
    await renderOpen()
    vi.mocked(serverFetch).mockResolvedValueOnce(Response.json({ ok: true }))
    fill()
    await submit()
    expect(serverFetch).toHaveBeenLastCalledWith("/api/v1/bootstrap", expect.objectContaining({
      method: "POST",
      body: JSON.stringify({ full_name: "Alex Johnson", email: "admin@example.test", password: "long-enough-pw" }),
    }))
    expect(auth.refresh).toHaveBeenCalledTimes(1)
    expect(nav.router.replace).toHaveBeenCalledWith("/onboarding")
  })

  it("hands a pending session to /login?registered=true", async () => {
    await renderOpen()
    vi.mocked(serverFetch).mockResolvedValueOnce(Response.json({ session_pending: true }))
    fill()
    await submit()
    expect(nav.router.replace).toHaveBeenCalledWith("/login?registered=true")
  })
})
