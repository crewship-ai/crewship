import { fireEvent, render, screen } from "@testing-library/react"
import { expect, it, vi } from "vitest"
import { WordCraftCatalogRibbon } from "./catalog-ribbon"
import { defaultWordCraftCatalog, type ParticipantCapabilities } from "./command-gate"

const commandIds = ["format.bold", "format.italic", "file.open"]
const participant: ParticipantCapabilities = { engineSha: defaultWordCraftCatalog.engine.sha, catalogVersion: defaultWordCraftCatalog.version, allowedCommands: ["format.bold", "format.italic", "file.open"] }

it("shows unqualified catalog buttons disabled with the requested explanation", () => {
  const onCommand = vi.fn()
  render(<WordCraftCatalogRibbon commandIds={commandIds} participant={participant} onCommand={onCommand} />)
  for (const button of screen.getAllByRole("button")) {
    expect(button).toBeDisabled()
    expect(button).toHaveAccessibleDescription("nemáte oprávnění")
    fireEvent.click(button)
  }
  expect(onCommand).not.toHaveBeenCalled()
})
it("renders qualified role-allowed actions active and keeps forbidden commands disabled", () => {
  const catalog = { ...defaultWordCraftCatalog, commands: defaultWordCraftCatalog.commands.map(command => ({ ...command, qualification: "qualified" })) }
  const onCommand = vi.fn()
  const { rerender } = render(<WordCraftCatalogRibbon catalog={catalog} commandIds={commandIds} participant={{ ...participant, allowedCommands: ["format.bold", "file.open"] }} onCommand={onCommand} />)
  expect(screen.getByRole("button", { name: "Bold" })).toBeEnabled()
  expect(screen.getByRole("button", { name: "Italic" })).toBeDisabled()
  expect(screen.getByRole("button", { name: "Open" })).toBeDisabled()
  fireEvent.click(screen.getByRole("button", { name: "Bold" }))
  expect(onCommand).toHaveBeenCalledExactlyOnceWith("format.bold", {})
  rerender(<WordCraftCatalogRibbon catalog={catalog} commandIds={commandIds} participant={{ ...participant, allowedCommands: [] }} onCommand={onCommand} />)
  expect(screen.getByRole("button", { name: "Bold" })).toBeDisabled()
  fireEvent.click(screen.getByRole("button", { name: "Bold" }))
  expect(onCommand).toHaveBeenCalledOnce()
})
