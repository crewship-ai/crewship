import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import LoginPage from "../login/page"
import { serverFetch } from "@/lib/server-base"

// Harbor 2 auth card on /login (#3410). The request, the redirect validation
// and the first-run gate are pinned elsewhere (safe-redirect, login-refused);
// this file pins what the person sees: one card, one primary, feedback in the
// right rank, and texts that say no more than the API does.

const nav = vi.hoisted(() => ({
  query: "",
  router: { push: vi.fn(), replace: vi.fn(), refresh: vi.fn() },
}))
const auth = vi.hoisted(() => ({ signIn: vi.fn() }))
vi.mock("next/navigation", () => ({
  useRouter: () => nav.router,
  useSearchParams: () => new URLSearchParams(nav.query),
}))
vi.mock("@/hooks/use-auth", () => ({ useAuth: () => ({ signIn: auth.signIn, session: null, status: "unauthenticated" }) }))
vi.mock("@/lib/server-base", () => ({ serverFetch: vi.fn() }))

function setupStatus(body: Record<string, unknown>) {
  vi.mocked(serverFetch).mockResolvedValue(Response.json(body))
}

async function renderLogin(query = "", status: Record<string, unknown> = { needs_bootstrap: false, allow_signup: true }) {
  nav.query = query
  setupStatus(status)
  render(<LoginPage />)
  await screen.findByRole("heading", { name: "Sign in to Crewship" })
}

beforeEach(() => {
  vi.mocked(serverFetch).mockReset()
  auth.signIn.mockReset()
  nav.router.push.mockReset()
  nav.router.replace.mockReset()
})
afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

describe("login card", () => {
  it("renders one primary Sign in and hides Create an account when allow_signup=false", async () => {
    await renderLogin("", { needs_bootstrap: false, allow_signup: false })
    const buttons = screen.getAllByRole("button")
    expect(buttons).toHaveLength(1)
    expect(buttons[0]).toHaveAccessibleName("Sign in")
    expect(buttons[0]).toHaveAttribute("type", "submit")
    expect(screen.queryByRole("link", { name: "Create an account" })).not.toBeInTheDocument()
  })

  it("offers Create an account in the footer when sign-up is on", async () => {
    await renderLogin()
    const create = screen.getByRole("link", { name: "Create an account" })
    expect(create).toHaveAttribute("href", "/signup")
    // The primary is reached first; the footer links follow it.
    const signIn = screen.getByRole("button", { name: "Sign in" })
    expect(signIn.compareDocumentPosition(create) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it("keeps the tab order Email → Password → Forgot password → Sign in", async () => {
    await renderLogin("", { needs_bootstrap: false, allow_signup: false })
    const order = [
      screen.getByLabelText("Email"),
      screen.getByLabelText("Password", { exact: true }),
      screen.getByRole("link", { name: "Forgot password?" }),
      screen.getByRole("button", { name: "Sign in" }),
    ]
    for (let i = 1; i < order.length; i++) {
      expect(order[i - 1].compareDocumentPosition(order[i]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    }
  })

  it("shows nothing for the first second of the gate, then a card skeleton", async () => {
    vi.useFakeTimers()
    nav.query = ""
    vi.mocked(serverFetch).mockReturnValue(new Promise(() => {}))
    render(<LoginPage />)
    const skeleton = document.querySelector("[data-slot=auth-card-skeleton]")
    expect(skeleton).not.toBeNull()
    expect(skeleton!.querySelector(".page-sk")).toBeNull()
    act(() => { vi.advanceTimersByTime(1000) })
    expect(skeleton!.querySelectorAll(".page-sk").length).toBeGreaterThan(0)
    expect(screen.queryByLabelText("Email")).not.toBeInTheDocument()
  })

  it("?registered=true shows the ok banner, role=status", async () => {
    await renderLogin("registered=true")
    const banner = screen.getByRole("status")
    expect(banner).toHaveTextContent("Administrator account created. Sign in to continue setup.")
    expect(banner).toHaveAttribute("data-tone", "ok")
  })

  it("signup=submitted: banner text does not reveal whether the address existed", async () => {
    await renderLogin("signup=submitted")
    const banner = screen.getByRole("status")
    expect(banner).toHaveTextContent("If that address was not registered yet, the account is ready. Sign in below.")
    // The same sentence for a new and an existing address: no welcome, no
    // "already registered", no thanks that implies a new account.
    expect(banner).not.toHaveTextContent(/welcome|already has|thanks|created/i)
  })

  it("expired banner hides when a submit error is shown", async () => {
    auth.signIn.mockResolvedValue({ ok: false, error: "Invalid email or password" })
    await renderLogin("reason=expired")
    expect(screen.getByRole("status")).toHaveTextContent("Your session expired. Please sign in again.")
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "reader@example.test" } })
    fireEvent.change(screen.getByLabelText("Password", { exact: true }), { target: { value: "wrong-passphrase" } })
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Sign in" })) })
    expect(screen.queryByText("Your session expired. Please sign in again.")).not.toBeInTheDocument()
    expect(screen.getByRole("alert")).toHaveTextContent("Invalid email or password")
  })

  it("a refused sign-in is one band above the footer, role=alert", async () => {
    await renderLogin("error=signin")
    const band = screen.getByRole("alert")
    expect(band).toHaveTextContent("We could not sign you in. Try again, or ask your administrator.")
    const footer = document.querySelector("[data-slot=auth-card-footer]")!
    expect(band.compareDocumentPosition(footer) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(document.querySelector("[data-slot=auth-card-body]")!.contains(band)).toBe(false)
  })

  it("submits the same credentials and keeps the validated redirect", async () => {
    auth.signIn.mockResolvedValue({ ok: true })
    await renderLogin("redirect=%2Fissues%3Fview%3Dmine")
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "reader@example.test" } })
    fireEvent.change(screen.getByLabelText("Password", { exact: true }), { target: { value: "synthetic-passphrase" } })
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Sign in" })) })
    expect(auth.signIn).toHaveBeenCalledWith("reader@example.test", "synthetic-passphrase")
    expect(nav.router.push).toHaveBeenCalledWith("/issues?view=mine")
  })

  it("an off-site redirect still lands on /", async () => {
    auth.signIn.mockResolvedValue({ ok: true })
    await renderLogin("redirect=https%3A%2F%2Fevil.example")
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "reader@example.test" } })
    fireEvent.change(screen.getByLabelText("Password", { exact: true }), { target: { value: "synthetic-passphrase" } })
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Sign in" })) })
    expect(nav.router.push).toHaveBeenCalledWith("/")
  })

  it("still sends a fresh install to /bootstrap with the redirect as next", async () => {
    nav.query = "redirect=%2Fissues"
    setupStatus({ needs_bootstrap: true })
    render(<LoginPage />)
    await vi.waitFor(() => expect(nav.router.replace).toHaveBeenCalledWith("/bootstrap?next=%2Fissues"))
  })
})
