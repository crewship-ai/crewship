// A sign-in refused on the server (Google for a suspended account) comes back
// to /login?error=signin. The page says it could not sign the person in —
// without saying why, like a wrong password.
import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams("error=signin"),
}))
vi.mock("@/hooks/use-auth", () => ({ useAuth: () => ({ signIn: vi.fn(), session: null, status: "unauthenticated" }) }))
vi.mock("@/lib/server-base", () => ({ serverFetch: vi.fn(async () => ({ ok: false, json: async () => ({}) })) }))

import LoginPage from "./page"

describe("login after a refused sign-in", () => {
  it("says it could not sign you in, and not why", async () => {
    render(<LoginPage />)
    const alert = await screen.findByText(/We could not sign you in/)
    expect(alert).toHaveTextContent("We could not sign you in")
    expect(alert).not.toHaveTextContent(/suspend/i)
  })
})
