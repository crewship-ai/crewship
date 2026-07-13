// Tests for the /credentials page — RBAC-gated row actions, the
// list-load error state (must never masquerade as "no credentials
// yet"), the pending-approval inbox deep-link, and bulk-delete
// partial-failure reporting.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"
import { toast } from "sonner"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"
import CredentialsPage from "../page"

// Hoisted holder so vi.mock factories can read per-test state.
const h = vi.hoisted(() => ({
  role: "OWNER" as string,
  apiFetch: vi.fn(),
}))

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => h.apiFetch(...args),
}))

vi.mock("next/link", () => ({
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  default: ({ href, children, ...rest }: any) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}))

vi.mock("@/hooks/use-abilities", async () => {
  const { defineAbilitiesFor } = await import("@/lib/permissions/abilities")
  return {
    useAbilities: () => ({
      abilities: defineAbilitiesFor(h.role as never),
      role: h.role,
      loading: false,
    }),
  }
})

// Feature children are exercised by their own suites — stub them so
// this test stays about the page shell (list, actions, dialogs).
vi.mock("@/components/features/credentials/add-secret-sheet", () => ({
  AddSecretSheet: () => <div data-testid="add-secret-sheet" />,
}))
vi.mock("@/components/features/credentials/credential-detail-sheet", () => ({
  CredentialDetailSheet: () => <div data-testid="detail-sheet" />,
}))
vi.mock("@/components/features/credentials/rotation-dialog", () => ({
  RotationDialog: () => <div data-testid="rotation-dialog" />,
}))
vi.mock("@/components/features/credentials/edit-credential-dialog", () => ({
  EditCredentialDialog: () => <div data-testid="edit-dialog" />,
}))

function makeCredential(overrides: Record<string, unknown> = {}) {
  return {
    id: "cred_1",
    name: "STRIPE_API_KEY",
    description: null,
    type: "API_KEY",
    provider: "CUSTOM_CLI",
    status: "ACTIVE",
    scope: "WORKSPACE",
    crew_id: null,
    crew_ids: [],
    account_label: null,
    account_email: null,
    username: null,
    token_expires_at: null,
    last_checked_at: null,
    last_error: null,
    last_used_at: null,
    last_used_ips: [],
    tags: [],
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    _count_agent_credentials: 0,
    agent_names: [],
    mcp_used: false,
    ...overrides,
  }
}

function ok(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}

function fail(status: number): Response {
  return { ok: false, status, json: async () => ({}) } as unknown as Response
}

/** Default happy-path routing: one workspace, given credential list. */
function routeApi(credentials: unknown[]) {
  h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.startsWith("/api/v1/workspaces")) return ok([{ id: "ws1", name: "Test" }])
    if (url.startsWith("/api/v1/credentials?")) return ok(credentials)
    if (init?.method === "DELETE") return ok({})
    return ok([])
  })
}

beforeEach(() => {
  h.role = "OWNER"
  h.apiFetch.mockReset()
})

describe("load error state (C1)", () => {
  it("shows an error card with Retry instead of the empty state when the fetch fails", async () => {
    h.apiFetch.mockRejectedValue(new TypeError("fetch failed"))
    render(<CredentialsPage />)

    expect(await screen.findByText("Couldn't load credentials")).toBeInTheDocument()
    expect(screen.getByRole("alert")).toBeInTheDocument()
    expect(screen.queryByText("No credentials yet")).not.toBeInTheDocument()

    // Retry recovers once the API is healthy again.
    routeApi([makeCredential()])
    fireEvent.click(screen.getByRole("button", { name: /retry/i }))
    expect(await screen.findByText("STRIPE_API_KEY")).toBeInTheDocument()
    expect(screen.queryByText("Couldn't load credentials")).not.toBeInTheDocument()
  })

  it("shows the error state on a non-2xx credentials response", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/v1/workspaces")) return ok([{ id: "ws1", name: "Test" }])
      return fail(500)
    })
    render(<CredentialsPage />)

    expect(await screen.findByText("Couldn't load credentials")).toBeInTheDocument()
    expect(screen.getByText(/HTTP 500/)).toBeInTheDocument()
    expect(screen.queryByText("No credentials yet")).not.toBeInTheDocument()
  })
})

describe("RBAC-gated row actions (C2)", () => {
  it.each(["VIEWER", "MEMBER"] as const)(
    "%s sees neither Edit/Delete actions nor bulk-select checkboxes",
    async (role) => {
      h.role = role
      routeApi([makeCredential()])
      render(<CredentialsPage />)

      expect(await screen.findByText("STRIPE_API_KEY")).toBeInTheDocument()
      expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument()
      expect(screen.queryByRole("button", { name: "Delete" })).not.toBeInTheDocument()
      expect(screen.queryByRole("checkbox")).not.toBeInTheDocument()
    },
  )

  it("MANAGER sees Edit but not Delete (backend delete is OWNER/ADMIN)", async () => {
    h.role = "MANAGER"
    routeApi([makeCredential()])
    render(<CredentialsPage />)

    expect(await screen.findByText("STRIPE_API_KEY")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Edit" })).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Delete" })).not.toBeInTheDocument()
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument()
  })

  it("OWNER sees Edit, Delete and the bulk-select checkbox", async () => {
    routeApi([makeCredential()])
    render(<CredentialsPage />)

    expect(await screen.findByText("STRIPE_API_KEY")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Edit" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Delete" })).toBeInTheDocument()
    expect(screen.getByRole("checkbox", { name: "Select STRIPE_API_KEY" })).toBeInTheDocument()
  })

  it("CASL sanity: MANAGER lacks delete, OWNER has it", () => {
    expect(defineAbilitiesFor("MANAGER" as OrgRole).can("delete", "Credential")).toBe(false)
    expect(defineAbilitiesFor("OWNER" as OrgRole).can("delete", "Credential")).toBe(true)
  })
})

describe("pending-approval deep-link (C4)", () => {
  it("renders the Pending approval badge as a link to /inbox", async () => {
    routeApi([makeCredential({ status: "PENDING_APPROVAL" })])
    render(<CredentialsPage />)

    const link = await screen.findByRole("link", { name: /pending approval/i })
    expect(link).toHaveAttribute("href", "/inbox")
  })
})

describe("bulk delete partial failure (C6)", () => {
  it("reports X deleted / Y failed and keeps failed items selected", async () => {
    const creds = [
      makeCredential({ id: "cred_a", name: "KEY_A" }),
      makeCredential({ id: "cred_b", name: "KEY_B" }),
    ]
    h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.startsWith("/api/v1/workspaces")) return ok([{ id: "ws1", name: "Test" }])
      if (url.startsWith("/api/v1/credentials?")) return ok(creds)
      if (init?.method === "DELETE") {
        return url.includes("cred_b") ? fail(500) : ok({})
      }
      return ok([])
    })
    render(<CredentialsPage />)

    fireEvent.click(await screen.findByRole("checkbox", { name: "Select KEY_A" }))
    fireEvent.click(screen.getByRole("checkbox", { name: "Select KEY_B" }))
    expect(screen.getByText("2 selected")).toBeInTheDocument()

    // Bulk bar → confirm dialog → confirm.
    const bulkBar = screen.getByText("2 selected").parentElement!
    fireEvent.click(within(bulkBar).getByRole("button", { name: "Delete" }))
    fireEvent.click(await screen.findByRole("button", { name: "Delete 2" }))

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("1 deleted, 1 failed"))
    })
    // The failed credential stays selected for one-click retry.
    expect(screen.getByRole("checkbox", { name: "Select KEY_B" })).toBeChecked()
    expect(screen.getByRole("checkbox", { name: "Select KEY_A" })).not.toBeChecked()
    expect(toast.success).not.toHaveBeenCalled()
  })

  it("reports success and clears the selection when everything deletes", async () => {
    routeApi([makeCredential({ id: "cred_a", name: "KEY_A" })])
    render(<CredentialsPage />)

    fireEvent.click(await screen.findByRole("checkbox", { name: "Select KEY_A" }))
    const bulkBar = screen.getByText("1 selected").parentElement!
    fireEvent.click(within(bulkBar).getByRole("button", { name: "Delete" }))
    fireEvent.click(await screen.findByRole("button", { name: "Delete 1" }))

    await waitFor(() => {
      expect(toast.success).toHaveBeenCalledWith(expect.stringContaining("1 credential deleted"))
    })
    expect(screen.queryByText("1 selected")).not.toBeInTheDocument()
  })

  // #1085 item 1: a 404 (another admin deleted it first) is success, not a
  // failure — the row must not linger selected as a phantom.
  it("treats a 404 DELETE as success, not a phantom failure", async () => {
    const creds = [makeCredential({ id: "cred_a", name: "KEY_A" })]
    h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.startsWith("/api/v1/workspaces")) return ok([{ id: "ws1", name: "Test" }])
      if (url.startsWith("/api/v1/credentials?")) return ok(creds)
      if (init?.method === "DELETE") return fail(404) // already gone
      return ok([])
    })
    render(<CredentialsPage />)

    fireEvent.click(await screen.findByRole("checkbox", { name: "Select KEY_A" }))
    const bulkBar = screen.getByText("1 selected").parentElement!
    fireEvent.click(within(bulkBar).getByRole("button", { name: "Delete" }))
    fireEvent.click(await screen.findByRole("button", { name: "Delete 1" }))

    await waitFor(() => {
      expect(toast.success).toHaveBeenCalledWith(expect.stringContaining("1 credential deleted"))
    })
    expect(toast.error).not.toHaveBeenCalled()
    expect(screen.queryByText("1 selected")).not.toBeInTheDocument()
  })
})

// #1085 item 2: a refresh failure after data is on screen must not replace the
// loaded list with the full-page error card — it toasts and keeps the list.
describe("transient refresh failure (C-refresh)", () => {
  it("toasts and keeps the list instead of showing the error card", async () => {
    const creds = [makeCredential({ id: "cred_a", name: "KEY_A" })]
    // Key the failure on "a delete has happened" rather than a call counter —
    // React double-invokes the load effect in tests, so a counter is fragile.
    let deletedHappened = false
    h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.startsWith("/api/v1/workspaces")) return ok([{ id: "ws1", name: "Test" }])
      if (url.startsWith("/api/v1/credentials?")) {
        // Initial load(s) succeed; only the post-delete refresh fails.
        return deletedHappened ? Promise.reject(new TypeError("fetch failed")) : ok(creds)
      }
      if (init?.method === "DELETE") {
        deletedHappened = true
        return ok({})
      }
      return ok([])
    })
    render(<CredentialsPage />)

    // Delete the only credential — success path fires handleRefresh, which fails.
    fireEvent.click(await screen.findByRole("checkbox", { name: "Select KEY_A" }))
    const bulkBar = screen.getByText("1 selected").parentElement!
    fireEvent.click(within(bulkBar).getByRole("button", { name: "Delete" }))
    fireEvent.click(await screen.findByRole("button", { name: "Delete 1" }))

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("Network error"))
    })
    // The full-page error card must NOT appear on a background refresh failure.
    expect(screen.queryByText("Couldn't load credentials")).not.toBeInTheDocument()
  })
})
