import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import SignupPage from "../signup/page"
import { serverFetch } from "@/lib/server-base"

const router = vi.hoisted(() => ({ push: vi.fn() }))
vi.mock("next/navigation", () => ({ useRouter: () => router }))
vi.mock("@/lib/server-base", () => ({ serverFetch: vi.fn() }))
beforeEach(() => { vi.mocked(serverFetch).mockReset(); router.push.mockReset() })
afterEach(() => { cleanup() })
function fill(confirmation = "synthetic-passphrase") {
  fireEvent.change(screen.getByLabelText("Full Name"), { target: { value: "Test Reader" } })
  fireEvent.change(screen.getByLabelText("Email"), { target: { value: "reader@example.test" } })
  fireEvent.change(screen.getByLabelText("Password", { exact: true }), { target: { value: "synthetic-passphrase" } })
  fireEvent.change(screen.getByLabelText("Confirm Password"), { target: { value: confirmation } })
}
async function submit() { await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Sign Up" })) }) }
it("rejects mismatched passwords without submitting personal data", async () => {
  render(<SignupPage />)
  fill("different-passphrase")
  await submit()
  expect(screen.getByText("Passwords do not match")).toBeVisible()
  expect(serverFetch).not.toHaveBeenCalled()
})
it("keeps signup pending until accepted and redirects to the same generic sign-in notice", async () => {
  let finish!: (response: Response) => void
  vi.mocked(serverFetch).mockReturnValue(new Promise(resolve => { finish = resolve }))
  render(<SignupPage />)
  fill()
  await submit()
  expect(screen.getByRole("button", { name: "Creating account..." })).toBeDisabled()
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
  expect(screen.getByRole("button", { name: "Sign Up" })).toBeEnabled()
  expect(screen.getByLabelText("Email")).toHaveValue("reader@example.test")
  expect(router.push).not.toHaveBeenCalled()
})
it.each(["network", "malformed body"])("recovers after %s and permits a deliberate retry", async (failure) => {
  if (failure === "network") vi.mocked(serverFetch).mockRejectedValueOnce(new Error("offline"))
  else vi.mocked(serverFetch).mockResolvedValueOnce(new Response("bad response", { status: 503 }))
  vi.mocked(serverFetch).mockResolvedValueOnce(Response.json({ ok: true }, { status: 202 }))
  render(<SignupPage />)
  fill()
  await submit()
  expect(screen.getByText(failure === "network" ? "Network error. Please try again." : "Something went wrong")).toBeVisible()
  expect(screen.getByRole("button", { name: "Sign Up" })).toBeEnabled()
  expect(screen.getByLabelText("Password", { exact: true })).toHaveValue("synthetic-passphrase")
  expect(router.push).not.toHaveBeenCalled()
  await submit()
  expect(router.push).toHaveBeenCalledWith("/login?signup=submitted")
})
