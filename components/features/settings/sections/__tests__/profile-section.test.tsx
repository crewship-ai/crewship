import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

import { ProfileSection } from "../profile-section"

// Same pattern as privacy-section.test.tsx / privileged-credentials-card.test.tsx:
// drive the component through its real fetch path with a stubbed apiFetch.
const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetch(...args),
}))

// The avatar and name live in the global session too (top bar). Writing them
// without re-pulling it is what made an upload look like it had done nothing
// everywhere except the one row in Settings.
const refresh = vi.fn().mockResolvedValue(undefined)
vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({ refresh }),
}))

const toastSuccess = vi.fn()
const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: {
    success: (...args: unknown[]) => toastSuccess(...args),
    error: (...args: unknown[]) => toastError(...args),
  },
}))

function jsonResponse(body: unknown, status = 200) {
  return { ok: status < 400, status, json: async () => body }
}

// The component is ~1000 lines with unrelated CLI-token surface. It fetches
// /api/v1/auth/cli-tokens on mount (SettingsCard "CLI Tokens") regardless of
// what we're testing, so the stub always answers it with an empty list —
// otherwise fetchTokens's promise never resolves and React logs act() noise.
function mockApi({
  avatarPostStatus = 200,
  avatarDeleteStatus = 200,
  namePatchStatus = 200,
}: {
  avatarPostStatus?: number
  avatarDeleteStatus?: number
  namePatchStatus?: number
} = {}) {
  apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.includes("/api/v1/auth/cli-tokens")) {
      return jsonResponse({ data: [] })
    }
    if (url.includes("/api/v1/users/me/avatar")) {
      if (init?.method === "DELETE") {
        if (avatarDeleteStatus >= 400) return jsonResponse({ error: "could not remove" }, avatarDeleteStatus)
        return jsonResponse({})
      }
      // POST upload
      if (avatarPostStatus >= 400) return jsonResponse({ error: "upload rejected" }, avatarPostStatus)
      return jsonResponse({ avatar_url: "https://example.com/avatar.png" })
    }
    if (url.includes("/api/v1/users/me") && init?.method === "PATCH") {
      if (namePatchStatus >= 400) return jsonResponse({ error: "name rejected" }, namePatchStatus)
      const body = JSON.parse(String(init.body)) as { full_name: string }
      return jsonResponse({ full_name: body.full_name })
    }
    throw new Error(`unexpected fetch: ${url} ${init?.method ?? "GET"}`)
  })
}

function pngFile(name = "avatar.png") {
  return new File(["fake-image-bytes"], name, { type: "image/png" })
}

describe("ProfileSection", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
    toastSuccess.mockReset()
    toastError.mockReset()
  })

  it("toasts a confirmation on successful avatar upload", async () => {
    mockApi()
    render(<ProfileSection userName="Ada Lovelace" userEmail="ada@example.com" />)

    const input = screen.getByLabelText("Upload profile picture") as HTMLInputElement
    fireEvent.change(input, { target: { files: [pngFile()] } })

    await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith(expect.stringMatching(/profile picture/i)))
  })

  it("re-pulls the session after an upload so the top bar shows the new picture", async () => {
    mockApi()
    render(<ProfileSection userName="Ada Lovelace" userEmail="ada@example.com" />)

    fireEvent.change(screen.getByLabelText("Upload profile picture") as HTMLInputElement, {
      target: { files: [pngFile()] },
    })

    await waitFor(() => expect(refresh).toHaveBeenCalled())
  })

  it("does not re-pull the session when the upload failed", async () => {
    mockApi({ avatarPostStatus: 400 })
    render(<ProfileSection userName="Ada Lovelace" userEmail="ada@example.com" />)

    fireEvent.change(screen.getByLabelText("Upload profile picture") as HTMLInputElement, {
      target: { files: [pngFile()] },
    })

    await waitFor(() => expect(screen.getByText("upload rejected")).toBeTruthy())
    expect(refresh).not.toHaveBeenCalled()
  })

  it("re-pulls the session after a name change so the top bar renames too", async () => {
    mockApi()
    render(<ProfileSection userName="Ada Lovelace" userEmail="ada@example.com" />)

    fireEvent.click(screen.getByRole("button", { name: /edit/i }))
    fireEvent.change(screen.getByDisplayValue("Ada Lovelace"), { target: { value: "Ada L" } })
    fireEvent.click(screen.getByRole("button", { name: /^save$/i }))

    await waitFor(() => expect(refresh).toHaveBeenCalled())
  })

  it("on a rejected upload, does not toast success and keeps the inline error", async () => {
    mockApi({ avatarPostStatus: 400 })
    render(<ProfileSection userName="Ada Lovelace" userEmail="ada@example.com" />)

    const input = screen.getByLabelText("Upload profile picture") as HTMLInputElement
    fireEvent.change(input, { target: { files: [pngFile()] } })

    await waitFor(() => expect(screen.getByText("upload rejected")).toBeTruthy())
    expect(toastSuccess).not.toHaveBeenCalled()
  })

  it("toasts a confirmation when removing the avatar", async () => {
    mockApi()
    render(<ProfileSection userName="Ada Lovelace" userEmail="ada@example.com" userAvatarUrl="https://example.com/old.png" />)

    fireEvent.click(screen.getByRole("button", { name: /remove/i }))

    await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith(expect.stringMatching(/profile picture/i)))
  })

  it("toasts a confirmation when the full-name save succeeds", async () => {
    mockApi()
    render(<ProfileSection userName="Ada Lovelace" userEmail="ada@example.com" />)

    fireEvent.click(screen.getByRole("button", { name: "Edit" }))
    const nameInput = screen.getByLabelText("Full name")
    fireEvent.change(nameInput, { target: { value: "Grace Hopper" } })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith(expect.stringMatching(/name/i)))
    // Editor closes on success, same as before this change.
    expect(screen.queryByLabelText("Full name")).toBeNull()
  })

  it("on a rejected name save, does not toast success and keeps the inline error with the editor open", async () => {
    mockApi({ namePatchStatus: 500 })
    render(<ProfileSection userName="Ada Lovelace" userEmail="ada@example.com" />)

    fireEvent.click(screen.getByRole("button", { name: "Edit" }))
    const nameInput = screen.getByLabelText("Full name")
    fireEvent.change(nameInput, { target: { value: "Grace Hopper" } })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => expect(screen.getByText("name rejected")).toBeTruthy())
    expect(toastSuccess).not.toHaveBeenCalled()
    expect(screen.getByLabelText("Full name")).toBeTruthy()
  })
})
