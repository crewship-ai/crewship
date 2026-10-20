import { render, screen } from "@testing-library/react"
import { expect, it } from "vitest"
import { DocumentSaveState, durabilityLabel } from "../document-save-state"

it.each([
  ["confirmed", "Saved", "Confirmed by server"],
  ["confirmed", "Loading document…", undefined],
  ["writing", "Saving…", "Saving to this browser… · not yet confirmed by server"],
  ["local", "Disconnected · changes are unsaved", "Saved locally in this browser · not yet confirmed by server"],
  ["unsaved", "Private document storage failed · changes are unsaved", "Not stored in this browser or on the server · keep this tab open"],
  ["held", "Unsaved document draft found · recover before reopening", "An earlier unsent draft is held in this browser and was not restored"],
] as const)("labels %s/%s", (durability, status, label) => { expect(durabilityLabel(durability, status)).toBe(label) })

it("renders one status region carrying both texts", () => {
  render(<DocumentSaveState status="Disconnected · changes are unsaved" durability="local" />)
  expect(screen.getAllByRole("status")).toHaveLength(1)
  expect(screen.getByRole("status")).toHaveTextContent("Saved locally in this browser")
})
