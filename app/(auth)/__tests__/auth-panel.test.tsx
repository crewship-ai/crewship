import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import LoginPage from "../login/page"
import SignupPage from "../signup/page"
import ResetPasswordPage from "../reset-password/page"
import BootstrapPage from "../bootstrap/page"
import { serverFetch } from "@/lib/server-base"

// The auth screens as the owner approved them on 2026-10-18 (wireframe v75,
// view "Přihlášení", #3583): the sail panel from main is back beside the form,
// and the fields carry the details people expect from a sign-in page.

const nav = vi.hoisted(() => ({
  query: "",
  router: { push: vi.fn(), replace: vi.fn(), refresh: vi.fn() },
}))
const auth = vi.hoisted(() => ({ signIn: vi.fn(), refresh: vi.fn() }))
vi.mock("next/navigation", () => ({
  useRouter: () => nav.router,
  useSearchParams: () => new URLSearchParams(nav.query),
}))
vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({ signIn: auth.signIn, refresh: auth.refresh, session: null, status: "unauthenticated" }),
}))
vi.mock("@/lib/server-base", () => ({ serverFetch: vi.fn() }))

async function renderLogin(status: Record<string, unknown> = { needs_bootstrap: false, allow_signup: true }) {
  nav.query = ""
  vi.mocked(serverFetch).mockResolvedValue(Response.json(status))
  render(<LoginPage />)
  await screen.findByRole("heading", { name: "Sign in to Crewship" })
}

beforeEach(() => {
  vi.mocked(serverFetch).mockReset()
  auth.signIn.mockReset()
  nav.router.push.mockReset()
  nav.router.replace.mockReset()
  nav.query = ""
})
afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

describe("sail panel", () => {
  it("draws the animated mark and the screen's own copy next to the form", async () => {
    await renderLogin()
    const panel = document.querySelector("[data-slot=auth-panel]")
    expect(panel).not.toBeNull()
    expect(panel!.querySelector("[data-testid=animated-mark]")).not.toBeNull()
    expect(panel).toHaveTextContent("Command the whole fleet.")
    // Branding, not a document section: the form's h1 stays the only heading.
    expect(panel!.querySelector("h1, h2, h3")).toBeNull()
    expect(document.querySelector("[data-slot=auth-screen]")).toHaveAttribute("data-layout", "split")
  })

  it("gives First run its own copy", async () => {
    vi.mocked(serverFetch).mockResolvedValueOnce(Response.json({ needs_bootstrap: true }))
    render(<BootstrapPage />)
    await screen.findByRole("heading", { name: "Create the administrator account" })
    expect(document.querySelector("[data-slot=auth-panel]")).toHaveTextContent("You’re the first aboard.")
  })
})

describe("password field", () => {
  it("shows and hides the password without losing focus", async () => {
    await renderLogin()
    const input = screen.getByLabelText("Password", { exact: true })
    expect(input).toHaveAttribute("type", "password")
    const toggle = screen.getByRole("button", { name: "Show password" })
    expect(toggle).toHaveAttribute("aria-pressed", "false")
    input.focus()
    fireEvent.click(toggle)
    expect(input).toHaveAttribute("type", "text")
    expect(screen.getByRole("button", { name: "Hide password" })).toHaveAttribute("aria-pressed", "true")
    expect(document.activeElement).toBe(input)
    fireEvent.click(screen.getByRole("button", { name: "Hide password" }))
    expect(input).toHaveAttribute("type", "password")
  })

  it("says when Caps Lock is on, and stops saying it when it is off", async () => {
    await renderLogin()
    const input = screen.getByLabelText("Password", { exact: true })
    expect(screen.queryByText("Caps Lock is on")).not.toBeInTheDocument()
    // jsdom does not track Caps Lock, so the event says what a browser would.
    const key = (type: "keydown" | "keyup", capsLock: boolean, k: string) => {
      const event = new KeyboardEvent(type, { key: k, bubbles: true })
      Object.defineProperty(event, "getModifierState", { value: (m: string) => m === "CapsLock" && capsLock })
      fireEvent(input, event)
    }
    key("keydown", true, "A")
    expect(screen.getByText("Caps Lock is on")).toBeVisible()
    key("keyup", false, "CapsLock")
    expect(screen.queryByText("Caps Lock is on")).not.toBeInTheDocument()
  })

  it("lets a password manager fill the sign-in form", async () => {
    await renderLogin()
    expect(screen.getByLabelText("Email")).toHaveAttribute("autocomplete", "username")
    expect(screen.getByLabelText("Password", { exact: true })).toHaveAttribute("autocomplete", "current-password")
  })

  it("ticks the length rule on a new password as it is typed", async () => {
    render(<SignupPage />)
    const rule = screen.getByText("At least 8 characters").closest("[data-slot=password-rule]")!
    expect(rule).toHaveAttribute("data-met", "false")
    fireEvent.change(screen.getByLabelText("Password", { exact: true }), { target: { value: "eight-ch" } })
    expect(rule).toHaveAttribute("data-met", "true")
  })
})

describe("sign in", () => {
  it("tells someone without sign-up how to get in", async () => {
    await renderLogin({ needs_bootstrap: false, allow_signup: false })
    expect(screen.getByText("Need access? Ask an administrator for an invitation.")).toBeVisible()
  })

  it("names the wait when the server rate-limits sign-in", async () => {
    auth.signIn.mockResolvedValue({ ok: false, error: "Too many requests", status: 429, retryAfter: 60 })
    await renderLogin()
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "reader@example.test" } })
    fireEvent.change(screen.getByLabelText("Password", { exact: true }), { target: { value: "synthetic-passphrase" } })
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Sign in" })) })
    expect(screen.getByRole("alert")).toHaveTextContent("Too many sign-in attempts. Try again in 1 minute, or reset your password.")
  })
})

describe("sign up", () => {
  it("shows the closed screen before the form when setup-status says sign-up is off", async () => {
    vi.mocked(serverFetch).mockImplementation(async (url) =>
      String(url).includes("setup-status") ? Response.json({ needs_bootstrap: false, allow_signup: false }) : Response.json({}),
    )
    render(<SignupPage />)
    expect(await screen.findByRole("heading", { name: "Sign-up is closed" })).toBeVisible()
    expect(screen.queryByLabelText("Full name")).not.toBeInTheDocument()
    expect(serverFetch).not.toHaveBeenCalledWith("/api/v1/auth/signup", expect.anything())
  })

  it("turns the server's 'registration is disabled' into a closed screen, not an operator hint", async () => {
    vi.mocked(serverFetch).mockImplementation(async (url) =>
      String(url).includes("setup-status")
        ? Response.json({ needs_bootstrap: false, allow_signup: true })
        : Response.json({ error: "Registration is disabled. Set CREWSHIP_ALLOW_SIGNUP=true to enable." }, { status: 403 }),
    )
    render(<SignupPage />)
    fireEvent.change(screen.getByLabelText("Full name"), { target: { value: "Test Reader" } })
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "reader@example.test" } })
    fireEvent.change(screen.getByLabelText("Password", { exact: true }), { target: { value: "synthetic-passphrase" } })
    fireEvent.change(screen.getByLabelText("Confirm password"), { target: { value: "synthetic-passphrase" } })
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Create account" })) })
    expect(await screen.findByRole("heading", { name: "Sign-up is closed" })).toBeVisible()
    expect(screen.queryByText(/CREWSHIP_ALLOW_SIGNUP/)).not.toBeInTheDocument()
    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/login")
  })
})

describe("field errors take focus", () => {
  it("sign up: a mismatch moves focus to Confirm password", async () => {
    render(<SignupPage />)
    fireEvent.change(screen.getByLabelText("Full name"), { target: { value: "Test Reader" } })
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "reader@example.test" } })
    fireEvent.change(screen.getByLabelText("Password", { exact: true }), { target: { value: "synthetic-passphrase" } })
    fireEvent.change(screen.getByLabelText("Confirm password"), { target: { value: "different-passphrase" } })
    const submit = screen.getByRole("button", { name: "Create account" })
    submit.focus()
    await act(async () => { fireEvent.click(submit) })
    expect(document.activeElement).toBe(screen.getByLabelText("Confirm password"))
  })

  it("reset: a short password moves focus to New password", async () => {
    nav.query = "token=synthetic-reset"
    render(<ResetPasswordPage />)
    fireEvent.change(screen.getByLabelText("New password"), { target: { value: "short" } })
    fireEvent.change(screen.getByLabelText("Confirm new password"), { target: { value: "short" } })
    const submit = screen.getByRole("button", { name: "Update password" })
    submit.focus()
    await act(async () => { fireEvent.click(submit) })
    expect(document.activeElement).toBe(screen.getByLabelText("New password"))
  })

  it("first run: a mismatch moves focus to Confirm password", async () => {
    vi.mocked(serverFetch).mockResolvedValueOnce(Response.json({ needs_bootstrap: true }))
    render(<BootstrapPage />)
    await screen.findByRole("heading", { name: "Create the administrator account" })
    fireEvent.change(screen.getByLabelText("Full name"), { target: { value: "Alex Johnson" } })
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "admin@example.test" } })
    fireEvent.change(screen.getByLabelText("Password", { exact: true }), { target: { value: "long-enough-pw" } })
    fireEvent.change(screen.getByLabelText("Confirm password"), { target: { value: "different-pw" } })
    const submit = screen.getByRole("button", { name: "Continue" })
    submit.focus()
    await act(async () => { fireEvent.click(submit) })
    expect(document.activeElement).toBe(screen.getByLabelText("Confirm password"))
  })
})

describe("reset without a token", () => {
  it("says the link is incomplete before asking for a password", () => {
    nav.query = ""
    render(<ResetPasswordPage />)
    expect(screen.getByText("This reset link is incomplete")).toBeVisible()
    expect(screen.queryByLabelText("New password")).not.toBeInTheDocument()
    expect(screen.getByRole("link", { name: "Request a new link" })).toHaveAttribute("href", "/forgot-password")
  })
})

describe("reset done", () => {
  it("offers Sign in and counts down instead of leaving silently", async () => {
    nav.query = "token=synthetic-reset"
    vi.mocked(serverFetch).mockResolvedValueOnce(Response.json({}))
    render(<ResetPasswordPage />)
    fireEvent.change(screen.getByLabelText("New password"), { target: { value: "new-passphrase" } })
    fireEvent.change(screen.getByLabelText("Confirm new password"), { target: { value: "new-passphrase" } })
    vi.useFakeTimers()
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Update password" })) })
    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/login")
    expect(screen.getByText("Opening sign-in in 5 s…")).toBeInTheDocument()
    act(() => { vi.advanceTimersByTime(2000) })
    expect(nav.router.push).not.toHaveBeenCalled()
    expect(screen.getByText("Opening sign-in in 3 s…")).toBeInTheDocument()
    act(() => { vi.advanceTimersByTime(3000) })
    expect(nav.router.push).toHaveBeenCalledWith("/login")
  })
})

describe("first run", () => {
  it("shows where the account step sits in setup", async () => {
    vi.mocked(serverFetch).mockResolvedValueOnce(Response.json({ needs_bootstrap: true }))
    render(<BootstrapPage />)
    await screen.findByRole("heading", { name: "Create the administrator account" })
    const steps = screen.getByRole("list", { name: "Setup steps" })
    const items = Array.from(steps.querySelectorAll("li")).map((li) => li.textContent?.replace(/^\d/, ""))
    expect(items).toEqual(["Account", "Workspace", "Model", "Crew"])
    expect(steps.querySelector("[aria-current=step]")).toHaveTextContent("Account")
  })
})
