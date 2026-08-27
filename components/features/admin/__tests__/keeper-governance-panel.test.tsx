import { describe, it, expect, vi, beforeEach, beforeAll } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"
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
  // The four-eyes rule as enforced (#1559) — the toggle above is only half of
  // it. tier_floor_* is what the credential tier forces on its own, and is
  // reported whatever the toggle says.
  effective_second_approver?: {
    min_security_level: number
    min_security_level_label?: string
    source: string
    tier_floor_security_level?: number
    tier_floor_label?: string
  }
  auto_lease_seconds?: number
  // #1001 M3. 0/absent = never configured → the built-in default (5).
  behavior_sample_every?: number
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
  // Mirrors what internal/api/keeper_governance.go computes for a workspace
  // with the toggle off: the tier table alone forces four-eyes, from L4 up.
  effective_second_approver: {
    min_security_level: 4,
    min_security_level_label: "L4 · critical",
    source: "tier",
    tier_floor_security_level: 4,
    tier_floor_label: "L4 · critical",
  },
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
    // The wording says what the state IS and what turning it on does, rather
    // than naming the pattern ("opt-in") the state is an instance of.
    expect(screen.getByText(/off by default/i)).toBeInTheDocument()
    // With the engine ON the copy no longer restates it — a line that says
    // "server engine is on" on a page whose own status strip already says so is
    // one more sentence to read for nothing. It appears only when the engine is
    // OFF, where it is the reason this switch will not do anything yet.
    expect(screen.queryByText(/engine above is off/i)).toBeNull()
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
      // The sampling cadence is this card's field too (#1001 M3) — how often it
      // looks belongs with what it looks for. Unset hydrates as the default, so
      // an untouched cadence is written as the number the operator was shown.
      behavior_sample_every: 5,
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

  // ── Sampling cadence (#1001 M3) ──────────────────────────────────────────

  it("hydrates the sampling cadence, showing the default for a workspace that never set one", async () => {
    mockRoutes({ ...BASE, behavior_sample_every: 0 })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    // 0 on the wire is "never configured", not "never review" — the field has to
    // show the cadence actually in force, or the operator reads it as disabled.
    expect(await screen.findByTestId("keeper-governance-sample-every")).toHaveValue(5)
    expect(screen.queryByTestId("keeper-watchdog-save")).not.toBeInTheDocument()
  })

  it("hydrates an explicitly configured cadence", async () => {
    mockRoutes({ ...BASE, behavior_sample_every: 20 })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    expect(await screen.findByTestId("keeper-governance-sample-every")).toHaveValue(20)
  })

  it("saves an edited cadence with the watchdog card", async () => {
    mockRoutes({ ...BASE, behavior_sample_every: 5, watch_presets: [], watch_spec: "" })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    fireEvent.change(await screen.findByTestId("keeper-governance-sample-every"), { target: { value: "12" } })
    fireEvent.click(screen.getByTestId("keeper-watchdog-save"))

    await waitFor(() => expect(putBodies()).toHaveLength(1))
    expect(putBodies()[0]).toMatchObject({ behavior_sample_every: 12 })
    expect(putBodies()[0]).not.toHaveProperty("deny_notify_min_risk")
  })

  it("refuses 0 by name — that is the switch's job, not the cadence's", async () => {
    mockRoutes({ ...BASE, behavior_sample_every: 5 })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    fireEvent.change(await screen.findByTestId("keeper-governance-sample-every"), { target: { value: "0" } })

    // The message has to point at the control that actually turns it off; a bare
    // "out of range" would leave the operator hunting for the off value.
    expect(screen.getByTestId("keeper-governance-sample-every-invalid")).toHaveTextContent(/turn it off/i)
    expect(screen.getByTestId("keeper-watchdog-save")).toBeDisabled()
  })

  it("refuses a cadence past the ceiling", async () => {
    mockRoutes({ ...BASE, behavior_sample_every: 5 })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    fireEvent.change(await screen.findByTestId("keeper-governance-sample-every"), { target: { value: "101" } })
    expect(screen.getByTestId("keeper-governance-sample-every-invalid")).toHaveTextContent(/1 to 100/)
    expect(screen.getByTestId("keeper-watchdog-save")).toBeDisabled()
  })

  it("warns about the cost of an aggressive cadence without blocking it", async () => {
    mockRoutes({ ...BASE, behavior_sample_every: 5 })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    fireEvent.change(await screen.findByTestId("keeper-governance-sample-every"), { target: { value: "1" } })

    // Reviewing everything is a real posture, so it stays saveable — the cost is
    // stated rather than refused.
    expect(screen.queryByTestId("keeper-governance-sample-every-invalid")).not.toBeInTheDocument()
    expect(screen.getByTestId("keeper-governance-sample-every-cost")).toBeInTheDocument()
    expect(screen.getByTestId("keeper-watchdog-save")).toBeEnabled()
  })

  it("hides the cadence with the rest of the rules when the watchdog is off", async () => {
    mockRoutes({ ...BASE, enabled: false })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    await screen.findByTestId("keeper-governance-switch")
    // Configuring how often a monitor that is not running looks is a screen for
    // a thing that does not exist yet — same reasoning as the watch rules.
    expect(screen.queryByTestId("keeper-governance-sample-every")).not.toBeInTheDocument()
  })

  // Turning the watchdog OFF is the one action on this card that must never be
  // blocked by something else on it. The cadence row unmounts with the rest of
  // the rules, taking its inline error with it — so a cadence left mid-edit
  // would veto the Save with nothing on screen saying why, and the operator
  // could not switch the monitor off at all.
  it("still turns the watchdog off when the cadence was left mid-edit", async () => {
    mockRoutes({ ...BASE, enabled: true, behavior_sample_every: 5, watch_spec: "", watch_presets: [] })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    fireEvent.change(await screen.findByTestId("keeper-governance-sample-every"), { target: { value: "" } })
    expect(screen.getByTestId("keeper-watchdog-save")).toBeDisabled()

    fireEvent.click(screen.getByTestId("keeper-governance-switch"))
    expect(screen.queryByTestId("keeper-governance-sample-every")).not.toBeInTheDocument()

    const save = screen.getByTestId("keeper-watchdog-save")
    expect(save).toBeEnabled()
    fireEvent.click(save)

    await waitFor(() => expect(putBodies()).toHaveLength(1))
    // …and the half-typed cadence does not ride along. Sending it would 400 the
    // whole save on a value the operator can no longer see or correct.
    expect(putBodies()[0]).toMatchObject({ enabled: false })
    expect(putBodies()[0]).not.toHaveProperty("behavior_sample_every")
  })

  // A cadence is a rule about a running monitor. With the switch off the row is
  // hidden, so writing one is writing a field nobody was shown — and it would
  // spend the "never configured" sentinel that keeps an untouched workspace on
  // whatever the built-in default is, rather than on today's copy of it.
  it("does not write a cadence the operator was never shown", async () => {
    mockRoutes({ ...BASE, enabled: true, behavior_sample_every: 0, watch_spec: "", watch_presets: [] })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    fireEvent.click(await screen.findByTestId("keeper-governance-switch"))
    fireEvent.click(screen.getByTestId("keeper-watchdog-save"))

    await waitFor(() => expect(putBodies()).toHaveLength(1))
    expect(putBodies()[0]).toMatchObject({ enabled: false })
    expect(putBodies()[0]).not.toHaveProperty("behavior_sample_every")
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

  // ── Four-eyes: the toggle is only half the rule (#1559) ──────────────────
  //
  // keeper.TierPolicy.SecondApprover forces four-eyes on the top tier whatever
  // this switch says. Rendered as if the switch were the only control, "off"
  // read as "nobody needs a second approver here" — and the first correction an
  // operator got was a 403 on their own approval. The note has to appear
  // exactly when the switch is off: with it on, the row's own description
  // already says a second approver is required, and repeating it there would
  // be a second sentence saying the same thing.

  it("names the tier floor that still applies when the toggle is off", async () => {
    mockRoutes({ ...BASE, require_second_approver: false })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    const note = await screen.findByTestId("keeper-second-approver-tier-note")
    // The label comes from the server's tier table, not from a string in the
    // console — that is the whole point of shipping tier_floor_label.
    expect(note).toHaveTextContent("L4 · critical")
    expect(note).toHaveTextContent(/tighten/i)
  })

  it("drops the tier note the moment the toggle is switched on", async () => {
    mockRoutes({ ...BASE, require_second_approver: false })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    expect(await screen.findByTestId("keeper-second-approver-tier-note")).toBeInTheDocument()
    fireEvent.click(screen.getByTestId("keeper-governance-second-approver"))
    // Follows the draft, not the saved row: the note answers "what applies if
    // this stays off", which is already false before the save lands.
    expect(screen.queryByTestId("keeper-second-approver-tier-note")).not.toBeInTheDocument()
  })

  it("shows no tier note when the toggle is already on", async () => {
    mockRoutes({ ...BASE, require_second_approver: true })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    await screen.findByTestId("keeper-governance-second-approver")
    expect(screen.queryByTestId("keeper-second-approver-tier-note")).not.toBeInTheDocument()
  })

  it("says nothing when the server reports no tier floor", async () => {
    // A pre-#1559 server sends no effective block, and a server whose tier
    // table forces nothing sends an empty floor. Inventing "L4" from a constant
    // in the console is exactly the drift this field exists to prevent.
    mockRoutes({ ...BASE, require_second_approver: false, effective_second_approver: undefined })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    await screen.findByTestId("keeper-governance-second-approver")
    expect(screen.queryByTestId("keeper-second-approver-tier-note")).not.toBeInTheDocument()
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

  // section="judge" renders ONLY the workspace judge override. It moved next to
  // the instance judge it overrides — the two are one question asked at two
  // scopes, and having them at opposite ends of the page is what made an operator
  // conclude there was no way to choose a model or an API key at all.
  // #1558: the other half of the scope explanation. This card is the ONLY place
  // the CREDENTIAL judge can be hosted, because the key it needs lives in this
  // workspace's vault — and the card has to say that before the operator goes
  // looking for Anthropic in the instance card and hits a 400. (The Background
  // checks slots are hosted-capable too, but they are a different question and
  // read their key from the server env, not the vault.)
  it("says what it governs, at which scope, and where the other case lives", async () => {
    mockRoutes(BASE)
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} section="judge" />)

    const scope = await screen.findByTestId("keeper-gov-scope")
    // Scope: this workspace only.
    expect(scope).toHaveTextContent(/this workspace only/i)
    // What only this card can do.
    expect(scope).toHaveTextContent(/anthropic|openai-compatible/i)
    expect(scope).toHaveTextContent(/vault/i)
    // The pointer at the other card, named as it is titled, with its limit.
    expect(scope).toHaveTextContent(/credential access judge/i)
    expect(scope).toHaveTextContent(/ollama/i)
  })

  it("renders the four governance-model provider options (#1001 gov-model)", async () => {
    mockRoutes(BASE)
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} section="judge" />)

    const trigger = await screen.findByTestId("keeper-gov-provider")
    // Defaults to "use the instance judge", and no model input until a concrete
    // provider is chosen. The labels name the DECISION rather than the provider
    // taxonomy: an operator picking a judge is choosing where it thinks and what
    // it costs, and "Server default" told them neither.
    expect(trigger).toHaveTextContent(/use the instance judge/i)
    expect(screen.queryByTestId("keeper-gov-model-id")).not.toBeInTheDocument()

    openSelect(trigger)
    for (const label of [
      /use the instance judge/i,
      /a different local model/i,
      /anthropic \(claude\)/i,
      /openai-compatible endpoint/i,
    ]) {
      expect(await screen.findByRole("option", { name: label })).toBeInTheDocument()
    }
  })

  it("blocks save when a provider is set but the model id is empty (#1001 gov-model)", async () => {
    mockRoutes(BASE)
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} section="judge" />)

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
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} section="judge" />)

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
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} section="judge" />)

    expect(await screen.findByTestId("keeper-gov-model-id")).toHaveValue("qwen2.5:3b-instruct")
    expect(screen.getByTestId("keeper-gov-provider")).toHaveTextContent(/a different local model/i)
    expect(screen.queryByTestId("keeper-gov-model-save")).not.toBeInTheDocument()
  })

  it("dropping back to the server default clears the model id and credential", async () => {
    mockRoutes({
      ...BASE,
      gov_model_provider: "anthropic",
      gov_model_id: "claude-haiku-4-5",
      gov_model_credential_id: "cred-api",
    })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} section="judge" />)

    const trigger = await screen.findByTestId("keeper-gov-provider")
    openSelect(trigger)
    fireEvent.click(await screen.findByRole("option", { name: /use the instance judge/i }))
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

  // ── Findings routing check ───────────────────────────────────────────────

  it("sends a test finding and lists who it reached", async () => {
    mockRoutes(BASE)
    // Layer the findings endpoint over the governance mock.
    const govImpl = apiFetch.getMockImplementation()!
    apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.includes("/admin/keeper/findings/test")) {
        return jsonResponse({
          inbox_item_id: "ibx_escalation_keepertest_1",
          recipients: [
            { user_id: "u-owner", email: "owner@x.dev", role: "OWNER", reason: "security contact" },
            { user_id: "u-mgr", email: "mgr@x.dev", role: "MANAGER", reason: "role fanout" },
          ],
        })
      }
      return govImpl(url, init)
    })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    fireEvent.click(await screen.findByTestId("keeper-findings-test"))

    const result = await screen.findByTestId("keeper-findings-test-result")
    expect(result).toHaveTextContent(/reaches 2 people/i)
    expect(result).toHaveTextContent(/owner@x.dev/)
    expect(result).toHaveTextContent(/security contact/i)
    expect(result).toHaveTextContent(/role fanout/i)
    await waitFor(() => expect(toastSuccess).toHaveBeenCalled())
    // The check must not disturb the card's settings — it is an action on what
    // is already saved.
    expect(screen.queryByTestId("keeper-findings-save")).not.toBeInTheDocument()
  })

  // A finding with no audience is the misconfiguration the button exists to
  // find, so it has to read as a failure rather than a green "sent".
  it("reports a test finding that reached nobody as a problem", async () => {
    mockRoutes(BASE)
    const govImpl = apiFetch.getMockImplementation()!
    apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.includes("/admin/keeper/findings/test")) {
        return jsonResponse({
          inbox_item_id: "ibx_escalation_keepertest_2",
          recipients: [],
          warning: "This finding reached nobody: no security contact is set and this workspace has no member with MANAGER, ADMIN or OWNER role.",
        })
      }
      return govImpl(url, init)
    })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    fireEvent.click(await screen.findByTestId("keeper-findings-test"))

    const result = await screen.findByTestId("keeper-findings-test-result")
    expect(result).toHaveTextContent(/reached nobody/i)
    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(toastSuccess).not.toHaveBeenCalled()
  })

  it("offers no test-finding button to a non-manager", async () => {
    canManage = false
    mockRoutes(BASE)
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    await screen.findByTestId("keeper-governance-risk")
    expect(screen.queryByTestId("keeper-findings-test")).not.toBeInTheDocument()
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

// Not asking for configuration of a thing that is not running.
//
// The watchdog card showed the preset checkboxes and the free-text rule box
// whether or not the watchdog was on — and it is off by default. So the first
// thing an operator saw was a rules editor for a monitor that does not exist yet,
// which is most of what made this page feel like work rather than a switch.
describe("WatchdogCard — how much it asks for before it is on", () => {
  beforeEach(() => { cleanup(); apiFetch.mockReset() })

  it("asks only the on/off question while it is off", async () => {
    mockRoutes({ ...BASE, configured: false, enabled: false })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    await screen.findByTestId("keeper-governance-switch")
    expect(screen.queryByTestId("keeper-watch-spec")).toBeNull()
    expect(screen.queryByTestId("keeper-watch-preset-credentials")).toBeNull()
  })

  it("reveals what to flag once it is turned on", async () => {
    mockRoutes({ ...BASE, configured: false, enabled: false })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={true} />)

    fireEvent.click(await screen.findByTestId("keeper-governance-switch"))
    // Immediately, on the draft — not after a save. Choosing the rules is part of
    // turning it on, and a round-trip in the middle of one decision is a place to
    // give up.
    expect(await screen.findByTestId("keeper-watch-preset-credentials")).toBeTruthy()
    expect(screen.getByTestId("keeper-watch-spec")).toBeTruthy()
  })

  it("says why the switch will not do anything while the engine is off", async () => {
    mockRoutes({ ...BASE, configured: false, enabled: false })
    render(<KeeperGovernancePanel workspaceId="ws1" serverEnabled={false} />)

    // Two switches, one of which silently gates the other, is exactly the shape
    // that produces "I turned it on and nothing happened".
    expect(await screen.findByText(/engine above is off/i)).toBeTruthy()
  })
})
