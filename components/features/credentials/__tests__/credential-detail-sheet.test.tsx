// Tests for CredentialDetailSheet RBAC gating — the Settings tab must
// only offer actions the backend will actually accept for the caller's
// role: value update/test = MANAGER+ ("update"), rotate = OWNER/ADMIN
// ("manage"), delete = OWNER/ADMIN ("delete"). A MANAGER must never
// see Rotate/Delete buttons that 403 on click.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { CredentialDetailSheet } from "../credential-detail-sheet"

const h = vi.hoisted(() => ({
  role: "OWNER" as string,
  capabilities: [] as string[],
  apiFetch: vi.fn(),
}))

vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => h.apiFetch(...args),
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

const credential = {
  id: "cred_1",
  name: "STRIPE_API_KEY",
  description: null,
  type: "API_KEY",
  provider: "CUSTOM_CLI",
  status: "ACTIVE",
  scope: "WORKSPACE",
  account_label: null,
  account_email: null,
  username: null,
  token_expires_at: null,
  last_checked_at: null,
  last_used_at: null,
  last_used_ips: [],
  last_error: null,
  tags: [],
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  agent_names: [],
  _count_agent_credentials: 0,
  mcp_used: false,
}

function renderSheet() {
  return render(
    <CredentialDetailSheet
      workspaceId="ws1"
      credential={credential}
      open
      onOpenChange={() => {}}
      onRefresh={() => {}}
      onRotate={() => {}}
      onEdit={() => {}}
    />,
  )
}

function openSettingsTab() {
  const trigger = screen.getByRole("tab", { name: /settings/i })
  fireEvent.mouseDown(trigger)
  fireEvent.click(trigger)
}

beforeEach(() => {
  h.capabilities = []
  h.apiFetch.mockReset()
  h.apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => [] })
})

describe("Settings tab gating by role", () => {
  it("OWNER sees update value, rotate and delete", () => {
    h.role = "OWNER"
    renderSheet()
    openSettingsTab()

    expect(screen.getByText("Update value")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /rotate with grace overlap/i })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /delete credential/i })).toBeInTheDocument()
  })

  it("MANAGER keeps update value but loses rotate and delete (backend requires manage)", () => {
    h.role = "MANAGER"
    renderSheet()
    openSettingsTab()

    expect(screen.getByText("Update value")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /rotate with grace overlap/i })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /delete credential/i })).not.toBeInTheDocument()
    // ...and gets told why, instead of a silent gap.
    expect(screen.getByText(/require a workspace admin/i)).toBeInTheDocument()
  })

  it("MANAGER does not trigger the rotations-history fetch it can't render", () => {
    h.role = "MANAGER"
    renderSheet()
    openSettingsTab()

    const rotationCalls = h.apiFetch.mock.calls.filter(([url]) =>
      String(url).includes("/rotations"),
    )
    expect(rotationCalls).toHaveLength(0)
  })

  it("VIEWER sees no mutation affordances at all", () => {
    h.role = "VIEWER"
    renderSheet()
    openSettingsTab()

    expect(screen.queryByText("Update value")).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /rotate with grace overlap/i })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /delete credential/i })).not.toBeInTheDocument()
    expect(screen.getByText(/don't have permission to modify/i)).toBeInTheDocument()
  })

  it("VIEWER does not get the header Edit button", () => {
    h.role = "VIEWER"
    renderSheet()
    expect(screen.queryByRole("button", { name: /edit/i })).not.toBeInTheDocument()
  })

  it("MANAGER gets the header Edit button (PATCH allows MANAGER)", () => {
    h.role = "MANAGER"
    renderSheet()
    expect(screen.getByRole("button", { name: /^edit$/i })).toBeInTheDocument()
  })

  // #1034 — the backend honors the credential.rotate capability for
  // lower roles (requireRoleOrCapabilityOrForbid, #1028); the sheet
  // must surface Rotate for a capability-holding MANAGER instead of
  // gating on role alone.
  it("MANAGER with credential.rotate capability sees the Rotate button", () => {
    h.role = "MANAGER"
    h.capabilities = ["chat", "credential.rotate"]
    renderSheet()
    openSettingsTab()

    expect(screen.getByRole("button", { name: /rotate with grace overlap/i })).toBeInTheDocument()
    // delete stays OWNER/ADMIN-only — the capability grants rotate, nothing more
    expect(screen.queryByRole("button", { name: /delete credential/i })).not.toBeInTheDocument()
  })
})
