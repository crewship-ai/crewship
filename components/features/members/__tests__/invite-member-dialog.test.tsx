import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

import { InviteMemberDialog } from "../invite-member-dialog"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

const toastSuccess = vi.fn()
const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: { success: (...a: unknown[]) => toastSuccess(...a), error: (...a: unknown[]) => toastError(...a) },
}))

function jsonResponse(body: unknown, status = 200) {
  return { ok: status < 400, status, json: async () => body }
}

let writeText: ReturnType<typeof vi.fn>

const SETUP_URL = "https://crewship.example.com/reset-password?token=abc123"

async function submit(email = "new@example.com") {
  render(<InviteMemberDialog workspaceId="ws1" onInvited={vi.fn()} />)
  fireEvent.click(screen.getByRole("button", { name: /add member|invite/i }))
  fireEvent.change(await screen.findByLabelText(/email/i), { target: { value: email } })
  fireEvent.click(screen.getByRole("button", { name: /^(add|create|send)/i }))
}

describe("InviteMemberDialog", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
    toastSuccess.mockReset()
    toastError.mockReset()
    // navigator.clipboard is getter-only in happy-dom, so it has to be
    // redefined rather than assigned.
    writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText }, configurable: true, writable: true,
    })
  })

  it("provisions the account instead of writing an invitation nobody delivers", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ setup_url: SETUP_URL, created_user: true, email: "new@example.com" }, 201))
    await submit()
    // The old endpoint wrote a workspace_invitations row and mailed nothing.
    await waitFor(() =>
      expect(apiFetch).toHaveBeenCalledWith(
        expect.stringContaining("/members/provision"),
        expect.objectContaining({ method: "POST" }),
      ),
    )
  })

  it("shows the setup link, because it is the only way the person hears about the account", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ setup_url: SETUP_URL, created_user: true, email: "new@example.com" }, 201))
    await submit()
    expect(await screen.findByDisplayValue(SETUP_URL)).toBeTruthy()
  })

  it("does NOT close itself after success", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ setup_url: SETUP_URL, created_user: true, email: "new@example.com" }, 201))
    await submit()
    await screen.findByDisplayValue(SETUP_URL)

    // The old dialog auto-closed after 1500ms. With a link on screen that
    // would destroy the only copy of it — the token is hashed server-side
    // and cannot be shown again.
    await new Promise((r) => setTimeout(r, 1800))
    expect(screen.getByDisplayValue(SETUP_URL)).toBeTruthy()
  })

  it("copies the link to the clipboard", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ setup_url: SETUP_URL, created_user: true, email: "new@example.com" }, 201))
    await submit()
    await screen.findByDisplayValue(SETUP_URL)

    fireEvent.click(screen.getByRole("button", { name: /copy/i }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(SETUP_URL))
  })

  it("says when an existing person was added rather than a new account created", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ setup_url: SETUP_URL, created_user: false, email: "old@example.com" }, 201))
    await submit("old@example.com")
    // They already have a password; sending them a setup link would be
    // confusing at best.
    expect(await screen.findByText(/already had an account|existing account/i)).toBeTruthy()
  })

  it("surfaces the server's reason for a refusal", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ error: "that person is already a member of this workspace" }, 409))
    await submit()
    expect(await screen.findByText(/already a member/i)).toBeTruthy()
  })

  it("explains a missing public URL instead of a generic failure", async () => {
    apiFetch.mockResolvedValue(
      jsonResponse({ error: "CREWSHIP_PUBLIC_URL is not configured, so no setup link can be produced" }, 503),
    )
    await submit()
    expect(await screen.findByText(/CREWSHIP_PUBLIC_URL/i)).toBeTruthy()
  })
})
