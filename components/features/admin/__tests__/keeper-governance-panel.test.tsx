import { describe, it, expect, vi, beforeEach, beforeAll } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { KeeperGovernancePanel } from "../keeper-governance-panel"

// The panel is four cards over ONE partial-update endpoint (#1001 M0), so the
// properties worth pinning are: each card hydrates from the shared GET, each
// card's Save sends ONLY its own fields, and a card with nothing edited shows no
// Save at all (SaveFooter earns its space by appearing).

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
const toastWarning = vi.fn()
vi.mock("sonner", () => ({
  toast: {
    success: (...args: unknown[]) => toastSuccess(...args),
    error: (...args: unknown[]) => toastError(...args),
    warning: (...args: unknown[]) => toastWarning(...args),
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

interface Gov {
  configured: boolean
  enabled: boolean
  security_contact_user_id: string
  deny_notify_min_risk: number
  require_second_approver?: boolean
  auto_lease_seconds?: number
  watch_spec?: string
  watch_presets?: string[]
  gov_model_provider?: string
  gov_model_id?: string
  gov_model_credential_id?: string
  warning?: string
}

/**
 * mockRoutes models the real endpoint's partial-update semantics: a PUT applies
 * the fields it carries and returns the WHOLE row. Faithfulness matters here —
 * a mock that echoed only the submitted fields would let every card re-baseline
 * onto undefined after any save, hiding exactly the bug this shape introduces.
 */
function mockRoutes(gov: Gov, opts: { putWarning?: string } = {}) {
  const current: Gov = { ...gov }
  apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.includes("/admin/keeper/governance")) {
      if (init?.method === "PUT") {
        Object.assign(current, JSON.parse(String(init.body)) as Partial<Gov>)
        current.configured = true
        return jsonResponse({ ...current, warning: opts.putWarning })
      }
      return jsonResponse(current)
    }
    if (url.includes("/credentials")) return jsonResponse(CREDENTIALS)
    if (url.includes("/members")) return jsonResponse(MEMBERS)
    throw new Error(`unexpected fetch: ${url}`)
  })
}

function putBodies(): Record<string, unknown>[] {
  return apiFetch.mock.calls
    .filter(([, init]) => (init as RequestInit)?.method === "PUT")
    .map(([, init]) => JSON.parse(String((init as RequestInit).body)) as Record<string, unknown>)
}

const BASE: Gov = {
  configured: true,
  enabled: true,
  security_contact_user_id: "",
  deny_notify_min_risk: 7,
}

describe("KeeperGovernancePanel (#1001 M0)", () => {
  beforeEach(() => {
    apiFetch.mockReset()
    toastSuccess.mockReset()
    toastError.mockReset()
    toastWarning.mockReset()
    canManage = true
  })

  // ── Watchdog card ────────────────────────────────────────────────────────

  it("renders the switch off (opt-in default) when unconfigured, regardless of server engine", async () => {
    mockRoutes({ ...BASE, configured: false, enabled: false })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    const sw = await screen.findByTestId("keeper-governance-switch")
    // Opt-in, default OFF: an unconfigured workspace shows the switch off even
    // though the server engine is on (the engine is shown only as context).
    expect(sw).toHaveAttribute("aria-checked", "false")
    expect(screen.getByText(/off by default \(opt-in\)/i)).toBeInTheDocument()
    expect(screen.getByText(/server engine is on/i)).toBeInTheDocument()
    expect(screen.getByTestId("keeper-governance-risk")).toHaveValue(7)
    // Nothing edited → no card offers a Save.
    for (const id of ["keeper-watchdog-save", "keeper-findings-save", "keeper-leases-save", "keeper-gov-model-save"]) {
      expect(screen.queryByTestId(id)).not.toBeInTheDocument()
    }
  })

  it("hydrates the watch spec + presets from GET (#1001 M1)", async () => {
    mockRoutes({ ...BASE, watch_spec: "flag any read of ~/.ssh", watch_presets: ["credentials", "egress"] })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    const spec = await screen.findByTestId("keeper-watch-spec")
    expect(spec).toHaveValue("flag any read of ~/.ssh")
    expect(screen.getByTestId("keeper-watch-preset-credentials")).toHaveAttribute("aria-checked", "true")
    expect(screen.getByTestId("keeper-watch-preset-egress")).toHaveAttribute("aria-checked", "true")
    expect(screen.getByTestId("keeper-watch-preset-memory")).toHaveAttribute("aria-checked", "false")
    expect(screen.queryByTestId("keeper-watchdog-save")).not.toBeInTheDocument()
  })

  it("saves the watchdog card with only its own fields", async () => {
    mockRoutes({ ...BASE, enabled: false, watch_spec: "", watch_presets: [], auto_lease_seconds: 900 })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={false} />)

    fireEvent.click(await screen.findByTestId("keeper-governance-switch"))
    fireEvent.change(screen.getByTestId("keeper-watch-spec"), {
      target: { value: "flag egress to non-allowlisted hosts" },
    })
    fireEvent.click(screen.getByTestId("keeper-watch-preset-destructive"))

    const save = screen.getByTestId("keeper-watchdog-save")
    expect(save).toBeEnabled()
    fireEvent.click(save)

    await waitFor(() => expect(putBodies()).toHaveLength(1))
    const [body] = putBodies()
    expect(body).toEqual({
      enabled: true,
      watch_spec: "flag egress to non-allowlisted hosts",
      watch_presets: ["destructive"],
    })
    // Nothing else travels with it — in particular not the lease TTL or the
    // governance model, which a single-Save panel resent on every edit.
    expect(body).not.toHaveProperty("auto_lease_seconds")
    expect(body).not.toHaveProperty("gov_model_provider")
    expect(body).not.toHaveProperty("deny_notify_min_risk")

    // Committed → the footer collapses back once it has confirmed.
    expect(await screen.findByText(/^saved$/i)).toBeInTheDocument()
  })

  it("toggling a preset back off leaves the card clean", async () => {
    mockRoutes({ ...BASE, watch_presets: ["credentials"] })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    const preset = await screen.findByTestId("keeper-watch-preset-egress")
    fireEvent.click(preset)
    expect(screen.getByTestId("keeper-watchdog-save")).toBeInTheDocument()
    fireEvent.click(preset)
    // Same set, different array — the draft must compare by content, or the
    // footer would sit there offering to save nothing.
    expect(screen.queryByTestId("keeper-watchdog-save")).not.toBeInTheDocument()
  })

  // ── Findings & routing card ──────────────────────────────────────────────

  it("saves the risk threshold and second-approver flag together, alone", async () => {
    mockRoutes({ ...BASE, security_contact_user_id: "u-owner", require_second_approver: false })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={false} />)

    fireEvent.change(await screen.findByTestId("keeper-governance-risk"), { target: { value: "9" } })
    fireEvent.click(screen.getByTestId("keeper-governance-second-approver"))
    fireEvent.click(screen.getByTestId("keeper-findings-save"))

    await waitFor(() => expect(putBodies()).toHaveLength(1))
    expect(putBodies()[0]).toEqual({
      security_contact_user_id: "u-owner",
      deny_notify_min_risk: 9,
      require_second_approver: true,
    })
    const putUrl = apiFetch.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT")?.[0]
    expect(putUrl).toContain("/api/v1/admin/keeper/governance?workspace_id=ws1")
  })

  it("blocks an out-of-range risk threshold client-side and never PUTs it", async () => {
    mockRoutes(BASE)
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    fireEvent.change(await screen.findByTestId("keeper-governance-risk"), { target: { value: "11" } })
    expect(screen.getByTestId("keeper-governance-risk-invalid")).toBeInTheDocument()

    const save = screen.getByTestId("keeper-findings-save")
    expect(save).toBeDisabled()
    fireEvent.click(save)
    expect(putBodies()).toHaveLength(0)
  })

  it("hydrates require_second_approver ON from GET", async () => {
    mockRoutes({ ...BASE, require_second_approver: true })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    expect(await screen.findByTestId("keeper-governance-second-approver")).toHaveAttribute("aria-checked", "true")
    expect(screen.queryByTestId("keeper-findings-save")).not.toBeInTheDocument()
  })

  it("surfaces the server's second-approver warning as a warning toast", async () => {
    mockRoutes(
      { ...BASE, require_second_approver: false },
      { putWarning: "second-approver is enabled, but this workspace has fewer than 2 members who can approve escalations" },
    )
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    fireEvent.click(await screen.findByTestId("keeper-governance-second-approver"))
    fireEvent.click(screen.getByTestId("keeper-findings-save"))

    await waitFor(() =>
      expect(toastWarning).toHaveBeenCalledWith(expect.stringContaining("fewer than 2 members")),
    )
    // The advisory is not an error and not a second success signal — the footer
    // already said "Saved".
    expect(toastError).not.toHaveBeenCalled()
    expect(toastSuccess).not.toHaveBeenCalled()
  })

  it("surfaces a failed save in the footer and keeps the draft", async () => {
    apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.includes("/admin/keeper/governance")) {
        if (init?.method === "PUT") return jsonResponse({ error: "risk threshold out of range" }, 400)
        return jsonResponse(BASE)
      }
      if (url.includes("/credentials")) return jsonResponse(CREDENTIALS)
      if (url.includes("/members")) return jsonResponse(MEMBERS)
      throw new Error(`unexpected fetch: ${url}`)
    })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    fireEvent.change(await screen.findByTestId("keeper-governance-risk"), { target: { value: "9" } })
    fireEvent.click(screen.getByTestId("keeper-findings-save"))

    expect(await screen.findByText(/risk threshold out of range/i)).toBeInTheDocument()
    // A failed write must not discard what someone typed.
    expect(screen.getByTestId("keeper-governance-risk")).toHaveValue(9)
  })

  // ── Credential leases card (#1373) ───────────────────────────────────────

  it("renders auto-lease empty (off) when the workspace has not opted in", async () => {
    mockRoutes({ ...BASE, auto_lease_seconds: 0 })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    // Empty, not "0": a meaningful-looking zero reads as a configured value.
    expect(await screen.findByTestId("keeper-governance-auto-lease")).toHaveValue(null)
  })

  it("hydrates auto-lease seconds into minutes and sends seconds back", async () => {
    mockRoutes({ ...BASE, auto_lease_seconds: 900 })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    const input = await screen.findByTestId("keeper-governance-auto-lease")
    expect(input).toHaveValue(15)

    fireEvent.change(input, { target: { value: "30" } })
    fireEvent.click(screen.getByTestId("keeper-leases-save"))

    await waitFor(() => expect(putBodies()).toHaveLength(1))
    expect(putBodies()[0]).toEqual({ auto_lease_seconds: 1800 })
  })

  it("clearing the auto-lease field turns it off (sends 0)", async () => {
    mockRoutes({ ...BASE, auto_lease_seconds: 900 })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    fireEvent.change(await screen.findByTestId("keeper-governance-auto-lease"), { target: { value: "" } })
    fireEvent.click(screen.getByTestId("keeper-leases-save"))

    await waitFor(() => expect(putBodies()).toHaveLength(1))
    expect(putBodies()[0]).toEqual({ auto_lease_seconds: 0 })
  })

  it("blocks a sub-minute auto-lease before it reaches the server", async () => {
    mockRoutes({ ...BASE, auto_lease_seconds: 900 })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    // A fractional minute is below the 60s floor: such a lease can lapse inside
    // Keeper's own evaluation, so the ALLOW would deny the request it authorised.
    fireEvent.change(await screen.findByTestId("keeper-governance-auto-lease"), { target: { value: "0.5" } })
    expect(screen.getByTestId("keeper-governance-auto-lease-invalid")).toHaveTextContent(/whole number of minutes/i)
    expect(screen.getByTestId("keeper-leases-save")).toBeDisabled()
    expect(putBodies()).toHaveLength(0)
  })

  it("blocks an auto-lease above the 30-day cap", async () => {
    mockRoutes({ ...BASE, auto_lease_seconds: 900 })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    fireEvent.change(await screen.findByTestId("keeper-governance-auto-lease"), { target: { value: "43201" } })
    expect(screen.getByTestId("keeper-governance-auto-lease-invalid")).toHaveTextContent(/30 days/i)
    expect(screen.getByTestId("keeper-leases-save")).toBeDisabled()
  })

  it("an unrelated save cannot touch a non-minute-aligned auto-lease TTL", async () => {
    // The CLI accepts any Go duration (`keeper auto-lease set 90s`), so the
    // stored TTL need not be minute-aligned, and the card renders it rounded.
    // With one Save for the whole panel, toggling the watchdog resent the
    // rounded value and silently rewrote 90s to 120s. Now the lease TTL travels
    // on its own card's save and nowhere else, so the hazard is structural, not
    // guarded by a flag.
    mockRoutes({ ...BASE, enabled: false, auto_lease_seconds: 90 })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    expect(await screen.findByTestId("keeper-governance-auto-lease")).toHaveValue(2)

    fireEvent.click(screen.getByTestId("keeper-governance-switch"))
    fireEvent.click(screen.getByTestId("keeper-watchdog-save"))

    await waitFor(() => expect(putBodies()).toHaveLength(1))
    expect(putBodies()[0]).not.toHaveProperty("auto_lease_seconds")
  })

  // ── Workspace governance model card ──────────────────────────────────────

  it("renders the four governance-model provider options (#1001 gov-model)", async () => {
    mockRoutes(BASE)
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
    mockRoutes(BASE)
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    const trigger = await screen.findByTestId("keeper-gov-provider")
    openSelect(trigger)
    fireEvent.click(await screen.findByRole("option", { name: /anthropic/i }))

    // Model input now shown, empty → required message + Save disabled.
    const modelInput = await screen.findByTestId("keeper-gov-model-id")
    expect(modelInput).toHaveValue("")
    expect(screen.getByTestId("keeper-gov-model-required")).toBeInTheDocument()
    expect(screen.getByTestId("keeper-gov-model-save")).toBeDisabled()

    // Whitespace is not a model id either.
    fireEvent.change(modelInput, { target: { value: "   " } })
    expect(screen.getByTestId("keeper-gov-model-save")).toBeDisabled()
    expect(putBodies()).toHaveLength(0)
  })

  it("saves the governance-model fields via PUT, alone (#1001 gov-model)", async () => {
    mockRoutes(BASE)
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

    const save = screen.getByTestId("keeper-gov-model-save")
    expect(save).toBeEnabled()
    fireEvent.click(save)

    await waitFor(() => expect(putBodies()).toHaveLength(1))
    expect(putBodies()[0]).toEqual({
      gov_model_provider: "anthropic",
      gov_model_id: "claude-haiku-4-5",
      gov_model_credential_id: "cred-api",
    })
  })

  it("hydrates the governance-model fields from GET (#1001 gov-model)", async () => {
    mockRoutes({
      ...BASE,
      gov_model_provider: "ollama",
      gov_model_id: "qwen2.5:3b-instruct",
      gov_model_credential_id: "cred-url",
    })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    expect(await screen.findByTestId("keeper-gov-model-id")).toHaveValue("qwen2.5:3b-instruct")
    expect(screen.getByTestId("keeper-gov-provider")).toHaveTextContent(/ollama/i)
    expect(screen.queryByTestId("keeper-gov-model-save")).not.toBeInTheDocument()
  })

  it("dropping back to the server default clears the model id and credential", async () => {
    mockRoutes({
      ...BASE,
      gov_model_provider: "anthropic",
      gov_model_id: "claude-haiku-4-5",
      gov_model_credential_id: "cred-api",
    })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    const trigger = await screen.findByTestId("keeper-gov-provider")
    openSelect(trigger)
    fireEvent.click(await screen.findByRole("option", { name: /server default/i }))
    fireEvent.click(screen.getByTestId("keeper-gov-model-save"))

    await waitFor(() => expect(putBodies()).toHaveLength(1))
    // The server 400s a credential or model id with no provider, so a stale one
    // must not ride along.
    expect(putBodies()[0]).toEqual({
      gov_model_provider: "",
      gov_model_id: "",
      gov_model_credential_id: "",
    })
  })

  // ── Cross-cutting ────────────────────────────────────────────────────────

  it("disables every control and offers no Save for non-managers", async () => {
    canManage = false
    mockRoutes({ ...BASE, require_second_approver: false, auto_lease_seconds: 900 })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    expect(await screen.findByTestId("keeper-governance-switch")).toBeDisabled()
    expect(screen.getByTestId("keeper-governance-risk")).toBeDisabled()
    expect(screen.getByTestId("keeper-governance-second-approver")).toBeDisabled()
    expect(screen.getByTestId("keeper-governance-auto-lease")).toBeDisabled()
    expect(screen.getByTestId("keeper-watch-spec")).toBeDisabled()
    for (const id of ["keeper-watchdog-save", "keeper-findings-save", "keeper-leases-save", "keeper-gov-model-save"]) {
      expect(screen.queryByTestId(id)).not.toBeInTheDocument()
    }
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

  it("a save on one card rebases the others rather than blanking them", async () => {
    // The endpoint returns the whole row on a partial write. If a card adopted
    // only the submitted fields, saving the watchdog would blank the lease TTL
    // and the contact in the cards next to it.
    mockRoutes({ ...BASE, enabled: false, auto_lease_seconds: 900, security_contact_user_id: "u-owner" })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    fireEvent.click(await screen.findByTestId("keeper-governance-switch"))
    fireEvent.click(screen.getByTestId("keeper-watchdog-save"))

    await waitFor(() => expect(putBodies()).toHaveLength(1))
    expect(screen.getByTestId("keeper-governance-auto-lease")).toHaveValue(15)
    expect(screen.getByTestId("keeper-governance-risk")).toHaveValue(7)
    // And the untouched cards stay clean — no phantom "unsaved changes".
    expect(screen.queryByTestId("keeper-leases-save")).not.toBeInTheDocument()
    expect(screen.queryByTestId("keeper-findings-save")).not.toBeInTheDocument()
  })
})
