// AUTH-01 (#3494, auth-public.md A06): with a valid ?redirect=, the sign-in
// card says where the person returns to — by the page's name from the
// navigation, never the raw path. The redirect is validated by the same
// safeRedirectPath the sign-in uses to navigate, so a rejected target
// (absolute URL, //host, backslash, /login) shows no line. Sign-in itself is
// unchanged.
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen } from "@testing-library/react"

let search = ""
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(search),
}))
vi.mock("@/hooks/use-auth", () => ({ useAuth: () => ({ signIn: vi.fn(), session: null, status: "unauthenticated" }) }))
vi.mock("@/lib/server-base", () => ({ serverFetch: vi.fn(async () => ({ ok: false, json: async () => ({}) })) }))

import LoginPage from "./page"
import { returnPageName } from "./return-to"

describe("returnPageName", () => {
  it.each([
    ["/issues", "Issues"],
    ["/issues/ISS-12?tab=activity#c3", "Issues"],
    ["/routines?tab=calendar", "Routines"],
    ["/credentials", "Vault"],
    ["/settings/members", "Settings"],
    ["/", "Dashboard"],
  ])("%s → %s", (raw, name) => {
    expect(returnPageName(raw)).toBe(name)
  })

  it.each([
    [null],
    [""],
    ["https://evil.example/issues"],
    ["//evil.example/issues"],
    ["/\\evil.example"],
    ["/login?redirect=/issues"],
    ["issues"],
    // A path the navigation has no name for: no raw path on screen.
    ["/no-such-page"],
  ])("%s → no line", (raw) => {
    expect(returnPageName(raw)).toBeNull()
  })
})

describe("sign-in card", () => {
  beforeEach(() => {
    search = ""
  })

  it("says the page a valid redirect returns to", async () => {
    search = "redirect=%2Fissues%2FISS-12%3Ftab%3Dactivity"
    render(<LoginPage />)
    const line = await screen.findByText("You'll return to Issues.")
    expect(line).toHaveAttribute("data-slot", "auth-return-to")
    // The card's description keeps its sentence; the line follows it.
    expect(screen.getByText(/Sign in to manage your AI workforce\./)).toBeInTheDocument()
    expect(document.body).not.toHaveTextContent("ISS-12")
  })

  it("shows no line without a redirect", async () => {
    render(<LoginPage />)
    await screen.findByRole("heading", { name: "Sign in to Crewship" })
    expect(document.querySelector("[data-slot=auth-return-to]")).toBeNull()
  })

  it("shows no line for a redirect the sign-in would refuse", async () => {
    search = "redirect=https%3A%2F%2Fevil.example%2Fissues"
    render(<LoginPage />)
    await screen.findByRole("heading", { name: "Sign in to Crewship" })
    expect(document.querySelector("[data-slot=auth-return-to]")).toBeNull()
  })
})
