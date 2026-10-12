import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

import { ProfileSection } from "../profile-section"
import { PageSaveBar, PageSaveProvider } from "@/components/ui/page-save-bar"

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))

/** As Settings renders it: inside the page's one floating Save bar. */
function renderInPage(ui: React.ReactElement) {
  return render(<PageSaveProvider>{ui}<PageSaveBar /></PageSaveProvider>)
}

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

// CLI tokens and sessions moved to SessionsSection (sessions-section.test.tsx).
// The stub still answers the tokens list so a stray fetch cannot hang a test.
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
    renderInPage(<ProfileSection userName="Ada Lovelace" userEmail="ada@example.com" />)

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

  it("the name is a plain field: no Save until edited, then the page bar commits it", async () => {
    mockApi()
    renderInPage(<ProfileSection userName="Ada Lovelace" userEmail="ada@example.com" />)

    const nameInput = screen.getByLabelText("Full name")
    expect(nameInput).toHaveValue("Ada Lovelace")
    expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull()

    fireEvent.change(nameInput, { target: { value: "Grace Hopper" } })
    expect(screen.getByRole("region", { name: "Unsaved changes" })).toHaveTextContent("1 unsaved change")
    fireEvent.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => {
      const patch = apiFetch.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === "PATCH")
      expect(JSON.parse(String((patch![1] as RequestInit).body))).toEqual({ full_name: "Grace Hopper" })
    })
    // Success is the bar saying so, not a toast; the field keeps the new name.
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Saved"))
    expect(toastSuccess).not.toHaveBeenCalled()
    expect(screen.getByLabelText("Full name")).toHaveValue("Grace Hopper")
  })

  it("on a rejected name save, toasts the server's reason in the corner and keeps what was typed", async () => {
    mockApi({ namePatchStatus: 500 })
    renderInPage(<ProfileSection userName="Ada Lovelace" userEmail="ada@example.com" />)

    fireEvent.change(screen.getByLabelText("Full name"), { target: { value: "Grace Hopper" } })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(toastError.mock.calls[0][0]).toBe("Couldn’t save Account")
    expect(toastError.mock.calls[0][1]).toMatchObject({ description: "name rejected Your edits are kept." })
    expect(toastSuccess).not.toHaveBeenCalled()
    expect(screen.getByLabelText("Full name")).toHaveValue("Grace Hopper")
    expect(screen.getByRole("region", { name: "Unsaved changes" })).toHaveTextContent("1 unsaved change")
  })

  it("blocks an empty name under the field, with Save disabled", () => {
    mockApi()
    renderInPage(<ProfileSection userName="Ada Lovelace" userEmail="ada@example.com" />)

    fireEvent.change(screen.getByLabelText("Full name"), { target: { value: "   " } })
    expect(screen.getByText("Name must be 1-100 characters")).toBeTruthy()
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled()
  })
})

describe("ProfileSection — sessions moved out", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
  })

  // Sessions & tokens is its own section now (sessions-section.tsx); Profile
  // must not keep a second copy of the card or its requests.
  it("renders no sessions card and asks for no tokens or sessions", async () => {
    mockApi()
    render(<ProfileSection userName="Ada Lovelace" userEmail="ada@example.com" />)
    await waitFor(() => expect(screen.getByRole("region", { name: "Account" })).toBeTruthy())
    expect(screen.queryByRole("region", { name: /Sessions/ })).toBeNull()
    expect(apiFetch.mock.calls.map((c) => String(c[0])).filter((u) => /auth\/(cli-tokens|sessions)/.test(u))).toEqual([])
  })
})

