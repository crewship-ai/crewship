import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import {
  EgressAllowlistAction,
  blockedEgressHost,
  isConfusableHost,
  normalizeEgressHost,
} from "../egress-allowlist-action"
import type { JournalEntry } from "@/lib/types/journal"

// Drive the component through its real fetch path with a stubbed apiFetch so
// the GET-then-union-then-PATCH contract against crews.go is exercised, not
// mocked away.
const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetch(...args),
}))

let role = "OWNER"
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ abilities: { can: () => true }, role, loading: false }),
}))

const toastSuccess = vi.fn()
const toastError = vi.fn()
const toastInfo = vi.fn()
vi.mock("sonner", () => ({
  toast: {
    success: (...a: unknown[]) => toastSuccess(...a),
    error: (...a: unknown[]) => toastError(...a),
    info: (...a: unknown[]) => toastInfo(...a),
  },
}))

function jsonResponse(body: unknown, status = 200) {
  return { ok: status < 400, status, json: async () => body }
}

function deniedEntry(overrides: Partial<JournalEntry> = {}): JournalEntry {
  return {
    id: "j1",
    workspace_id: "ws1",
    ts: new Date("2026-07-25T10:00:00Z").toISOString(),
    entry_type: "network.egress",
    severity: "warn",
    actor_type: "agent",
    summary: "CONNECT raw.githubusercontent.com:443 → BLOCKED by network policy",
    crew_id: "crew_1",
    payload: { host: "raw.githubusercontent.com:443", method: "CONNECT", denied: true },
    ...overrides,
  } as JournalEntry
}

beforeEach(() => {
  apiFetch.mockReset()
  toastSuccess.mockReset()
  toastError.mockReset()
  toastInfo.mockReset()
  role = "OWNER"
})

describe("normalizeEgressHost — mirrors internal/api/crews.go normalizeDomain", () => {
  it("strips the :port a CONNECT denial carries", () => {
    expect(normalizeEgressHost("raw.githubusercontent.com:443")).toBe("raw.githubusercontent.com")
  })
  it("lowercases and trims", () => {
    expect(normalizeEgressHost("  API.GitHub.com ")).toBe("api.github.com")
  })
  it("rejects hosts the server allowlist would drop", () => {
    // No dot, IPv6 literal, and path-ish junk all normalize to "" server-side —
    // offering a button for them would promise a save that silently no-ops.
    expect(normalizeEgressHost("localhost")).toBeNull()
    expect(normalizeEgressHost("[::1]:443")).toBeNull()
    expect(normalizeEgressHost("::1")).toBeNull()
    expect(normalizeEgressHost("evil.com/path")).toBeNull()
    expect(normalizeEgressHost(undefined)).toBeNull()
  })
})

describe("isConfusableHost — the one-click guard", () => {
  it("passes ordinary ASCII hosts", () => {
    expect(isConfusableHost("raw.githubusercontent.com")).toBe(false)
    expect(isConfusableHost("api-2.example.co.uk")).toBe(false)
  })
  it("flags a homoglyph of a trusted domain", () => {
    // Cyrillic "а" (U+0430) in place of ASCII "a".
    expect(isConfusableHost("аpi.anthropic.com")).toBe(true)
  })
  it("flags an already-punycoded label", () => {
    expect(isConfusableHost("xn--pi-fmc.anthropic.com")).toBe(true)
  })
})

describe("blockedEgressHost — the render predicate", () => {
  it("matches a denied network.egress with a crew", () => {
    expect(blockedEgressHost(deniedEntry())).toBe("raw.githubusercontent.com")
  })
  it("ignores an ALLOWED egress entry", () => {
    const e = deniedEntry({ payload: { host: "api.anthropic.com", status_code: 200 } })
    expect(blockedEgressHost(e)).toBeNull()
  })
  it("ignores a denial with no crew to patch", () => {
    expect(blockedEgressHost(deniedEntry({ crew_id: undefined }))).toBeNull()
  })
  it("ignores non-egress entry types", () => {
    expect(blockedEgressHost(deniedEntry({ entry_type: "exec.command" }))).toBeNull()
  })
})

describe("<EgressAllowlistAction>", () => {
  it("renders nothing for an entry that is not a remediable denial", () => {
    const { container } = render(<EgressAllowlistAction entry={deniedEntry({ crew_id: undefined })} />)
    expect(container).toBeEmptyDOMElement()
  })

  it("unions the port-stripped host onto the crew's existing allowed_domains", async () => {
    apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (init?.method === "PATCH") return jsonResponse({ id: "crew_1" })
      return jsonResponse({ network_mode: "restricted", allowed_domains: ["github.com"] })
    })

    render(<EgressAllowlistAction entry={deniedEntry()} />)
    fireEvent.click(screen.getByRole("button", { name: /add raw\.githubusercontent\.com/i }))

    await waitFor(() => expect(toastSuccess).toHaveBeenCalled())
    const patchCall = apiFetch.mock.calls.find((c) => c[1]?.method === "PATCH")
    expect(patchCall?.[0]).toBe("/api/v1/crews/crew_1")
    // Existing entries preserved — this must never clobber the allowlist.
    expect(JSON.parse(String(patchCall?.[1]?.body))).toEqual({
      allowed_domains: ["github.com", "raw.githubusercontent.com"],
    })
    expect(await screen.findByText(/added to allowlist/i)).toBeInTheDocument()
  })

  it("does not PATCH when the host is already allowed", async () => {
    apiFetch.mockImplementation(async () =>
      jsonResponse({ network_mode: "restricted", allowed_domains: ["raw.githubusercontent.com"] }),
    )
    render(<EgressAllowlistAction entry={deniedEntry()} />)
    fireEvent.click(screen.getByRole("button", { name: /add raw\.githubusercontent\.com/i }))

    await waitFor(() => expect(toastInfo).toHaveBeenCalled())
    expect(apiFetch.mock.calls.some((c) => c[1]?.method === "PATCH")).toBe(false)
  })

  it("surfaces the server error and stays clickable when the PATCH is refused", async () => {
    apiFetch.mockImplementation(async (_url: string, init?: RequestInit) => {
      if (init?.method === "PATCH") return jsonResponse({ error: "Forbidden" }, 403)
      return jsonResponse({ network_mode: "restricted", allowed_domains: [] })
    })
    render(<EgressAllowlistAction entry={deniedEntry()} />)
    fireEvent.click(screen.getByRole("button", { name: /add raw\.githubusercontent\.com/i }))

    await waitFor(() => expect(toastError).toHaveBeenCalledWith("Forbidden"))
    expect(screen.getByRole("button", { name: /add raw\.githubusercontent\.com/i })).toBeEnabled()
  })

  it("refuses the one-click path for a homoglyph host, even for an admin", () => {
    // The button's own label is the thing an admin trusts; a host the blocked
    // agent chose must not be able to make that label read as a familiar domain.
    render(
      <EgressAllowlistAction
        entry={deniedEntry({ payload: { host: "аpi.anthropic.com:443", denied: true } })}
      />,
    )
    expect(screen.queryByRole("button")).toBeNull()
    expect(screen.getByText(/may impersonate a familiar domain/i)).toBeInTheDocument()
  })

  it("shows a read-only explanation instead of a button for non-admins", () => {
    role = "MEMBER"
    render(<EgressAllowlistAction entry={deniedEntry()} />)
    expect(screen.queryByRole("button")).toBeNull()
    expect(screen.getByText(/an admin can add/i)).toBeInTheDocument()
  })
})
