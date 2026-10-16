import { describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react"

import { AddPersonDialog, NewWorkspaceDialog } from "../people-dialogs"
import type { usePeople } from "../use-people"

type Actions = ReturnType<typeof usePeople>["actions"]

function actions(overrides: Partial<Actions> = {}): Actions {
  return {
    createPerson: vi.fn().mockResolvedValue(null),
    createWorkspace: vi.fn().mockResolvedValue(null),
    setRole: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  } as unknown as Actions
}

describe("AddPersonDialog", () => {
  it("is the shared create surface and submits on Enter in the email field", async () => {
    const a = actions()
    render(<AddPersonDialog open onOpenChange={() => {}} workspaces={[]} actions={a} busy={null} onCreated={() => {}} />)
    const dialog = screen.getByRole("dialog")
    expect(dialog).toHaveAccessibleName("Add person")
    expect(within(dialog).getByRole("button", { name: "Create and get link" })).toBeDisabled()
    const email = within(dialog).getByLabelText("Email")
    fireEvent.change(email, { target: { value: "lucie@acme.example" } })
    fireEvent.submit(email.closest("form")!)
    await waitFor(() => expect(a.createPerson).toHaveBeenCalledWith("lucie@acme.example", "", []))
  })

  it("submits from the footer primary and with Ctrl+Enter, and Cancel closes without creating", async () => {
    const a = actions()
    const onOpenChange = vi.fn()
    render(<AddPersonDialog open onOpenChange={onOpenChange} workspaces={[]} actions={a} busy={null} onCreated={() => {}} />)
    const dialog = screen.getByRole("dialog")
    fireEvent.change(within(dialog).getByLabelText("Email"), { target: { value: "a@b.example" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "Create and get link" }))
    await waitFor(() => expect(a.createPerson).toHaveBeenCalledTimes(1))
    fireEvent.keyDown(dialog, { key: "Enter", ctrlKey: true })
    await waitFor(() => expect(a.createPerson).toHaveBeenCalledTimes(2))
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
    expect(a.createPerson).toHaveBeenCalledTimes(2)
  })
})

describe("NewWorkspaceDialog", () => {
  it("keeps Create disabled until name, address and owner are set", () => {
    const people = [{ id: "u1", email: "o@x.example", full_name: "Olga Owner", memberships: [] }] as never
    render(<NewWorkspaceDialog open onOpenChange={() => {}} people={people} actions={actions()} busy={null} onCreated={() => {}} />)
    const dialog = screen.getByRole("dialog")
    expect(dialog).toHaveAccessibleName("New workspace")
    const create = within(dialog).getByRole("button", { name: "Create workspace" })
    expect(create).toBeDisabled()
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Marketing" } })
    fireEvent.change(within(dialog).getByLabelText("Owner"), { target: { value: "u1" } })
    expect(create).toBeEnabled()
  })
})
