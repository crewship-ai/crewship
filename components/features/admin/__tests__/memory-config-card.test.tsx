import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { MemoryConfigCard } from "../memory-config-card"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

let role = "OWNER"
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ abilities: { can: () => true }, role, loading: false }),
}))

const toastSuccess = vi.fn()
const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: { success: (...a: unknown[]) => toastSuccess(...a), error: (...a: unknown[]) => toastError(...a) },
}))

function jsonResponse(body: unknown, status = 200) {
  return { ok: status < 400, status, json: async () => body }
}

beforeEach(() => {
  apiFetch.mockReset()
  toastSuccess.mockReset()
  toastError.mockReset()
  role = "OWNER"
})

describe("<MemoryConfigCard> — #1379", () => {
  it("says when the value is only the built-in default", async () => {
    // Whether the value was chosen decides whether editing is routine or is
    // overriding somebody's deliberate policy.
    apiFetch.mockResolvedValue(jsonResponse({ workspace_id: "ws1", versions_retention_days: 30, is_default: true }))
    render(<MemoryConfigCard workspaceId="ws-1" />)
    expect(await screen.findByText(/built-in default/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/keep history for/i)).toHaveValue(30)
  })

  it("says when the value was set explicitly", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ workspace_id: "ws1", versions_retention_days: 7, is_default: false }))
    render(<MemoryConfigCard workspaceId="ws-1" />)
    expect(await screen.findByText(/set explicitly/i)).toBeInTheDocument()
  })

  it("PATCHes only the one key so a newer setting can't be clobbered", async () => {
    apiFetch.mockImplementation(async (_u: string, init?: RequestInit) => {
      if (init?.method === "PATCH") {
        return jsonResponse({ workspace_id: "ws1", versions_retention_days: 14, is_default: false })
      }
      return jsonResponse({ workspace_id: "ws1", versions_retention_days: 30, is_default: false })
    })
    render(<MemoryConfigCard workspaceId="ws-1" />)
    const input = await screen.findByLabelText(/keep history for/i)
    fireEvent.change(input, { target: { value: "14" } })
    fireEvent.click(screen.getByRole("button", { name: /save/i }))

    await waitFor(() => expect(toastSuccess).toHaveBeenCalled())
    const patch = apiFetch.mock.calls.find((c) => c[1]?.method === "PATCH")
    expect(JSON.parse(String(patch?.[1]?.body))).toEqual({ versions_retention_days: 14 })
  })

  it("blocks an out-of-range value client-side", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ workspace_id: "ws1", versions_retention_days: 30, is_default: false }))
    render(<MemoryConfigCard workspaceId="ws-1" />)
    const input = await screen.findByLabelText(/keep history for/i)
    fireEvent.change(input, { target: { value: "0" } })
    expect(screen.getByText(/between 1 and 3650/i)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /save/i })).toBeDisabled()
  })

  it("relays the server's rejection message rather than a generic failure", async () => {
    // The bounds live on the server; its message names the actual violated rule.
    apiFetch.mockImplementation(async (_u: string, init?: RequestInit) => {
      if (init?.method === "PATCH") {
        return jsonResponse({ error: "versions_retention_days must be <= 3650 (10 years)" }, 400)
      }
      return jsonResponse({ workspace_id: "ws1", versions_retention_days: 30, is_default: false })
    })
    render(<MemoryConfigCard workspaceId="ws-1" />)
    const input = await screen.findByLabelText(/keep history for/i)
    fireEvent.change(input, { target: { value: "100" } })
    fireEvent.click(screen.getByRole("button", { name: /save/i }))
    await waitFor(() =>
      expect(toastError).toHaveBeenCalledWith("versions_retention_days must be <= 3650 (10 years)"),
    )
  })

  it("is read-only for a non-admin", async () => {
    role = "MEMBER"
    apiFetch.mockResolvedValue(jsonResponse({ workspace_id: "ws1", versions_retention_days: 30, is_default: false }))
    render(<MemoryConfigCard workspaceId="ws-1" />)
    expect(await screen.findByLabelText(/keep history for/i)).toBeDisabled()
    expect(screen.queryByRole("button", { name: /save/i })).toBeNull()
    expect(screen.getByText(/requires an admin/i)).toBeInTheDocument()
  })

  it("explains a 403 instead of showing an empty card", async () => {
    apiFetch.mockResolvedValue(jsonResponse({}, 403))
    render(<MemoryConfigCard workspaceId="ws-1" />)
    await waitFor(() => expect(screen.getByText(/requires an admin role/i)).toBeInTheDocument())
  })
})

// The card was called "Memory configuration" with a retention window, which
// reads as "how long agents remember". It is not that: an agent's memory is
// its own files, kept for as long as the agent exists. What expires here is
// the VERSION TRAIL of those files. Anyone who mistook one for the other
// would either panic about data loss or set a window expecting an effect it
// cannot have.
describe("<MemoryConfigCard> — says what it actually governs", () => {
  beforeEach(() => {
    apiFetch.mockReset()
    apiFetch.mockResolvedValue(jsonResponse({ versions_retention_days: 30, is_default: true }))
  })

  it("does not claim to govern what an agent remembers", async () => {
    render(<MemoryConfigCard workspaceId="ws-1" />)
    expect(await screen.findByText(/does not affect what an agent remembers/i)).toBeInTheDocument()
  })

  it("names what does expire — the history of past versions", async () => {
    render(<MemoryConfigCard workspaceId="ws-1" />)
    expect(await screen.findByText(/trail of past versions/i)).toBeInTheDocument()
  })

  it("says when the trim runs, including the boot pass", async () => {
    render(<MemoryConfigCard workspaceId="ws-1" />)
    // Only the daily tick used to run, and a restart-heavy instance therefore
    // never swept at all — the boot pass is why the window means anything.
    expect(await screen.findByText(/shortly after this instance starts/i)).toBeInTheDocument()
    expect(screen.getByText(/03:00 UTC/)).toBeInTheDocument()
  })

  it("mentions the floor that survives any window", async () => {
    render(<MemoryConfigCard workspaceId="ws-1" />)
    expect(await screen.findByText(/three most recent versions/i)).toBeInTheDocument()
  })
})
