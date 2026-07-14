import { describe, it, expect, vi, beforeEach, beforeAll } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { KeeperGovernancePanel } from "../keeper-governance-panel"

// Radix Select drives open/close through pointer-capture APIs happy-dom does
// not implement; polyfill them so the provider/credential menus can open.
beforeAll(() => {
  Element.prototype.scrollIntoView = vi.fn()
  // @ts-expect-error jsdom/happy-dom lacks these pointer-capture stubs
  Element.prototype.hasPointerCapture = vi.fn(() => false)
  // @ts-expect-error polyfill
  Element.prototype.setPointerCapture = vi.fn()
  // @ts-expect-error polyfill
  Element.prototype.releasePointerCapture = vi.fn()
})

// openSelect drives a Radix SelectTrigger open the way a pointer would.
function openSelect(trigger: HTMLElement) {
  fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false, pointerId: 1 })
  fireEvent.pointerUp(trigger, { button: 0, pointerId: 1 })
  fireEvent.click(trigger)
}

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

const CREDENTIALS = [
  { id: "cred-api", name: "Anthropic key", type: "API_KEY", status: "ACTIVE" },
  { id: "cred-url", name: "Ollama host", type: "ENDPOINT_URL", status: "ACTIVE" },
  // A SECRET must be filtered out of the gov-model credential picker.
  { id: "cred-secret", name: "DB password", type: "SECRET", status: "ACTIVE" },
]

function mockRoutes(gov: {
  configured: boolean
  enabled: boolean
  security_contact_user_id: string
  deny_notify_min_risk: number
  watch_spec?: string
  watch_presets?: string[]
  gov_model_provider?: string
  gov_model_id?: string
  gov_model_credential_id?: string
}) {
  apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.includes("/admin/keeper/governance")) {
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body)) as Record<string, unknown>
        return jsonResponse({ configured: true, ...body })
      }
      return jsonResponse(gov)
    }
    if (url.includes("/credentials")) return jsonResponse(CREDENTIALS)
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
      watch_spec: "",
      watch_presets: [],
      gov_model_provider: "",
      gov_model_id: "",
      gov_model_credential_id: "",
    })
    // Baseline resets after a successful save → Save disabled again.
    await waitFor(() =>
      expect(screen.getByTestId("keeper-governance-save")).toBeDisabled(),
    )
  })

  it("hydrates the watch spec + presets from GET (#1001 M1)", async () => {
    mockRoutes({
      configured: true,
      enabled: true,
      security_contact_user_id: "",
      deny_notify_min_risk: 7,
      watch_spec: "flag any read of ~/.ssh",
      watch_presets: ["credentials", "egress"],
    })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    const spec = await screen.findByTestId("keeper-watch-spec")
    expect(spec).toHaveValue("flag any read of ~/.ssh")
    expect(screen.getByTestId("keeper-watch-preset-credentials")).toHaveAttribute("aria-checked", "true")
    expect(screen.getByTestId("keeper-watch-preset-egress")).toHaveAttribute("aria-checked", "true")
    expect(screen.getByTestId("keeper-watch-preset-memory")).toHaveAttribute("aria-checked", "false")
    // Pristine → Save disabled.
    expect(screen.getByTestId("keeper-governance-save")).toBeDisabled()
  })

  it("saves an edited watch spec + toggled preset via PUT (#1001 M1)", async () => {
    mockRoutes({
      configured: true,
      enabled: true,
      security_contact_user_id: "",
      deny_notify_min_risk: 7,
      watch_spec: "",
      watch_presets: [],
    })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    const spec = await screen.findByTestId("keeper-watch-spec")
    fireEvent.change(spec, { target: { value: "flag egress to non-allowlisted hosts" } })
    fireEvent.click(screen.getByTestId("keeper-watch-preset-destructive"))

    const save = screen.getByTestId("keeper-governance-save")
    expect(save).toBeEnabled()
    fireEvent.click(save)

    await waitFor(() => expect(toastSuccess).toHaveBeenCalled())
    const putCall = apiFetch.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT")
    const [, putInit] = putCall as [string, RequestInit]
    const body = JSON.parse(String(putInit.body)) as { watch_spec: string; watch_presets: string[] }
    expect(body.watch_spec).toBe("flag egress to non-allowlisted hosts")
    expect(body.watch_presets).toEqual(["destructive"])
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

  it("renders the four governance-model provider options (#1001 gov-model)", async () => {
    mockRoutes({
      configured: true,
      enabled: true,
      security_contact_user_id: "",
      deny_notify_min_risk: 7,
    })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    const trigger = await screen.findByTestId("keeper-gov-provider")
    // Defaults to the server-default option, and no model input until a
    // concrete provider is chosen.
    expect(trigger).toHaveTextContent(/server default/i)
    expect(screen.queryByTestId("keeper-gov-model-id")).not.toBeInTheDocument()

    openSelect(trigger)
    for (const label of [
      /server default/i,
      /ollama \(local\)/i,
      /anthropic/i,
      /openai-compatible/i,
    ]) {
      expect(await screen.findByRole("option", { name: label })).toBeInTheDocument()
    }
  })

  it("blocks save when a provider is set but the model id is empty (#1001 gov-model)", async () => {
    mockRoutes({
      configured: true,
      enabled: true,
      security_contact_user_id: "",
      deny_notify_min_risk: 7,
    })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    const trigger = await screen.findByTestId("keeper-gov-provider")
    openSelect(trigger)
    fireEvent.click(await screen.findByRole("option", { name: /anthropic/i }))

    // Model input now shown, empty → required message + Save disabled.
    const modelInput = await screen.findByTestId("keeper-gov-model-id")
    expect(modelInput).toHaveValue("")
    expect(screen.getByTestId("keeper-gov-model-required")).toBeInTheDocument()
    expect(screen.getByTestId("keeper-governance-save")).toBeDisabled()

    // A save attempt (via the guard) never reaches the PUT.
    fireEvent.change(modelInput, { target: { value: "   " } })
    expect(screen.getByTestId("keeper-governance-save")).toBeDisabled()
    expect(
      apiFetch.mock.calls.some(([, init]) => (init as RequestInit)?.method === "PUT"),
    ).toBe(false)
  })

  it("saves the governance-model fields via PUT (#1001 gov-model)", async () => {
    mockRoutes({
      configured: true,
      enabled: true,
      security_contact_user_id: "",
      deny_notify_min_risk: 7,
    })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    const trigger = await screen.findByTestId("keeper-gov-provider")
    openSelect(trigger)
    fireEvent.click(await screen.findByRole("option", { name: /anthropic/i }))

    fireEvent.change(await screen.findByTestId("keeper-gov-model-id"), {
      target: { value: "claude-haiku-4-5" },
    })

    // The credential picker is filtered to API_KEY / ENDPOINT_URL (no SECRET).
    const credTrigger = screen.getByTestId("keeper-gov-credential")
    openSelect(credTrigger)
    expect(await screen.findByRole("option", { name: /anthropic key \(API_KEY\)/i })).toBeInTheDocument()
    expect(screen.getByRole("option", { name: /ollama host \(ENDPOINT_URL\)/i })).toBeInTheDocument()
    expect(screen.queryByRole("option", { name: /db password/i })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("option", { name: /anthropic key \(API_KEY\)/i }))

    const save = screen.getByTestId("keeper-governance-save")
    expect(save).toBeEnabled()
    fireEvent.click(save)

    await waitFor(() => expect(toastSuccess).toHaveBeenCalled())
    const putCall = apiFetch.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT")
    const [, putInit] = putCall as [string, RequestInit]
    const body = JSON.parse(String(putInit.body)) as {
      gov_model_provider: string
      gov_model_id: string
      gov_model_credential_id: string
    }
    expect(body.gov_model_provider).toBe("anthropic")
    expect(body.gov_model_id).toBe("claude-haiku-4-5")
    expect(body.gov_model_credential_id).toBe("cred-api")
  })

  it("hydrates the governance-model fields from GET (#1001 gov-model)", async () => {
    mockRoutes({
      configured: true,
      enabled: true,
      security_contact_user_id: "",
      deny_notify_min_risk: 7,
      gov_model_provider: "ollama",
      gov_model_id: "qwen2.5:3b-instruct",
      gov_model_credential_id: "cred-url",
    })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    expect(await screen.findByTestId("keeper-gov-model-id")).toHaveValue("qwen2.5:3b-instruct")
    expect(screen.getByTestId("keeper-gov-provider")).toHaveTextContent(/ollama/i)
    // Pristine hydration → Save disabled.
    expect(screen.getByTestId("keeper-governance-save")).toBeDisabled()
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
