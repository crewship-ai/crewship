import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"

import { SettingsTab } from "@/components/features/crews/crew-canvas-tabs/settings-tab"
import type { CrewRecord } from "@/components/features/crews/crew-canvas-tabs/types"

// =============================================================================
// #2118 — clearing the issue prefix from the web UI silently no-ops.
//
// `onSave={(v) => patch({ issue_prefix: (v || null) && v.toUpperCase()... })}`
// short-circuits an empty draft to `null`, which the server decodes as "field
// absent" and ignores (`crews_update.go` gates the write on `IssuePrefix !=
// nil`). `""` is the documented clear and is what the CLI sends. The fix
// sends `""` instead of `null`.
//
// The heavy Runtime/Policy/MCP/Escalations sub-panels are stubbed out — they
// fetch their own data on mount and are unrelated to the Profile field under
// test.
// =============================================================================

vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ role: "ADMIN" }),
}))

vi.mock("@/components/features/crews/crew-policy-controls", () => ({
  CrewPolicyControls: () => <div data-testid="policy-controls" />,
}))
vi.mock("@/components/features/crews/crew-container-config", () => ({
  CrewContainerConfig: () => <div data-testid="container-config" />,
}))
vi.mock("@/components/features/crews/crew-network-policy", () => ({
  CrewNetworkPolicy: () => <div data-testid="network-policy" />,
}))
vi.mock("@/components/features/crews/crew-mcp-config", () => ({
  CrewMCPConfig: () => <div data-testid="mcp-config" />,
}))
vi.mock("@/components/features/crews/crew-image-freshness", () => ({
  CrewImageFreshness: () => <div data-testid="image-freshness" />,
}))
vi.mock("@/components/features/crews/crew-runtime-config", () => ({
  CrewRuntimeConfig: () => <div data-testid="runtime-config" />,
}))
vi.mock("@/components/features/crews/crew-escalations", () => ({
  CrewEscalations: () => <div data-testid="escalations" />,
}))

const baseCrew = {
  id: "c1",
  workspace_id: "w1",
  name: "Ops",
  slug: "ops",
  description: "",
  color: null,
  icon: null,
  avatar_style: "bottts-neutral",
  issue_prefix: "ENG",
  network_mode: "free",
  allowed_domains: [],
  container_memory_mb: 2048,
  container_cpus: 1,
  container_ttl_hours: 24,
  runtime_image: null,
  devcontainer_config: null,
  mise_config: null,
  escalation_config: null,
  cached_image: null,
  created_at: new Date("2026-07-27").toISOString(),
  updated_at: new Date("2026-07-27").toISOString(),
} as unknown as CrewRecord

function renderTab(overrides: Partial<CrewRecord> = {}, patch = vi.fn().mockResolvedValue(undefined)) {
  render(
    <SettingsTab
      workspaceId="w1"
      crew={{ ...baseCrew, ...overrides }}
      agentsForCrew={[]}
      integrations={[]}
      patch={patch}
      applyAvatarStyle={vi.fn()}
      onDelete={vi.fn()}
    />,
  )
  // The row's toggle button carries no accessible name until it becomes an
  // <input> (the EditableField only sets aria-label on the editing control),
  // so locate it via the row's visible "Issue prefix" label instead.
  const row = screen.getByText("Issue prefix").closest("div.grid") as HTMLElement
  const field = within(row).getByRole("button")
  return { patch, row, field }
}

describe("Issue prefix field (#2118)", () => {
  it("sends an empty string, not null, when the prefix is cleared", async () => {
    const { patch, field, row } = renderTab({ issue_prefix: "ENG" })
    fireEvent.click(field)
    const input = within(row).getByLabelText("Issue prefix") as HTMLInputElement
    fireEvent.change(input, { target: { value: "" } })
    fireEvent.blur(input)
    await waitFor(() => expect(patch).toHaveBeenCalledWith({ issue_prefix: "" }))
    // Pin the exact bug: the call must NOT have gone out as null.
    expect(patch).not.toHaveBeenCalledWith({ issue_prefix: null })
  })

  it("uppercases and forwards a non-empty prefix, capped at 16 characters", async () => {
    const { patch, field, row } = renderTab({ issue_prefix: "" })
    fireEvent.click(field)
    const input = within(row).getByLabelText("Issue prefix") as HTMLInputElement
    fireEvent.change(input, { target: { value: "engineering-team" } }) // 17 chars
    fireEvent.blur(input)
    await waitFor(() =>
      expect(patch).toHaveBeenCalledWith({ issue_prefix: "ENGINEERING-TEAM".slice(0, 16) }),
    )
  })

  it("advertises the 16-character server limit, not the old 5-character cap", () => {
    renderTab()
    expect(screen.getByText(/max 16/i)).toBeInTheDocument()
    expect(screen.queryByText(/max 5/i)).toBeNull()
  })
})
