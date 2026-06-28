// Tests for ConnectedAccountsTab — accounts grouped per Composio user, now
// rendered as a vertical stacked list (one full-width row per account) instead
// of horizontally-wrapping pills. Each row keeps its Refresh / Revoke / Remove
// actions wired to the accounts API.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"
import { ConnectedAccountsTab } from "../connected-accounts-tab"
import type { Inventory } from "../types"

function inventory(): Inventory {
  return {
    enabled: true,
    auth_configs: [],
    users: [
      {
        user_id: "pg-test-8acca167",
        connected_accounts: [
          { id: "acc_yt_active", user_id: "pg-test-8acca167", status: "ACTIVE", toolkit: { slug: "youtube" } },
          { id: "acc_yt_expired", user_id: "pg-test-8acca167", status: "EXPIRED", toolkit: { slug: "youtube" } },
          { id: "acc_gmail", user_id: "pg-test-8acca167", status: "ACTIVE", toolkit: { slug: "gmail" } },
        ],
      },
    ],
  }
}

describe("ConnectedAccountsTab", () => {
  beforeEach(() => {
    global.fetch = vi.fn(async () => new Response(JSON.stringify({}), { status: 200 })) as unknown as typeof fetch
  })

  it("renders one row per connected account, including duplicates", () => {
    render(
      <ConnectedAccountsTab
        workspaceId="ws1"
        data={inventory()}
        onConnectForUser={vi.fn()}
        onChanged={vi.fn()}
      />,
    )
    // One stacked row per account (3), not a wrapping pill cluster.
    const rows = screen.getAllByTestId(/^account-row-/)
    expect(rows).toHaveLength(3)
    // Both Youtube accounts (the duplicate) are present.
    expect(screen.getAllByText("Youtube")).toHaveLength(2)
    expect(screen.getByText("Gmail")).toBeDefined()
  })

  it("shows each account's status, including a non-active one", () => {
    render(
      <ConnectedAccountsTab
        workspaceId="ws1"
        data={inventory()}
        onConnectForUser={vi.fn()}
        onChanged={vi.fn()}
      />,
    )
    expect(screen.getAllByText("ACTIVE")).toHaveLength(2)
    expect(screen.getByText("EXPIRED")).toBeDefined()
  })

  it("each row exposes Refresh / Revoke / Remove actions", () => {
    render(
      <ConnectedAccountsTab
        workspaceId="ws1"
        data={inventory()}
        onConnectForUser={vi.fn()}
        onChanged={vi.fn()}
      />,
    )
    expect(screen.getAllByRole("button", { name: "Refresh" })).toHaveLength(3)
    expect(screen.getAllByRole("button", { name: "Revoke" })).toHaveLength(3)
    expect(screen.getAllByRole("button", { name: "Remove" })).toHaveLength(3)
  })

  it("Remove fires a DELETE to the account endpoint and refreshes", async () => {
    const onChanged = vi.fn()
    render(
      <ConnectedAccountsTab
        workspaceId="ws1"
        data={inventory()}
        onConnectForUser={vi.fn()}
        onChanged={onChanged}
      />,
    )
    const expiredRow = screen.getByTestId("account-row-acc_yt_expired")
    fireEvent.click(within(expiredRow).getByRole("button", { name: "Remove" }))
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    const [url, opts] = (global.fetch as ReturnType<typeof vi.fn>).mock.calls[0]
    expect(String(url)).toContain("/accounts/acc_yt_expired")
    expect(String(url)).toContain("workspace_id=ws1")
    expect(opts).toMatchObject({ method: "DELETE" })
  })

  it("Connect account fires the per-user callback", () => {
    const onConnectForUser = vi.fn()
    render(
      <ConnectedAccountsTab
        workspaceId="ws1"
        data={inventory()}
        onConnectForUser={onConnectForUser}
        onChanged={vi.fn()}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: /Connect account/i }))
    expect(onConnectForUser).toHaveBeenCalledWith("pg-test-8acca167")
  })
})
