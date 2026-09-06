// Sign in with a code (PRD provider-logins §10.3). Real timers throughout —
// the poll floor is a prop, so the whole state machine runs in milliseconds
// and `waitFor` sees every transition the way a browser would.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { DeviceSignIn } from "../device-sign-in"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))

function ok(body: unknown, status = 200) {
  return { ok: true, status, json: async () => body } as unknown as Response
}
function fail(status: number, body: unknown = {}) {
  return { ok: false, status, json: async () => body } as unknown as Response
}

const START = {
  device_id: "dev_1",
  user_code: "ABCD-EFGH",
  verification_url: "https://auth.openai.com/device",
  expires_at: new Date(Date.now() + 600_000).toISOString(),
  interval_s: 5,
}

/** Answers the start request with a code, then the polls in the given order. */
function script(polls: unknown[]) {
  const queue = [...polls]
  h.apiFetch.mockImplementation((url: unknown, init?: { method?: string }) => {
    const u = String(url)
    if (u.startsWith("/api/v1/provider-logins/device?") && init?.method === "POST") return Promise.resolve(ok(START))
    if (u.startsWith("/api/v1/provider-logins/device/dev_1")) return Promise.resolve(ok(queue.length > 1 ? queue.shift() : queue[0]))
    throw new Error(`unexpected ${u}`)
  })
}

function renderIt(over: Partial<React.ComponentProps<typeof DeviceSignIn>> = {}) {
  const onComplete = vi.fn()
  const onStateChange = vi.fn()
  render(
    <DeviceSignIn
      workspaceId="ws1"
      provider="OPENAI"
      mode="subscription"
      onComplete={onComplete}
      onStateChange={onStateChange}
      pollIntervalMs={5}
      {...over}
    />,
  )
  return { onComplete, onStateChange }
}

// Braces on purpose: mockReset returns the mock, and a function returned from
// beforeEach is a cleanup hook — which would call the mock with no arguments.
beforeEach(() => {
  h.apiFetch.mockReset()
})

describe("the flow", () => {
  it("asks for a code with the provider and mode, then shows it big with the page to open", async () => {
    script([{ status: "pending" }])
    renderIt()
    expect(screen.getByText(/asking openai for a code/i)).toBeInTheDocument()
    expect(await screen.findByTestId("device-user-code")).toHaveTextContent("ABCD-EFGH")
    expect(screen.getByRole("link", { name: /open auth\.openai\.com/i })).toHaveAttribute("href", START.verification_url)
    expect(screen.getByText(/waiting for you to approve/i)).toBeInTheDocument()

    const [url, init] = h.apiFetch.mock.calls[0]
    expect(String(url)).toBe("/api/v1/provider-logins/device?workspace_id=ws1")
    expect(JSON.parse(String((init as { body?: string }).body))).toEqual({ provider: "OPENAI", mode: "subscription" })
  })

  it("polls until complete and hands the credential id up once", async () => {
    script([{ status: "pending" }, { status: "pending" }, { status: "complete", credential_id: "cred_9" }])
    const { onComplete, onStateChange } = renderIt()
    await waitFor(() => expect(onComplete).toHaveBeenCalledWith("cred_9"))
    expect(screen.getByTestId("device-sign-in")).toHaveAttribute("data-phase", "complete")
    expect(screen.getByText(/signed in\. the login is saved with its refresh token sealed/i)).toBeInTheDocument()
    expect(onStateChange).toHaveBeenLastCalledWith("complete")
    // The poll went to the device the start named, every time.
    const polls = h.apiFetch.mock.calls.filter(([u]) => String(u).startsWith("/api/v1/provider-logins/device/dev_1"))
    expect(polls.length).toBe(3)
    expect(onComplete).toHaveBeenCalledTimes(1)
  })

  it("expired: says so and offers a new code, which starts the flow again", async () => {
    script([{ status: "expired" }])
    const { onComplete } = renderIt()
    await waitFor(() => expect(screen.getByTestId("device-sign-in")).toHaveAttribute("data-phase", "expired"))
    expect(screen.getByText(/the code expired before it was used/i)).toBeInTheDocument()
    expect(onComplete).not.toHaveBeenCalled()

    script([{ status: "pending" }])
    fireEvent.click(screen.getByRole("button", { name: /get a new code/i }))
    await waitFor(() => expect(screen.getByTestId("device-sign-in")).toHaveAttribute("data-phase", "pending"))
    const starts = h.apiFetch.mock.calls.filter(([u, i]) => String(u).startsWith("/api/v1/provider-logins/device?") && (i as { method?: string })?.method === "POST")
    expect(starts).toHaveLength(2) // the first code, then the one asked for
  })

  it("denied: says the provider refused and offers to try again", async () => {
    script([{ status: "denied" }])
    renderIt()
    await waitFor(() => expect(screen.getByTestId("device-sign-in")).toHaveAttribute("data-phase", "denied"))
    expect(screen.getByText(/reported the sign-in was denied/i)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /try again/i })).toBeInTheDocument()
  })

  it("a start the server refuses is an error with the server's words, not a spinner forever", async () => {
    h.apiFetch.mockResolvedValue(fail(501, { error: "device flow not available for GOOGLE" }))
    const { onStateChange } = renderIt({ provider: "GOOGLE" })
    expect(await screen.findByRole("alert")).toHaveTextContent("device flow not available for GOOGLE")
    expect(onStateChange).toHaveBeenLastCalledWith("error")
    expect(screen.queryByTestId("device-user-code")).not.toBeInTheDocument()
  })

  it("complete without a credential id is an error — the wizard must not proceed on nothing", async () => {
    script([{ status: "complete" }])
    const { onComplete } = renderIt()
    expect(await screen.findByRole("alert")).toHaveTextContent(/named no credential/i)
    expect(onComplete).not.toHaveBeenCalled()
  })
})
