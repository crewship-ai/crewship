import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"

import { ConfirmHost, askConfirm, confirmHostMounted } from "../confirm-host"

// One in-page confirmation for code that cannot render its own dialog — a
// navigation guard, a hook shared with another team's editor. It answers a
// promise; without a mounted host the caller falls back to its own path.

afterEach(() => cleanup())

describe("ConfirmHost", () => {
  it("is not mounted until the shell renders it", () => {
    expect(confirmHostMounted()).toBe(false)
    const { unmount } = render(<ConfirmHost />)
    expect(confirmHostMounted()).toBe(true)
    unmount()
    expect(confirmHostMounted()).toBe(false)
  })

  it("resolves true on confirm and false on cancel", async () => {
    render(<ConfirmHost />)
    let yes!: Promise<boolean>
    act(() => { yes = askConfirm({ title: "Leave this page?", description: "Unsaved changes are lost.", confirmLabel: "Leave" }) })
    expect(await screen.findByRole("alertdialog", { name: "Leave this page?" })).toHaveTextContent("Unsaved changes are lost.")
    fireEvent.click(screen.getByRole("button", { name: "Leave" }))
    await expect(yes).resolves.toBe(true)
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())

    let no!: Promise<boolean>
    act(() => { no = askConfirm({ title: "Leave this page?", confirmLabel: "Leave", cancelLabel: "Keep editing" }) })
    fireEvent.click(await screen.findByRole("button", { name: "Keep editing" }))
    await expect(no).resolves.toBe(false)
  })
})
