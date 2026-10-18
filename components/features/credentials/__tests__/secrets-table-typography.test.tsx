// PAR-080 (Harbor 2 v67): the Secrets table draws the tier as a grey L1/L2
// chip, the state as a sans pill, and keeps only "Last used" in mono.
import { describe, expect, it, vi } from "vitest"
import { render, screen } from "@testing-library/react"
import { SecretsTable, type VaultSecret } from "../secrets-table"

vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => <span /> }))

const row = (id: string, level: number): VaultSecret =>
  ({ id, name: `S${id}`, type: "API_KEY", provider: "CUSTOM_CLI", status: "ACTIVE", scope: "WORKSPACE", security_level: level, sensitivity: "STANDARD", last_used_at: null, agent_ids: [], agent_names: [], tags: [] }) as unknown as VaultSecret

function renderTable() {
  render(
    <SecretsTable rows={[row("1", 2), row("2", 3)]} group="none" onGroupChange={vi.fn()} selectable={false} selectedIds={new Set()} onToggle={vi.fn()} onToggleAll={vi.fn()} onOpen={vi.fn()} />,
  )
}

describe("SecretsTable typography (v67)", () => {
  it("draws the state pill in sans", () => {
    renderTable()
    const pill = document.querySelector("[data-slot=status-pill]")!
    expect(pill.className).toContain("font-sans")
    expect(pill.className).not.toContain("font-mono")
  })

  it("draws L1 and L2 as a grey sans chip, L3 keeps its warning colour", () => {
    renderTable()
    const l2 = screen.getByText("L2")
    expect(l2.className).not.toContain("font-mono")
    expect(l2.className).not.toContain("text-info")
    expect(l2.className).toContain("text-muted-foreground")
    expect(screen.getByText("L3").className).toContain("text-warn")
  })

  it("keeps Last used mono", () => {
    renderTable()
    expect(screen.getAllByText("never")[0].className).toContain("font-mono")
  })
})
