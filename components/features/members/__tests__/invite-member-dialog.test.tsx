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

  // `created_user` and `setup_url` answer different questions, and this
  // copy used to key off the first while the link rendered off the second.
  // An existing-but-unclaimed account gets created_user=false AND a link,
  // so the paragraph announced "no setup link is issued" directly above the
  // setup link. Found on dev3 against the real server.
  it("does not deny the link it is showing, for an existing unclaimed account", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ setup_url: SETUP_URL, created_user: false, email: "old@example.com" }, 201))
    await submit("old@example.com")

    expect((await screen.findByLabelText("Setup link")).getAttribute("value")).toBe(SETUP_URL)
    expect(screen.queryByText(/no setup link is issued/i)).toBeNull()
    expect(screen.queryByText(/existing password/i)).toBeNull()
  })

  it("says an existing person was added, and issues nothing, when the account is claimed", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ created_user: false, email: "claimed@example.com" }, 201))
    await submit("claimed@example.com")

    expect(await screen.findByText(/already had an account|existing account/i)).toBeTruthy()
    expect(screen.queryByLabelText("Setup link")).toBeNull()
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

// A provisioned account has no name until the person sets one, so the
// roster showed them by email alone. The admin usually knows the name at
// the moment they add someone — asking then costs one optional field and
// saves a stranger-looking row.
describe("InviteMemberDialog — optional name", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
  })

  it("sends the name when one is given", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ setup_url: SETUP_URL, created_user: true, email: "ada@example.com" }, 201))
    render(<InviteMemberDialog workspaceId="ws1" onInvited={vi.fn()} />)
    fireEvent.click(screen.getByRole("button", { name: /add member/i }))
    fireEvent.change(await screen.findByLabelText(/email/i), { target: { value: "ada@example.com" } })
    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: "Ada Lovelace" } })
    fireEvent.click(screen.getByRole("button", { name: /^add member$/i }))

    await waitFor(() => {
      const body = JSON.parse(String(apiFetch.mock.calls.at(-1)?.[1]?.body))
      expect(body.full_name).toBe("Ada Lovelace")
    })
  })

  it("stays optional — an empty name is not sent as a blank string", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ setup_url: SETUP_URL, created_user: true, email: "x@example.com" }, 201))
    await submit("x@example.com")
    await waitFor(() => {
      const body = JSON.parse(String(apiFetch.mock.calls.at(-1)?.[1]?.body))
      // "" would be stored as a name and defeat the email fallback — the
      // exact bug this pair of changes fixes.
      expect(body.full_name ?? "").toBe("")
    })
  })
})

// The link never appeared on screen. Not a rendering bug — a lifecycle one:
// onInvited fired the instant provisioning succeeded, the settings layout
// set loading=true for the refetch, MembersSection was replaced by a
// skeleton, and the dialog inside it was unmounted with the link still in
// its state. It flashed and died.
//
// The roster can wait a few seconds. The link cannot: it is shown once.
describe("InviteMemberDialog — the link outlives the roster refresh", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
  })

  async function provision(onInvited: () => void) {
    apiFetch.mockResolvedValue(jsonResponse({ setup_url: SETUP_URL, created_user: true, email: "new@example.com" }, 201))
    render(<InviteMemberDialog workspaceId="ws1" onInvited={onInvited} />)
    fireEvent.click(screen.getByRole("button", { name: /add member/i }))
    fireEvent.change(await screen.findByLabelText(/email/i), { target: { value: "new@example.com" } })
    fireEvent.click(screen.getByRole("button", { name: /^add member$/i }))
    await screen.findByDisplayValue(SETUP_URL)
  }

  it("does not trigger the parent refresh while the link is on screen", async () => {
    const onInvited = vi.fn()
    await provision(onInvited)
    // Refreshing here unmounts this very dialog in the real layout.
    expect(onInvited).not.toHaveBeenCalled()
  })

  it("refreshes once the admin is done with the link", async () => {
    const onInvited = vi.fn()
    await provision(onInvited)
    fireEvent.click(screen.getByRole("button", { name: /^done$/i }))
    await waitFor(() => expect(onInvited).toHaveBeenCalled())
  })

  it("does NOT refresh on Add another — that unmounts the dialog just the same", async () => {
    const onInvited = vi.fn()
    await provision(onInvited)
    fireEvent.click(screen.getByRole("button", { name: /add another/i }))

    // Same trap as the success path: onInvited flips the layout to
    // loading, MembersSection is swapped for a skeleton, and the form the
    // admin just asked for is unmounted mid-keystroke.
    expect(onInvited).not.toHaveBeenCalled()
    expect(await screen.findByLabelText(/email/i)).toBeTruthy()
  })

  it("refreshes once at the end, after several additions", async () => {
    const onInvited = vi.fn()
    await provision(onInvited)
    fireEvent.click(screen.getByRole("button", { name: /add another/i }))
    await screen.findByLabelText(/email/i)
    fireEvent.click(screen.getByRole("button", { name: /cancel/i }))
    // One refresh for the whole session, on the way out.
    await waitFor(() => expect(onInvited).toHaveBeenCalledTimes(1))
  })
})

// Provisioning an email that already has a password withholds the setup
// link server-side — a token there would let whoever holds it change that
// person's password, and any user can self-serve a workspace to reach this
// endpoint. The dialog must not promise a link it will not get.
describe("InviteMemberDialog — existing account", () => {
  beforeEach(() => { cleanup(); apiFetch.mockReset() })

  it("shows no link when the server withholds one", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ created_user: false, email: "old@example.com" }, 201))
    await submit("old@example.com")
    expect(await screen.findByText(/credential they already have/i)).toBeTruthy()
    expect(screen.queryByLabelText("Setup link")).toBeNull()
    expect(screen.queryByRole("button", { name: /copy/i })).toBeNull()
  })
})
