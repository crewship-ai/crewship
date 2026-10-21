// #3787 (wireframe round 64, Spojení §4): the login page opens on a
// full-screen state when it was reached because a session ended — expired,
// revoked elsewhere, or a plain Log out — and only shows the form once the
// person chooses to sign in. After signing in they return to the address
// they were on.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"

let search = ""
const push = vi.fn()
const signIn = vi.fn(async () => ({ ok: true }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push, replace: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(search),
}))
vi.mock("@/hooks/use-auth", () => ({ useAuth: () => ({ signIn, session: null, status: "unauthenticated" }) }))
vi.mock("@/lib/server-base", () => ({ serverFetch: vi.fn(async () => ({ ok: true, json: async () => ({ needs_bootstrap: false, allow_signup: true }) })) }))

import LoginPage from "../login/page"
import { noteUnsentDraft, rememberSignedOut, readSignedOut } from "@/lib/signed-out"

const DRAFT_KEY = JSON.stringify(["user", "u1", "s-1"])
function keepDraft(text: string) {
  localStorage.setItem("crewship-composer", JSON.stringify({ state: { modelId: null, drafts: { [DRAFT_KEY]: text } }, version: 0 }))
}

beforeEach(() => {
  search = ""
  push.mockClear()
  signIn.mockClear()
  sessionStorage.clear()
  // vitest.setup.ts stubs localStorage with no-op mocks; give it a memory.
  const store = new Map<string, string>()
  vi.mocked(localStorage.getItem).mockImplementation((k: string) => store.get(k) ?? null)
  vi.mocked(localStorage.setItem).mockImplementation((k: string, v: string) => { store.set(k, String(v)) })
})
afterEach(() => { cleanup(); noteUnsentDraft("", undefined, "") })

const heading = (name: string) => screen.findByRole("heading", { level: 1, name })

describe("Your session expired", () => {
  it("says the draft is kept and who was signed in, then signs in and returns to the same address", async () => {
    keepDraft("Rebuild both images tonight")
    noteUnsentDraft(DRAFT_KEY, "Riley", "Rebuild both images tonight")
    rememberSignedOut("ada@example.test")
    search = "reason=expired&redirect=%2Fchat%2Friley%3Fsession%3Ds-1"
    render(<LoginPage />)
    const screenEl = (await heading("Your session expired")).closest("[data-slot=signed-out-screen]")!
    expect(screenEl).toHaveAttribute("data-state", "expired")
    expect(screenEl).toHaveTextContent("Sign in again to pick up where you left off. Your unsent message to Riley is kept on this device.")
    expect(screenEl).toHaveTextContent("Signed in as ada@example.test · Use another account")
    expect(screen.queryByLabelText("Password")).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "Sign in" }))
    expect(await screen.findByLabelText("Email")).toHaveValue("ada@example.test")
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "pw" } })
    fireEvent.submit(screen.getByLabelText("Password").closest("form")!)
    await waitFor(() => expect(push).toHaveBeenCalledWith("/chat/riley?session=s-1"))
    expect(signIn).toHaveBeenCalledWith("ada@example.test", "pw")
    // The draft itself is untouched; only the screen's memo is gone.
    expect(JSON.parse(localStorage.getItem("crewship-composer")!).state.drafts[DRAFT_KEY]).toBe("Rebuild both images tonight")
    expect(readSignedOut()).toEqual({ email: null, draftAgent: null })
  })

  it("has no draft line when there is no unsent message", async () => {
    rememberSignedOut("ada@example.test")
    search = "reason=expired"
    render(<LoginPage />)
    const screenEl = (await heading("Your session expired")).closest("[data-slot=signed-out-screen]")!
    expect(screenEl).not.toHaveTextContent("unsent message")
  })

  it("Use another account opens an empty form", async () => {
    rememberSignedOut("ada@example.test")
    search = "reason=expired"
    render(<LoginPage />)
    await heading("Your session expired")
    fireEvent.click(screen.getByRole("button", { name: "Use another account" }))
    expect(await screen.findByLabelText("Email")).toHaveValue("")
    expect(readSignedOut().email).toBeNull()
  })
})

describe("You're signed out", () => {
  it("is a quiet goodbye with Sign in again and a way back to crewship.ai", async () => {
    search = "reason=signed-out"
    render(<LoginPage />)
    const screenEl = (await heading("You're signed out")).closest("[data-slot=signed-out-screen]")!
    expect(screenEl).toHaveTextContent("See you next time.")
    expect(screen.getByRole("link", { name: "Back to crewship.ai" })).toHaveAttribute("href", "https://crewship.ai")
    // No error colours anywhere on it (the button's own aria-invalid styling
    // is the kit's and never applies to a button).
    const tones = [...screenEl.querySelectorAll("*")].flatMap((el) => [...el.classList]).filter((c) => !c.includes("aria-invalid"))
    expect(tones.join(" ")).not.toMatch(/destructive|danger|warn|error/)
    fireEvent.click(screen.getByRole("button", { name: "Sign in again" }))
    expect(await screen.findByLabelText("Email")).toBeInTheDocument()
  })
})

describe("Signed out on this device", () => {
  it("explains why, then signs in", async () => {
    search = "reason=revoked&redirect=%2Fissues"
    render(<LoginPage />)
    const screenEl = (await heading("Signed out on this device")).closest("[data-slot=signed-out-screen]")!
    expect(screenEl).toHaveTextContent("Someone signed you out from another device or an admin ended your session.")
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }))
    expect(await screen.findByLabelText("Email")).toBeInTheDocument()
  })
})

it("plain /login still opens straight on the form", async () => {
  render(<LoginPage />)
  expect(await screen.findByLabelText("Email")).toBeInTheDocument()
  expect(document.querySelector("[data-slot=signed-out-screen]")).toBeNull()
})
