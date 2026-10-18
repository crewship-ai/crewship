import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import SignupPage from "../signup/page"
import { serverFetch } from "@/lib/server-base"

const router = vi.hoisted(() => ({ push: vi.fn() }))
vi.mock("next/navigation", () => ({ useRouter: () => router }))
vi.mock("@/lib/server-base", () => ({ serverFetch: vi.fn() }))
beforeEach(() => { window.sessionStorage.clear(); vi.mocked(serverFetch).mockReset(); router.push.mockReset() })
afterEach(() => { cleanup() })
function fill(confirmation = "synthetic-passphrase") {
  fireEvent.change(screen.getByLabelText("Full name"), { target: { value: "Test Reader" } })
  fireEvent.change(screen.getByLabelText("Email"), { target: { value: "reader@example.test" } })
  fireEvent.change(screen.getByLabelText("Password", { exact: true }), { target: { value: "synthetic-passphrase" } })
  fireEvent.change(screen.getByLabelText("Confirm password"), { target: { value: confirmation } })
}
async function submit() { await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Create account" })) }) }
it("rejects mismatched passwords without submitting personal data", async () => {
  render(<SignupPage />)
  fill("different-passphrase")
  await submit()
  expect(screen.getByText("Passwords do not match")).toBeVisible()
  expect(serverFetch).not.toHaveBeenCalledWith("/api/v1/auth/signup", expect.anything())
})
it("mismatch shows field error under Confirm password, no toast", async () => {
  render(<SignupPage />)
  fill("different-passphrase")
  await submit()
  const confirm = screen.getByLabelText("Confirm password")
  expect(confirm).toHaveAttribute("aria-invalid", "true")
  const error = screen.getByRole("alert")
  expect(error).toHaveTextContent("Passwords do not match")
  expect(confirm).toHaveAttribute("aria-describedby", error.id)
  // Field-level, not the band above the footer and not a toast.
  expect(document.querySelector("[data-slot=auth-card-body]")!.contains(error)).toBe(true)
  expect(document.querySelector("[data-sonner-toaster]")).toBeNull()
  // Typing again clears it.
  fireEvent.change(confirm, { target: { value: "synthetic-passphrase" } })
  expect(screen.queryByRole("alert")).not.toBeInTheDocument()
})
it("an invitation says which address to use", () => {
  window.history.replaceState({}, "", "/signup?invitation_token=synthetic-invitation-proof")
  render(<SignupPage />)
  expect(screen.getByText("You're accepting an invitation. Use the address it was sent to.")).toBeVisible()
  window.history.replaceState({}, "", "/signup")
})
it("keeps signup pending until accepted and redirects to the same generic sign-in notice", async () => {
  let finish!: (response: Response) => void
  vi.mocked(serverFetch).mockReturnValue(new Promise(resolve => { finish = resolve }))
  render(<SignupPage />)
  fill()
  await submit()
  expect(screen.getByRole("button", { name: "Creating account…" })).toBeDisabled()
  expect(router.push).not.toHaveBeenCalled()
  expect(serverFetch).toHaveBeenCalledWith("/api/v1/auth/signup", expect.objectContaining({ method: "POST", body: JSON.stringify({ full_name: "Test Reader", email: "reader@example.test", password: "synthetic-passphrase" }) }))
  await act(async () => { finish(Response.json({ ok: true }, { status: 202 })) })
  expect(router.push).toHaveBeenCalledWith("/login?signup=submitted")
})
it.each([
  [{ error: { fieldErrors: { email: ["Invalid email"], password: ["Too short"] } } }, "Invalid email. Too short"],
  [{ error: { fieldErrors: {} } }, "Invalid input"],
  [{ error: "Signup is disabled" }, "Signup is disabled"],
  [{ error: {} }, "Something went wrong"],
  [null, "Something went wrong"],
] as const)("shows a refusal without discarding the entered values (%s)", async (body, message) => {
  vi.mocked(serverFetch).mockResolvedValue(Response.json(body, { status: 400 }))
  render(<SignupPage />)
  fill()
  await submit()
  expect(screen.getByRole("alert")).toHaveTextContent(message)
  expect(screen.getByRole("button", { name: "Create account" })).toBeEnabled()
  expect(screen.getByLabelText("Email")).toHaveValue("reader@example.test")
  expect(router.push).not.toHaveBeenCalled()
})
it.each(["network", "malformed body"])("recovers after %s and permits a deliberate retry", async (failure) => {
  // The page asks setup-status first; sign-up is on.
  vi.mocked(serverFetch).mockResolvedValueOnce(Response.json({ needs_bootstrap: false, allow_signup: true }))
  if (failure === "network") vi.mocked(serverFetch).mockRejectedValueOnce(new Error("offline"))
  else vi.mocked(serverFetch).mockResolvedValueOnce(new Response("bad response", { status: 503 }))
  vi.mocked(serverFetch).mockResolvedValueOnce(Response.json({ ok: true }, { status: 202 }))
  render(<SignupPage />)
  fill()
  await submit()
  expect(screen.getByText(failure === "network" ? "Network error. Please try again." : "Something went wrong")).toBeVisible()
  expect(screen.getByRole("button", { name: "Create account" })).toBeEnabled()
  expect(screen.getByLabelText("Password", { exact: true })).toHaveValue("synthetic-passphrase")
  expect(router.push).not.toHaveBeenCalled()
  await submit()
  expect(router.push).toHaveBeenCalledWith("/login?signup=submitted")
})

it("carries an invitation token through signup and the sign-in handoff", async () => {
  window.history.replaceState({}, "", "/signup?invitation_token=synthetic-invitation-proof")
  vi.mocked(serverFetch).mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 202 }))
  render(<SignupPage />)
  fill()
  await submit()
  expect(serverFetch).toHaveBeenCalledWith("/api/v1/auth/signup", expect.objectContaining({ body: JSON.stringify({ full_name: "Test Reader", email: "reader@example.test", password: "synthetic-passphrase", invitation_token: "synthetic-invitation-proof" }) }))
  expect(router.push).toHaveBeenCalledWith("/login?signup=submitted&redirect=%2Fjoin")
  expect(window.location.href).not.toContain("synthetic-invitation-proof")
  window.history.replaceState({}, "", "/signup")
})
