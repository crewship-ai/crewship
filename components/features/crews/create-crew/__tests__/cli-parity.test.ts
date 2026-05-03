// =============================================================================
// CLI ↔ Wizard parity test
//
// The wizard MUST cover every flag of `crewship crew create`. This test is the
// canary: when someone adds a flag in cmd/crewship/cmd_crew_manage.go, the
// matching test entry below has to be added too — otherwise an entire UI path
// silently lags behind the CLI. Same the other way round: if a wizard input
// disappears, the corresponding CLI flag's coverage assertion fails.
//
// The test parses cmd_crew_manage.go from disk, extracts the flag names from
// `crewCreateCmd.Flags()` calls, and checks each one against an EXPECTED_MAP
// that maps CLI flag -> WizardState field + the API body key it ends up as.
//
// If a flag is missing from EXPECTED_MAP, the test fails with a message that
// asks the human to consciously decide: is this flag intentionally CLI-only,
// or should the wizard cover it?
// =============================================================================

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { readFileSync } from "node:fs"
import { resolve } from "node:path"
import { INITIAL_STATE, type WizardState } from "../types"
import { submitCrew } from "../submit"

vi.mock("sonner", () => ({
  toast: { warning: vi.fn(), error: vi.fn(), success: vi.fn(), info: vi.fn() },
}))

const CLI_SOURCE_PATH = resolve(__dirname, "../../../../../cmd/crewship/cmd_crew_manage.go")

// EXPECTED_MAP — the source of truth that pairs every CLI flag with its
// counterpart in the wizard. Tuple: [wizard state key | "n/a", API body key].
// "n/a" + null means the flag is intentionally not exposed in the wizard
// (and there must be a comment explaining why).
const EXPECTED_MAP: Record<string, { wizardField: keyof WizardState | "n/a"; bodyKey: string | null; note?: string }> = {
  // Identity step
  "name":             { wizardField: "name",            bodyKey: "name" },
  "slug":             { wizardField: "slug",            bodyKey: "slug" },
  "description":      { wizardField: "description",     bodyKey: "description" },
  "color":            { wizardField: "color",           bodyKey: "color" },
  "icon":             { wizardField: "icon",            bodyKey: "icon" },
  // Runtime step
  "memory-mb":        { wizardField: "memoryMB",        bodyKey: "container_memory_mb" },
  "cpus":             { wizardField: "cpus",            bodyKey: "container_cpus" },
  "ttl":              { wizardField: "ttlHours",        bodyKey: "container_ttl_hours" },
  "network-mode":     { wizardField: "networkMode",     bodyKey: "network_mode" },
  "allowed-domains":  { wizardField: "allowedDomains",  bodyKey: "allowed_domains" },
}

// =============================================================================
// CLI flag extraction
// =============================================================================

function readCLIFlags(): string[] {
  const src = readFileSync(CLI_SOURCE_PATH, "utf-8")
  // Capture only flags registered on the CREATE command (not update/delete),
  // matching lines like:  crewCreateCmd.Flags().String("foo", "", "…")
  const re = /crewCreateCmd\.Flags\(\)\.\w+\("([\w-]+)"/g
  const flags: string[] = []
  let m: RegExpExecArray | null
  while ((m = re.exec(src)) !== null) flags.push(m[1])
  return flags
}

describe("CLI ↔ Wizard parity", () => {
  it("every `crewship crew create` flag has an entry in EXPECTED_MAP", () => {
    const cliFlags = readCLIFlags()
    expect(cliFlags.length).toBeGreaterThan(0) // sanity: regex actually matched

    const missing = cliFlags.filter((f) => !(f in EXPECTED_MAP))
    expect(missing, `New CLI flag(s) detected with no wizard coverage decision:
  ${missing.join(", ")}

Update components/features/crews/create-crew/__tests__/cli-parity.test.ts
EXPECTED_MAP and either:
  (a) wire the flag through wizard state + submit body, or
  (b) explicitly document it as { wizardField: "n/a", bodyKey: null,
      note: "<reason this is intentionally CLI-only>" }.`).toEqual([])
  })

  it("every EXPECTED_MAP entry that maps to a wizard field has that field on WizardState", () => {
    for (const [cliFlag, { wizardField }] of Object.entries(EXPECTED_MAP)) {
      if (wizardField === "n/a") continue
      expect(INITIAL_STATE, `CLI flag --${cliFlag} maps to WizardState.${String(wizardField)} but that key is missing from INITIAL_STATE — wizard state field has been removed without updating EXPECTED_MAP`).toHaveProperty(String(wizardField))
    }
  })

  it("submitting a fully-populated wizard sends every mapped CLI flag's body key to the API", async () => {
    const calls = setupFetchMock()
    queueResponse(calls, { ok: true, body: { id: "x", slug: "x", name: "X" } })

    const populated: WizardState = {
      ...INITIAL_STATE,
      name: "Engineering",
      slug: "engineering",
      description: "Backend",
      color: "blue",
      icon: "code",
      memoryMB: 4096,
      cpus: 2,
      ttlHours: 24,
      networkMode: "restricted",
      allowedDomains: ["github.com"],
      mode: "empty",
    }

    await submitCrew("ws_test", populated)
    vi.unstubAllGlobals()

    const body = calls.calls[0].body!
    for (const [cliFlag, { bodyKey }] of Object.entries(EXPECTED_MAP)) {
      if (bodyKey === null) continue // intentional CLI-only
      expect(body, `CLI flag --${cliFlag} (body key "${bodyKey}") is not present in the POST body that the wizard sent`).toHaveProperty(bodyKey)
    }
  })

  // -----------------------------------------------------------------------------
  // Devil's-advocate audit (lock-in for future readers)
  // -----------------------------------------------------------------------------

  it("wizard exposes container fields that CLI does NOT (runtime_image, devcontainer_config, mise_config, mcp_config_json) — superset", () => {
    expect(INITIAL_STATE).toHaveProperty("runtimeImage")
    expect(INITIAL_STATE).toHaveProperty("devcontainerConfig")
    expect(INITIAL_STATE).toHaveProperty("miseConfig")
    expect(INITIAL_STATE).toHaveProperty("mcpConfig")

    // Document the reason: these are settable via `crewship crew config <slug>`
    // post-create on the CLI, but the wizard puts them in step 4 so users can
    // configure once at create time instead of running a follow-up command.
  })

  it("wizard does NOT expose escalation_config, issue_prefix, avatar_style — UI-only, by design", () => {
    // These fields exist on the crews table (migrate_consts_v33_v41.go) and on
    // updateCrewRequest, but are NOT in createCrewRequest, NOT in the CLI, and
    // NOT used at crew-create time anywhere. They're managed post-create on the
    // crew settings page. If someone wires them into createCrewRequest later,
    // this assertion fires as a reminder to revisit the wizard.
    expect(INITIAL_STATE).not.toHaveProperty("escalationConfig")
    expect(INITIAL_STATE).not.toHaveProperty("issuePrefix")
    expect(INITIAL_STATE).not.toHaveProperty("avatarStyle")
  })
})

// =============================================================================
// Local fetch mock — duplicated trivially from submit.test.ts so this file
// stands alone (parity tests should not depend on other test files).
// =============================================================================

interface MockCall { url: string; method: string; body: Record<string, unknown> | undefined }

function setupFetchMock() {
  const calls: MockCall[] = []
  const responses: Array<{ ok: boolean; status: number; body: unknown }> = []
  const fetchMock = vi.fn(async (url: string | URL, init?: RequestInit) => {
    const u = typeof url === "string" ? url : url.toString()
    let parsedBody: Record<string, unknown> | undefined
    if (init?.body && typeof init.body === "string") {
      try { parsedBody = JSON.parse(init.body) } catch { /* ignore */ }
    }
    calls.push({ url: u, method: init?.method ?? "GET", body: parsedBody })
    const r = responses.shift() ?? { ok: true, status: 200, body: {} }
    return {
      ok: r.ok, status: r.status,
      json: async () => r.body,
      text: async () => (typeof r.body === "string" ? r.body : JSON.stringify(r.body)),
    } as Response
  })
  vi.stubGlobal("fetch", fetchMock)
  return { calls, responses }
}

function queueResponse(state: { responses: Array<{ ok: boolean; status: number; body: unknown }> }, r: { ok: boolean; status?: number; body: unknown }) {
  state.responses.push({ ok: r.ok, status: r.status ?? 200, body: r.body })
}

beforeEach(() => { /* per-test stub */ })
afterEach(() => { vi.unstubAllGlobals() })
