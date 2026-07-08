import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"
import { ProfileSection } from "../sections/profile-section"

// #867.1 — the Profile tab is now editable: the full name saves via
// PATCH /api/v1/users/me and the password changes via
// POST /api/v1/users/me/password.

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

function ok(payload: unknown = {}) {
  return { ok: true, status: 200, json: async () => payload }
}

function renderProfile() {
  return render(
    <ProfileSection
      userName="Ada Lovelace"
      userEmail="ada@example.com"
      role="OWNER"
      workspaceName="Acme"
    />,
  )
}

describe("ProfileSection editing (#867.1)", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
    // Default: the tokens list fetch on mount resolves empty.
    apiFetch.mockResolvedValue(ok([]))
  })

  it("saves an edited full name via PATCH /users/me", async () => {
    renderProfile()
    fireEvent.click(screen.getByRole("button", { name: /^edit$/i }))
    const input = screen.getByLabelText(/full name/i) as HTMLInputElement
    fireEvent.change(input, { target: { value: "Ada B. Lovelace" } })

    apiFetch.mockResolvedValueOnce(ok({ id: "u1", email: "ada@example.com", full_name: "Ada B. Lovelace" }))
    fireEvent.click(screen.getByRole("button", { name: /^save$/i }))

    await waitFor(() => {
      const call = apiFetch.mock.calls.find((c) => c[0] === "/api/v1/users/me")
      expect(call).toBeTruthy()
      expect(call![1]).toMatchObject({ method: "PATCH" })
      expect(JSON.parse(call![1].body)).toEqual({ full_name: "Ada B. Lovelace" })
    })
    await waitFor(() => expect(screen.getByText("Ada B. Lovelace")).toBeTruthy())
  })

  it("shows a save error inline while staying in edit mode (#883 review)", async () => {
    renderProfile()
    fireEvent.click(screen.getByRole("button", { name: /^edit$/i }))
    fireEvent.change(screen.getByLabelText(/full name/i), { target: { value: "X" } })

    apiFetch.mockResolvedValueOnce({ ok: false, status: 400, json: async () => ({ error: "boom" }) })
    fireEvent.click(screen.getByRole("button", { name: /^save$/i }))

    // Error is visible AND the input is still rendered (edit mode intact).
    await waitFor(() => expect(screen.getByText("boom")).toBeTruthy())
    expect(screen.getByLabelText(/full name/i)).toBeTruthy()
  })

  it("posts a password change and confirms sessions were signed out", async () => {
    renderProfile()
    fireEvent.click(screen.getByRole("button", { name: /^change$/i }))

    fireEvent.change(screen.getByLabelText(/current password/i), { target: { value: "oldpassword1" } })
    fireEvent.change(screen.getByLabelText(/^new password$/i), { target: { value: "brandnew123" } })
    fireEvent.change(screen.getByLabelText(/confirm new password/i), { target: { value: "brandnew123" } })

    apiFetch.mockResolvedValueOnce(ok({ success: true, sessions_revoked: 2 }))
    fireEvent.click(screen.getByRole("button", { name: /change password/i }))

    await waitFor(() => {
      const call = apiFetch.mock.calls.find((c) => c[0] === "/api/v1/users/me/password")
      expect(call).toBeTruthy()
      expect(JSON.parse(call![1].body)).toEqual({ current_password: "oldpassword1", new_password: "brandnew123" })
    })
    await waitFor(() => expect(screen.getByText(/other sessions have been signed out/i)).toBeTruthy())
  })

  it("blocks a password change when the confirmation does not match", async () => {
    renderProfile()
    fireEvent.click(screen.getByRole("button", { name: /^change$/i }))
    fireEvent.change(screen.getByLabelText(/current password/i), { target: { value: "oldpassword1" } })
    fireEvent.change(screen.getByLabelText(/^new password$/i), { target: { value: "brandnew123" } })
    fireEvent.change(screen.getByLabelText(/confirm new password/i), { target: { value: "different99" } })

    fireEvent.click(screen.getByRole("button", { name: /change password/i }))

    await waitFor(() => expect(screen.getByText(/do not match/i)).toBeTruthy())
    // No password POST should have been issued.
    expect(apiFetch.mock.calls.some((c) => c[0] === "/api/v1/users/me/password")).toBe(false)
  })

  // #889 — avatar upload.
  it("uploads a selected image via multipart POST and renders it", async () => {
    renderProfile()
    const input = screen.getByLabelText(/upload profile picture/i) as HTMLInputElement

    const returnedUrl = "/api/v1/users/u1/avatar?v=42"
    apiFetch.mockResolvedValueOnce(ok({ id: "u1", email: "ada@example.com", avatar_url: returnedUrl }))

    const file = new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], "me.png", { type: "image/png" })
    fireEvent.change(input, { target: { files: [file] } })

    await waitFor(() => {
      const call = apiFetch.mock.calls.find((c) => c[0] === "/api/v1/users/me/avatar")
      expect(call).toBeTruthy()
      expect(call![1]).toMatchObject({ method: "POST" })
      expect(call![1].body).toBeInstanceOf(FormData)
    })
    // The returned URL is rendered as an <img>.
    await waitFor(() => {
      const img = screen.getByRole("img", { name: /your avatar/i }) as HTMLImageElement
      expect(img.getAttribute("src")).toBe(returnedUrl)
    })
  })

  it("rejects a non-image file client-side without calling the API", async () => {
    renderProfile()
    const input = screen.getByLabelText(/upload profile picture/i) as HTMLInputElement

    const file = new File(["hello"], "notes.txt", { type: "text/plain" })
    fireEvent.change(input, { target: { files: [file] } })

    await waitFor(() => expect(screen.getByText(/must be a png, jpeg, or webp/i)).toBeTruthy())
    expect(apiFetch.mock.calls.some((c) => c[0] === "/api/v1/users/me/avatar")).toBe(false)
  })

  it("clears the avatar via DELETE and falls back to initials", async () => {
    render(
      <ProfileSection
        userName="Ada Lovelace"
        userEmail="ada@example.com"
        userAvatarUrl="/api/v1/users/u1/avatar?v=1"
        role="OWNER"
        workspaceName="Acme"
      />,
    )
    expect(screen.getByRole("img", { name: /your avatar/i })).toBeTruthy()

    apiFetch.mockResolvedValueOnce(ok({ id: "u1", email: "ada@example.com", avatar_url: null }))
    fireEvent.click(screen.getByRole("button", { name: /^remove$/i }))

    await waitFor(() => {
      const call = apiFetch.mock.calls.find((c) => c[0] === "/api/v1/users/me/avatar")
      expect(call).toBeTruthy()
      expect(call![1]).toMatchObject({ method: "DELETE" })
    })
    // Image gone → initials shown.
    await waitFor(() => expect(screen.queryByRole("img", { name: /your avatar/i })).toBeNull())
  })
})
