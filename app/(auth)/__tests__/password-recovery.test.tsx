import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import ForgotPasswordPage from "../forgot-password/page"
import ResetPasswordPage from "../reset-password/page"
import { serverFetch } from "@/lib/server-base"

const navigation = vi.hoisted(() => ({ query: "token=synthetic-reset", router: { push: vi.fn() } }))
vi.mock("next/navigation", () => ({ useRouter: () => navigation.router, useSearchParams: () => new URLSearchParams(navigation.query) }))
vi.mock("@/lib/server-base", () => ({ serverFetch: vi.fn() }))
beforeEach(() => { vi.mocked(serverFetch).mockReset(); navigation.router.push.mockReset(); navigation.query = "token=synthetic-reset" })
afterEach(() => { cleanup(); vi.useRealTimers() })

function fillReset(password = "new-passphrase", confirmation = password) {
  fireEvent.change(screen.getByLabelText("New password"), { target: { value: password } })
  fireEvent.change(screen.getByLabelText("Confirm new password"), { target: { value: confirmation } })
}
async function submitReset() {
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Update password" })) })
}

describe("password recovery", () => {
  it("disables empty and pending requests and submits only the entered address", async () => {
    let finish!: (response: Response) => void
    vi.mocked(serverFetch).mockReturnValue(new Promise(resolve => { finish = resolve }))
    render(<ForgotPasswordPage />)
    expect(screen.getByRole("button", { name: "Send reset link" })).toBeDisabled()
    expect(screen.getByRole("link", { name: "Back to sign in" })).toHaveAttribute("href", "/login")
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "reader@example.test" } })
    fireEvent.click(screen.getByRole("button", { name: "Send reset link" }))
    expect(screen.getByRole("button", { name: "Sending..." })).toBeDisabled()
    expect(serverFetch).toHaveBeenCalledWith("/api/v1/auth/forgot", expect.objectContaining({ method: "POST", body: JSON.stringify({ email: "reader@example.test" }) }))
    await act(async () => { finish(new Response(null, { status: 202 })) })
    expect(screen.getByRole("status")).toHaveTextContent("If an account exists")
    expect(screen.getByRole("status")).toHaveTextContent("crewship admin reset-password")
    expect(screen.queryByRole("button")).not.toBeInTheDocument()
  })
  it.each(["server refusal", "network rejection", "synchronous transport error"])("preserves the same non-enumerating response after %s", async (failure) => {
    if (failure === "server refusal") vi.mocked(serverFetch).mockResolvedValue(new Response("private server detail", { status: 503 }))
    else if (failure === "network rejection") vi.mocked(serverFetch).mockRejectedValue(new Error("private network detail"))
    else vi.mocked(serverFetch).mockImplementation(() => { throw new Error("private URL detail") })
    render(<ForgotPasswordPage />)
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "reader@example.test" } })
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Send reset link" })) })
    expect(screen.getByRole("status")).toHaveTextContent("Check your inbox.")
    expect(screen.getByRole("status")).toHaveTextContent("If an account exists")
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
    expect(screen.queryByText(/private/)).not.toBeInTheDocument()
  })
})

describe("reset password", () => {
  it.each([
    ["", "new-passphrase", "new-passphrase", "Missing or invalid reset link"],
    ["token=synthetic-reset", "short", "short", "Password must be at least 8 characters"],
    ["token=synthetic-reset", "new-passphrase", "different-passphrase", "Passwords don't match"],
  ])("refuses invalid local input (%s / %s)", async (query, password, confirmation, message) => {
    navigation.query = query
    render(<ResetPasswordPage />)
    fillReset(password, confirmation)
    await submitReset()
    expect(screen.getByRole("alert")).toHaveTextContent(message)
    expect(serverFetch).not.toHaveBeenCalled()
    expect(navigation.router.push).not.toHaveBeenCalled()
  })
  it.each([
    ["server", "This link expired"], ["empty body", "Reset failed. The link may have expired."],
    ["null body", "Reset failed. The link may have expired."], ["network", "Network error. Please try again."],
  ])("keeps inputs and allows retry after %s", async (failure, message) => {
    if (failure === "network") vi.mocked(serverFetch).mockRejectedValueOnce(new Error("offline"))
    else if (failure === "empty body") vi.mocked(serverFetch).mockResolvedValueOnce(new Response(null, { status: 503 }))
    else vi.mocked(serverFetch).mockResolvedValueOnce(Response.json(failure === "server" ? { error: "This link expired" } : null, { status: 400 }))
    vi.mocked(serverFetch).mockResolvedValueOnce(Response.json({ ok: true }))
    render(<ResetPasswordPage />)
    fillReset()
    await submitReset()
    expect(screen.getByRole("alert")).toHaveTextContent(message)
    expect(screen.getByLabelText("New password")).toHaveValue("new-passphrase")
    expect(screen.getByRole("button", { name: "Update password" })).toBeEnabled()
    expect(navigation.router.push).not.toHaveBeenCalled()
    await submitReset()
    expect(screen.getByRole("status")).toHaveTextContent("Password updated.")
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
  })
  it.each([false, true])("redirects only a still-mounted successful reset (unmount=%s)", async (unmountBeforeRedirect) => {
    vi.useFakeTimers()
    let finish!: (response: Response) => void
    vi.mocked(serverFetch).mockReturnValue(new Promise(resolve => { finish = resolve }))
    const view = render(<ResetPasswordPage />)
    fillReset()
    await submitReset()
    expect(screen.getByRole("button", { name: "Updating..." })).toBeDisabled()
    expect(serverFetch).toHaveBeenCalledWith("/api/v1/auth/reset", expect.objectContaining({ method: "POST", body: JSON.stringify({ token: "synthetic-reset", new_password: "new-passphrase" }) }))
    await act(async () => { finish(Response.json({ ok: true })) })
    expect(screen.getByRole("status")).toHaveTextContent("All existing sessions have been signed out")
    if (unmountBeforeRedirect) view.unmount()
    act(() => { vi.advanceTimersByTime(1999) })
    expect(navigation.router.push).not.toHaveBeenCalled()
    act(() => { vi.advanceTimersByTime(1) })
    if (unmountBeforeRedirect) expect(navigation.router.push).not.toHaveBeenCalled()
    else expect(navigation.router.push).toHaveBeenCalledWith("/login")
  })
})
