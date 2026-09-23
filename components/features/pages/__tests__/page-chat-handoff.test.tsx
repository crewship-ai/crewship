import { describe, expect, it, vi } from "vitest"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"
import { PageChatHandoff, pageChatHandoffHref } from "../page-chat-handoff"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

describe("Page chat handoff", () => {
  it("puts only a Page slug in the URL and starts an unsent session", () => {
    expect(pageChatHandoffHref("lead one", "fleet/201", "ws-1"))
      .toBe("/chat/lead%20one?page=fleet%2F201&new=1&workspace_id=ws-1")
  })

  it("prefers the owning crew lead and lets the reader choose another agent", async () => {
    apiFetch.mockResolvedValueOnce({ ok: true, json: async () => [
      { id: "other", slug: "other", name: "Other", agent_role: "LEAD", crew: { slug: "quality" } },
      { id: "owner-lead", slug: "lead", name: "Lead", agent_role: "LEAD", crew: { slug: "ops" } },
    ] })
    render(<PageChatHandoff open onOpenChange={() => {}} workspaceId="ws-1" pageSlug="fleet" ownerCrewSlug="ops" />)
    await waitFor(() => expect(screen.getByRole("combobox")).toHaveValue("owner-lead"))
    expect(screen.getByRole("link", { name: "Open draft" })).toHaveAttribute("href", "/chat/lead?page=fleet&new=1&workspace_id=ws-1")
    fireEvent.change(screen.getByRole("combobox"), { target: { value: "other" } })
    expect(screen.getByRole("link", { name: "Open draft" })).toHaveAttribute("href", "/chat/other?page=fleet&new=1&workspace_id=ws-1")
  })
})
