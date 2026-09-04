import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import type { InboxItem } from "@/hooks/use-inbox"

import { KindActions } from "../kind-actions"

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const hire: InboxItem = {
  id: "ibx-h", workspace_id: "ws", kind: "waitpoint", source_id: "ag-new", title: "Hire ephemeral agent: DevOps / SRE (60m)",
  state: "unread", priority: "high", blocking: true, created_at: "2026-09-03T10:00:00Z", updated_at: "2026-09-03T10:00:00Z",
  payload: { kind: "hire", agent_id: "ag-new", agent_name: "DevOps / SRE", crew_id: "c-eng" },
}

describe("a staged hire", () => {
  it("has Deny when the page wired the approvals-queue twin, and refreshes after it", async () => {
    const onDenyHire = vi.fn().mockResolvedValue(undefined)
    const onRefresh = vi.fn().mockResolvedValue(undefined)
    render(<KindActions item={hire} onResolve={() => {}} onRefresh={onRefresh} disabled={false} onDenyHire={onDenyHire} crewHref="/crews?crew=engineering" />)
    fireEvent.click(screen.getByRole("button", { name: "Deny" }))
    await waitFor(() => expect(onDenyHire).toHaveBeenCalled())
    await waitFor(() => expect(onRefresh).toHaveBeenCalledWith("denied"))
    expect(screen.getByRole("link", { name: "Open crew" }).getAttribute("href")).toBe("/crews?crew=engineering")
  })

  it("names where to go, as a link, when there is no twin to deny through", () => {
    render(<KindActions item={hire} onResolve={() => {}} onRefresh={() => {}} disabled={false} crewHref="/crews?crew=engineering" />)
    expect(screen.queryByRole("button", { name: "Deny" })).not.toBeInTheDocument()
    expect(screen.getByRole("link", { name: "its crew page" }).getAttribute("href")).toBe("/crews?crew=engineering")
  })
})
