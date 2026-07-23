import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { PrivilegedCredentialsCard } from "../privileged-credentials-card"

// Drive the component through its real fetch path with a stubbed apiFetch
// (same pattern as keeper-governance-panel.test.tsx) so we exercise the actual
// GET → toggle → PATCH round-trip against the workspaces.go contract.
const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetch(...args),
}))

// CASL abilities come from useWorkspace/session plumbing we don't want in a
// unit test — stub the hook and steer edit rights per test via `canManage`.
let canManage = true
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({
    abilities: { can: () => canManage },
    role: canManage ? "OWNER" : "MEMBER",
    loading: false,
  }),
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

function mockWorkspace(allow: boolean, putStatus = 200) {
  apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.includes("/api/v1/workspaces/")) {
      if (init?.method === "PATCH") {
        const body = JSON.parse(String(init.body)) as Record<string, unknown>
        if (putStatus >= 400) return jsonResponse({ error: "nope" }, putStatus)
        return jsonResponse({ allow_privileged_credentials: body.allow_privileged_credentials })
      }
      return jsonResponse({ allow_privileged_credentials: allow })
    }
    throw new Error(`unexpected fetch: ${url}`)
  })
}

describe("PrivilegedCredentialsCard (#1378)", () => {
  beforeEach(() => {
    apiFetch.mockReset()
    toastSuccess.mockReset()
    toastError.mockReset()
    canManage = true
  })

  it("hydrates the switch OFF (fail-closed default) from GET", async () => {
    mockWorkspace(false)
    render(<PrivilegedCredentialsCard workspaceId="ws1" />)

    const sw = await screen.findByTestId("privileged-credentials-switch")
    expect(sw).toHaveAttribute("aria-checked", "false")
    const [getUrl] = apiFetch.mock.calls[0] as [string]
    expect(getUrl).toContain("/api/v1/workspaces/ws1")
  })

  it("hydrates the switch ON when the workspace has opted in", async () => {
    mockWorkspace(true)
    render(<PrivilegedCredentialsCard workspaceId="ws1" />)

    expect(await screen.findByTestId("privileged-credentials-switch")).toHaveAttribute(
      "aria-checked",
      "true",
    )
  })

  it("PATCHes allow_privileged_credentials=true when toggled on", async () => {
    mockWorkspace(false)
    render(<PrivilegedCredentialsCard workspaceId="ws1" />)

    const sw = await screen.findByTestId("privileged-credentials-switch")
    fireEvent.click(sw)

    await waitFor(() => expect(toastSuccess).toHaveBeenCalled())
    const putCall = apiFetch.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PATCH")
    expect(putCall).toBeTruthy()
    const [putUrl, putInit] = putCall as [string, RequestInit]
    expect(putUrl).toContain("/api/v1/workspaces/ws1")
    expect(JSON.parse(String(putInit.body))).toEqual({ allow_privileged_credentials: true })
    // Optimistic + server-confirmed → switch stays on.
    expect(sw).toHaveAttribute("aria-checked", "true")
  })

  it("rolls the switch back and toasts an error when the PATCH fails", async () => {
    mockWorkspace(false, 403)
    render(<PrivilegedCredentialsCard workspaceId="ws1" />)

    const sw = await screen.findByTestId("privileged-credentials-switch")
    fireEvent.click(sw)

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(sw).toHaveAttribute("aria-checked", "false")
  })

  it("disables the switch for a non-admin caller", async () => {
    canManage = false
    mockWorkspace(true)
    render(<PrivilegedCredentialsCard workspaceId="ws1" />)

    expect(await screen.findByTestId("privileged-credentials-switch")).toBeDisabled()
  })
})
