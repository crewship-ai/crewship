// Vault folders in the secondary panel (#3479; v67 V-PANEL "Folders"): the tree
// sits under Views, a click filters the list, and "+" only exists for people
// who may create folders.

import * as React from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { fireEvent, render, screen, cleanup } from "@testing-library/react"

import { VaultPanel } from "../vault-panel"
import { EMPTY_CREDENTIAL_FILTERS } from "@/lib/credentials/facets"
import type { CredentialFolder } from "@/lib/credentials/folders"

const folder = (id: string, name: string, parent: string | null, count: number): CredentialFolder => ({
  id, name, parent_id: parent, description: "", reveal_policy: "inherit", remind_after_days: 0,
  default_security_level: null, min_security_level: 1, credential_count: count,
})
const FOLDERS = [folder("prod", "Production", null, 2), folder("pay", "Payments", "prod", 1), folder("ops", "Ops", null, 0)]

function panel(over: Partial<React.ComponentProps<typeof VaultPanel>> = {}) {
  const props: React.ComponentProps<typeof VaultPanel> = {
    filters: EMPTY_CREDENTIAL_FILTERS,
    onFiltersChange: vi.fn(),
    viewCounts: { all: 9, attention: 1, recent: 2, unused: 3, "missing-tool": 0 },
    shapes: [], brands: [], crews: [], tiers: [], classes: [], scopes: [], tags: [], agents: [],
    crewsById: {},
    folders: FOLDERS,
    spaces: null,
    ...over,
  }
  render(<VaultPanel {...props} />)
  return props
}

beforeEach(() => cleanup())

describe("Vault panel folders", () => {
  it("lists folders with the subtree count and nests subfolders", () => {
    panel()
    expect(screen.getByRole("button", { name: /^Production/ })).toHaveTextContent("3")
    expect(screen.getByRole("button", { name: /^Payments/ })).toHaveTextContent("1")
    expect(screen.getByRole("button", { name: /^Ops/ })).toHaveTextContent("0")
  })

  it("picking a folder filters to it, picking it again clears", () => {
    const props = panel()
    fireEvent.click(screen.getByRole("button", { name: /^Payments/ }))
    expect(props.onFiltersChange).toHaveBeenCalledWith(expect.objectContaining({ folder: ["pay"] }))
    cleanup()
    const again = panel({ filters: { ...EMPTY_CREDENTIAL_FILTERS, folder: ["pay"] } })
    fireEvent.click(screen.getByRole("button", { name: /^Payments/ }))
    expect(again.onFiltersChange).toHaveBeenCalledWith(expect.objectContaining({ folder: [] }))
  })

  it("offers New folder only when the caller can create one", () => {
    panel()
    expect(screen.queryByRole("button", { name: "New folder" })).toBeNull()
    cleanup()
    const onNewFolder = vi.fn()
    panel({ onNewFolder })
    fireEvent.click(screen.getByRole("button", { name: "New folder" }))
    expect(onNewFolder).toHaveBeenCalled()
  })

  it("offers Edit folder for the open folder only to people who may edit", () => {
    panel({ filters: { ...EMPTY_CREDENTIAL_FILTERS, folder: ["pay"] } })
    expect(screen.queryByRole("button", { name: "Edit folder Payments" })).toBeNull()
    cleanup()
    const onEditFolder = vi.fn()
    panel({ onEditFolder })
    expect(screen.queryByRole("button", { name: /^Edit folder/ })).toBeNull()
    cleanup()
    panel({ onEditFolder, filters: { ...EMPTY_CREDENTIAL_FILTERS, folder: ["pay"] } })
    fireEvent.click(screen.getByRole("button", { name: "Edit folder Payments" }))
    expect(onEditFolder).toHaveBeenCalledWith(expect.objectContaining({ id: "pay" }))
  })

  it("shows the Folders section empty state for someone who can create", () => {
    panel({ folders: [], onNewFolder: vi.fn() })
    expect(screen.getByText("No folders yet.")).toBeInTheDocument()
  })

  it("draws nothing for someone who cannot create when there are no folders", () => {
    panel({ folders: [] })
    expect(screen.queryByText("Folders")).toBeNull()
  })
})
