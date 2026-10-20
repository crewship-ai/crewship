// Edit folder (#3706): after a folder exists its policy can still be changed
// from the Vault. Owners and admins edit who can reveal and the lowest tier;
// anyone who may organise folders edits the name and reminder. Only changed
// fields are sent.

import * as React from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { fireEvent, render, screen, cleanup, waitFor } from "@testing-library/react"

import { EditFolderDialog } from "../edit-folder-dialog"
import type { CredentialFolder } from "@/lib/credentials/folders"

const FOLDER: CredentialFolder = {
  id: "prod", name: "Production", parent_id: null, description: "", reveal_policy: "inherit",
  remind_after_days: 0, default_security_level: 2, min_security_level: 1, credential_count: 3,
}

function dialog(canSetPolicy: boolean) {
  const onSave = vi.fn(async () => FOLDER)
  const onOpenChange = vi.fn()
  render(<EditFolderDialog folder={FOLDER} onOpenChange={onOpenChange} canSetPolicy={canSetPolicy} onSave={onSave} />)
  return { onSave, onOpenChange }
}

beforeEach(() => cleanup())

describe("Edit folder dialog", () => {
  it("prefills the folder and sends only the changed policy fields", async () => {
    const { onSave, onOpenChange } = dialog(true)
    expect(screen.getByLabelText("Name")).toHaveValue("Production")
    fireEvent.change(screen.getByLabelText("Who can reveal"), { target: { value: "nobody" } })
    fireEvent.change(screen.getByLabelText("Lowest allowed tier"), { target: { value: "3" } })
    fireEvent.change(screen.getByLabelText(/Remind to replace/), { target: { value: "90" } })
    expect(screen.getByText(/raised to L3 when you save/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(onSave).toHaveBeenCalledWith("prod", {
      reveal_policy: "nobody", min_security_level: 3, default_security_level: 3, remind_after_days: 90,
    }))
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
  })

  it("keeps Save off until something changes", () => {
    dialog(true)
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled()
  })

  it("hides reveal and tier from people who may only organise", async () => {
    const { onSave } = dialog(false)
    expect(screen.queryByLabelText("Who can reveal")).toBeNull()
    expect(screen.queryByLabelText("Lowest allowed tier")).toBeNull()
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Prod" } })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(onSave).toHaveBeenCalledWith("prod", { name: "Prod" }))
  })

  it("shows the server's refusal and stays open", async () => {
    const onSave = vi.fn(async () => { throw new Error("Moving this folder would loosen its protection") })
    const onOpenChange = vi.fn()
    render(<EditFolderDialog folder={FOLDER} onOpenChange={onOpenChange} canSetPolicy onSave={onSave} />)
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Prod" } })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    expect(await screen.findByText(/loosen its protection/)).toBeInTheDocument()
    expect(onOpenChange).not.toHaveBeenCalledWith(false)
  })
})
