import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { KeeperGovernancePanel } from "../keeper-governance-panel"

// Drive the component through its real fetch path with a stubbed apiFetch
// (same pattern as aux-status-section.test.tsx) so we exercise the actual
// GET → edit → PUT flow against the keeper_governance.go contract.
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

const MEMBERS = [
  {
    id: "m1",
    user_id: "u-owner",
    role: "OWNER",
    user: { id: "u-owner", email: "owner@x.dev", full_name: "Olga Owner", avatar_url: null },
  },
  {
    id: "m2",
    user_id: "u-member",
    role: "MEMBER",
    user: { id: "u-member", email: "member@x.dev", full_name: "Mem Ber", avatar_url: null },
  },
]

function mockRoutes(gov: {
  configured: boolean
  enabled: boolean
  security_contact_user_id: string
  deny_notify_min_risk: number
}) {
  apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.includes("/admin/keeper/governance")) {
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body)) as {
          enabled: boolean
          security_contact_user_id: string
          deny_notify_min_risk: number
        }
        return jsonResponse({ configured: true, ...body })
      }
      return jsonResponse(gov)
    }
    if (url.includes("/members")) return jsonResponse(MEMBERS)
    throw new Error(`unexpected fetch: ${url}`)
  })
}

describe("KeeperGovernancePanel (#1001 M0)", () => {
  beforeEach(() => {
    apiFetch.mockReset()
    toastSuccess.mockReset()
    toastError.mockReset()
    canManage = true
  })

  it("renders the switch off (opt-in default) when unconfigured, regardless of server engine", async () => {
    mockRoutes({
      configured: false,
      enabled: false,
      security_contact_user_id: "",
      deny_notify_min_risk: 7,
    })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    const sw = await screen.findByTestId("keeper-governance-switch")
    // Opt-in, default OFF: an unconfigured workspace shows the switch off even
    // though the server engine is on (the engine is shown only as context).
    expect(sw).toHaveAttribute("aria-checked", "false")
    expect(screen.getByText(/off by default \(opt-in\)/i)).toBeInTheDocument()
    expect(screen.getByText(/server engine is on/i)).toBeInTheDocument()
    expect(screen.getByTestId("keeper-governance-risk")).toHaveValue(7)
    // Pristine form → Save disabled.
    expect(screen.getByTestId("keeper-governance-save")).toBeDisabled()
  })

  it("saves the edited settings via PUT and reports success", async () => {
    mockRoutes({
      configured: true,
      enabled: false,
      security_contact_user_id: "u-owner",
      deny_notify_min_risk: 7,
    })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={false} />)

    const sw = await screen.findByTestId("keeper-governance-switch")
    fireEvent.click(sw)
    const risk = screen.getByTestId("keeper-governance-risk")
    fireEvent.change(risk, { target: { value: "9" } })

    const save = screen.getByTestId("keeper-governance-save")
    expect(save).toBeEnabled()
    fireEvent.click(save)

    await waitFor(() => expect(toastSuccess).toHaveBeenCalled())
    const putCall = apiFetch.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT")
    expect(putCall).toBeTruthy()
    const [putUrl, putInit] = putCall as [string, RequestInit]
    expect(putUrl).toContain("/api/v1/admin/keeper/governance?workspace_id=ws1")
    expect(JSON.parse(String(putInit.body))).toEqual({
      enabled: true,
      security_contact_user_id: "u-owner",
      deny_notify_min_risk: 9,
    })
    // Baseline resets after a successful save → Save disabled again.
    await waitFor(() =>
      expect(screen.getByTestId("keeper-governance-save")).toBeDisabled(),
    )
  })

  it("rejects an out-of-range risk threshold client-side", async () => {
    mockRoutes({
      configured: true,
      enabled: true,
      security_contact_user_id: "",
      deny_notify_min_risk: 7,
    })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    const risk = await screen.findByTestId("keeper-governance-risk")
    fireEvent.change(risk, { target: { value: "11" } })
    fireEvent.click(screen.getByTestId("keeper-governance-save"))

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(
      apiFetch.mock.calls.some(([, init]) => (init as RequestInit)?.method === "PUT"),
    ).toBe(false)
  })

  it("disables editing and hides Save for non-managers", async () => {
    canManage = false
    mockRoutes({
      configured: true,
      enabled: true,
      security_contact_user_id: "",
      deny_notify_min_risk: 7,
    })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    const sw = await screen.findByTestId("keeper-governance-switch")
    expect(sw).toBeDisabled()
    expect(screen.getByTestId("keeper-governance-risk")).toBeDisabled()
    expect(screen.queryByTestId("keeper-governance-save")).not.toBeInTheDocument()
  })

  it("surfaces a load failure with a retry affordance", async () => {
    apiFetch.mockImplementation(async (url: string) => {
      if (url.includes("/admin/keeper/governance")) return jsonResponse({ error: "nope" }, 500)
      return jsonResponse(MEMBERS)
    })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={false} />)

    expect(
      await screen.findByText(/failed to load governance settings/i),
    ).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /retry/i })).toBeInTheDocument()
  })
})
