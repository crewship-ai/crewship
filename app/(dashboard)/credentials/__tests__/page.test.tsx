// Tests for the /credentials page — RBAC-gated row actions, the
// list-load error state (must never masquerade as "no credentials
// yet"), the pending-approval inbox deep-link, and bulk-delete
// partial-failure reporting.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"
import { toast } from "sonner"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"
import { _resetWorkspaceStoreForTests, useWorkspace } from "@/hooks/use-workspace"
import CredentialsPage from "../page"

/** The rail lists the same credential names as the table, so an unscoped
 *  text query matches twice. Scope to the labelled list region — which is
 *  what the assertion always meant. */
function list() {
  return within(screen.getByRole("region", { name: /credential list/i }))
}

/** Async twin of list(): waits for the region itself, so it can be used in a
 *  state the list has not rendered into yet (a retry, a workspace switch).
 *  list() resolves the region synchronously and would throw first. */
/**
 * Enter selection mode.
 *
 * Ticking rows is a MODE now, not the resting state of the rail: a checkbox on
 * every row all the time reads as "this list is a thing you tick", when it is
 * overwhelmingly a thing you click — and it leaves a bulk-delete one mis-click
 * from every secret in the vault. Every bulk test opens the mode first,
 * because that is what a user now does.
 */
async function enterSelectMode() {
  fireEvent.click(await screen.findByRole("button", { name: /select several credentials/i }))
}

async function inList(name: string) {
  const region = await screen.findByRole("region", { name: /credential list/i })
  return within(region).findByText(name)
}


// Hoisted holder so vi.mock factories can read per-test state.
const h = vi.hoisted(() => ({
  role: "OWNER" as string,
  capabilities: [] as string[],
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
  const { hasCapability } = await import("@/lib/capabilities")
  return {
    useAbilities: () => ({
      abilities: defineAbilitiesFor(h.role as never),
      role: h.role,
      capabilities: h.capabilities,
      hasCapability: (cap: never) => hasCapability(h.capabilities, cap),
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
vi.mock("@/components/features/credentials/connect-oauth-dialog", () => ({
  ConnectOAuthDialog: ({ open }: { open: boolean }) => (
    <div data-testid="connect-oauth-dialog" data-open={open ? "true" : "false"} />
  ),
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
  h.capabilities = []
  h.apiFetch.mockReset()
  // The workspace store is a module singleton with a localStorage-backed
  // selection — reset both so each test starts from a clean slate (#1033).
  _resetWorkspaceStoreForTests()
  try { localStorage.clear() } catch { /* jsdom */ }
})

describe("load error state (C1)", () => {
  it("shows an error card with Retry instead of the empty state when the fetch fails", async () => {
    // Workspace resolves; the credentials fetch is what fails.
    h.apiFetch.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/v1/workspaces")) return ok([{ id: "ws1", name: "Test" }])
      throw new TypeError("fetch failed")
    })
    render(<CredentialsPage />)

    expect(await screen.findByText("Couldn't load credentials")).toBeInTheDocument()
    expect(screen.getByRole("alert")).toBeInTheDocument()
    expect(screen.queryByText("No credentials yet")).not.toBeInTheDocument()

    // Retry recovers once the API is healthy again.
    routeApi([makeCredential()])
    fireEvent.click(screen.getByRole("button", { name: /retry/i }))
    expect(await inList("STRIPE_API_KEY")).toBeInTheDocument()
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

describe("multi-workspace (#1033)", () => {
  it("loads credentials for the workspace the shared store resolves", async () => {
    const requested: string[] = []
    h.apiFetch.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/v1/workspaces")) {
        return ok([{ id: "ws-alpha", name: "Alpha" }, { id: "ws-beta", name: "Beta" }])
      }
      if (url.startsWith("/api/v1/credentials?")) {
        requested.push(new URL(url, "http://x").searchParams.get("workspace_id") ?? "")
        return ok([makeCredential({ id: "c1", name: "ALPHA_KEY" })])
      }
      return ok([])
    })
    render(<CredentialsPage />)

    // The page delegates workspace resolution to useWorkspace and fetches
    // credentials for whichever workspace the store selected — no longer its
    // own hardcoded orgs[0]. (Selection/persistence is use-workspace's own
    // concern and suite.)
    expect(await inList("ALPHA_KEY")).toBeInTheDocument()
    expect(requested.length).toBeGreaterThan(0)
    expect(requested.every((id) => id === "ws-alpha")).toBe(true)
  })
})

// Harness that renders the real useWorkspace() store alongside the page so
// tests can drive a workspace switch the same way the top-bar switcher does.
function renderWithSwitcher() {
  function Harness() {
    const { setWorkspaceId } = useWorkspace()
    return (
      <>
        <button onClick={() => setWorkspaceId("ws-b")}>switch to B</button>
        <CredentialsPage />
      </>
    )
  }
  return render(<Harness />)
}

describe("stale response guard on workspace switch (#1156)", () => {
  it("ignores a slow response from the previous workspace after switching", async () => {
    let resolveA: (v: Response) => void = () => {}
    const pendingA = new Promise<Response>((resolve) => { resolveA = resolve })
    h.apiFetch.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/v1/workspaces")) {
        return ok([{ id: "ws-a", name: "A" }, { id: "ws-b", name: "B" }])
      }
      if (url.includes("workspace_id=ws-a")) return pendingA
      if (url.includes("workspace_id=ws-b")) {
        return ok([makeCredential({ id: "cb", name: "BETA_KEY" })])
      }
      return ok([])
    })

    renderWithSwitcher()

    // Wait until the (still-pending) ws-a fetch has actually been issued.
    await waitFor(() => {
      expect(h.apiFetch.mock.calls.some((c) => String(c[0]).includes("workspace_id=ws-a"))).toBe(true)
    })

    fireEvent.click(screen.getByText("switch to B"))

    // ws-b's fetch resolves quickly and should win.
    expect(await inList("BETA_KEY")).toBeInTheDocument()

    // Now let the slow ws-a response resolve — it must NOT clobber the
    // already-displayed ws-b rows, even though it resolves later.
    resolveA(ok([makeCredential({ id: "ca", name: "ALPHA_KEY" })]))
    await new Promise((r) => setTimeout(r, 0))
    await new Promise((r) => setTimeout(r, 0))

    expect(screen.queryByText("ALPHA_KEY")).not.toBeInTheDocument()
    expect(list().getByText("BETA_KEY")).toBeInTheDocument()
  })
})

describe("selection cleared on workspace switch (#1156)", () => {
  it("clears bulk-select ids when the workspace changes", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/v1/workspaces")) {
        return ok([{ id: "ws-a", name: "A" }, { id: "ws-b", name: "B" }])
      }
      if (url.includes("workspace_id=ws-a")) {
        return ok([makeCredential({ id: "ca", name: "ALPHA_KEY" })])
      }
      if (url.includes("workspace_id=ws-b")) {
        return ok([makeCredential({ id: "cb", name: "BETA_KEY" })])
      }
      return ok([])
    })

    renderWithSwitcher()

    expect(await inList("ALPHA_KEY")).toBeInTheDocument()
    await enterSelectMode()
    fireEvent.click(screen.getByLabelText("Select ALPHA_KEY"))
    expect(await screen.findByText("1 selected")).toBeInTheDocument()

    fireEvent.click(screen.getByText("switch to B"))

    expect(await inList("BETA_KEY")).toBeInTheDocument()
    // Both the ids AND the mode reset: a tick left over from another
    // workspace would target credential ids this one does not have.
    expect(screen.queryByText(/selected/)).not.toBeInTheDocument()
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument()
  })
})

// Per-row Edit and Delete buttons went with the table. Edit now lives on the
// credential's own detail (one Edit, on the thing being edited), and delete is
// either the detail's Settings tab or the rail's bulk selection. What the LIST
// still gates is the bulk-select checkbox, and it gates it exactly where the
// backend does: DELETE is OWNER/ADMIN.
describe("RBAC-gated list actions (C2)", () => {
  it.each(["VIEWER", "MEMBER"] as const)(
    "%s sees neither Edit/Delete actions nor bulk-select checkboxes",
    async (role) => {
      h.role = role
      routeApi([makeCredential()])
      render(<CredentialsPage />)

      expect(await inList("STRIPE_API_KEY")).toBeInTheDocument()
      expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument()
      expect(screen.queryByRole("button", { name: "Delete" })).not.toBeInTheDocument()
      // Neither the checkboxes nor the toggle that would produce them.
      expect(screen.queryByRole("checkbox")).not.toBeInTheDocument()
      expect(
        screen.queryByRole("button", { name: /select several credentials/i }),
      ).not.toBeInTheDocument()
    },
  )

  it("MANAGER gets no bulk-select checkbox (backend delete is OWNER/ADMIN)", async () => {
    h.role = "MANAGER"
    routeApi([makeCredential()])
    render(<CredentialsPage />)

    expect(await inList("STRIPE_API_KEY")).toBeInTheDocument()
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: /select several credentials/i }),
    ).not.toBeInTheDocument()
  })

  it("OWNER can turn selection on, and it is off until they do", async () => {
    routeApi([makeCredential()])
    render(<CredentialsPage />)

    expect(await inList("STRIPE_API_KEY")).toBeInTheDocument()
    // Not until asked: the rail offers the toggle, and the checkboxes follow.
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument()
    await enterSelectMode()
    expect(screen.getByRole("checkbox", { name: "Select STRIPE_API_KEY" })).toBeInTheDocument()
  })

  it("CASL sanity: MANAGER lacks delete, OWNER has it", () => {
    expect(defineAbilitiesFor("MANAGER" as OrgRole).can("delete", "Credential")).toBe(false)
    expect(defineAbilitiesFor("OWNER" as OrgRole).can("delete", "Credential")).toBe(true)
  })
})

describe("OAuth connect entry point (#1034)", () => {
  it("shows Connect via OAuth next to Add secret and opens the dialog", async () => {
    routeApi([makeCredential()])
    render(<CredentialsPage />)

    expect(await inList("STRIPE_API_KEY")).toBeInTheDocument()
    const btn = screen.getByRole("button", { name: /connect via oauth/i })
    expect(screen.getByTestId("connect-oauth-dialog")).toHaveAttribute("data-open", "false")
    fireEvent.click(btn)
    expect(screen.getByTestId("connect-oauth-dialog")).toHaveAttribute("data-open", "true")
  })

  it("MEMBER with an explicit credential.create grant sees the create actions", async () => {
    // Backend honors credential.create for lower roles
    // (requireRoleOrCapabilityOrForbid) — the UI must not hide what
    // the API would accept.
    h.role = "MEMBER"
    h.capabilities = ["chat", "credential.create"]
    routeApi([makeCredential()])
    render(<CredentialsPage />)

    expect(await inList("STRIPE_API_KEY")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /connect via oauth/i })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /add secret/i })).toBeInTheDocument()
  })

  it("MEMBER without the grant sees neither create action", async () => {
    h.role = "MEMBER"
    h.capabilities = ["chat"]
    routeApi([makeCredential()])
    render(<CredentialsPage />)

    expect(await inList("STRIPE_API_KEY")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /connect via oauth/i })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /add secret/i })).not.toBeInTheDocument()
  })
})

// Approving an agent-proposed credential is an inbox action, so the row that
// reports one has to LINK there. The badge used to carry the link from the
// table; the attention queue on the overview carries it now, and the reason
// text is the label.
describe("pending-approval deep-link (C4)", () => {
  it("links a pending credential straight to /inbox", async () => {
    routeApi([makeCredential({ status: "PENDING_APPROVAL" })])
    render(<CredentialsPage />)

    const link = await screen.findByRole("link", { name: /approve or reject it/i })
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

    await enterSelectMode()
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

    await enterSelectMode()
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

    await enterSelectMode()
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

// #1162: confirmDeleteCredential (single-item delete) still did
// `if (res.ok) refresh` — a 404 (already deleted by another admin) was
// silently swallowed: no toast, no refresh, stale row lingers. Apply the
// same 404-as-already-gone semantics bulkDelete got in #1085.
// The single-credential delete moved to the detail sheet's Settings tab when
// the table's row actions went. Its #1162 404-as-already-gone handling is
// tested against the real component in
// components/features/credentials/__tests__/credential-detail-sheet.test.tsx.

// #1085 item 2: a refresh failure after data is on screen must not replace the
// loaded list with the full-page error card — it toasts and keeps the list.
// The selection survives a filter change, and the rail only draws what the
// filters leave. Ticking rows, then narrowing, then confirming used to DELETE
// secrets that were no longer on screen — counted in the dialog, named nowhere.
describe("bulk delete respects the filters (CodeRabbit #1948)", () => {
  it("deletes only what the rail is still showing", async () => {
    const creds = [
      makeCredential({ id: "cred_a", name: "KEY_A", provider: "GITHUB" }),
      makeCredential({ id: "cred_b", name: "KEY_B", provider: "ANTHROPIC" }),
    ]
    const deleted: string[] = []
    h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.startsWith("/api/v1/workspaces")) return ok([{ id: "ws1", name: "Test" }])
      if (url.startsWith("/api/v1/credentials?")) return ok(creds)
      if (init?.method === "DELETE") {
        deleted.push(String(url).split("/")[4].split("?")[0])
        return ok({})
      }
      return ok([])
    })
    render(<CredentialsPage />)

    expect(await inList("KEY_A")).toBeInTheDocument()
    await enterSelectMode()
    fireEvent.click(screen.getByRole("checkbox", { name: "Select KEY_A" }))
    fireEvent.click(screen.getByRole("checkbox", { name: "Select KEY_B" }))
    expect(await screen.findByText("2 selected")).toBeInTheDocument()

    // Narrow so KEY_A is no longer on screen.
    fireEvent.change(screen.getByPlaceholderText(/search a secret or tool/i), {
      target: { value: "KEY_B" },
    })
    await waitFor(() => expect(list().queryByText("KEY_A")).not.toBeInTheDocument())

    fireEvent.click(screen.getByRole("button", { name: /^Delete$/ }))
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.click(within(dialog).getByRole("button", { name: /^Delete/ }))

    await waitFor(() => expect(deleted.length).toBeGreaterThan(0))
    expect(deleted).toEqual(["cred_b"])
  })
})

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
    await enterSelectMode()
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
