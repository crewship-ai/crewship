import { describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen } from "@testing-library/react"

import { VaultHealth, buildVaultHealth } from "../vault-health"

const NOW = Date.parse("2026-10-17T12:00:00Z")
const DAY = 24 * 3600 * 1000
const base = { provider: "GITHUB", scope: "WORKSPACE", status: "ACTIVE", last_used_at: new Date(NOW - DAY).toISOString() }

describe("vault health", () => {
  it("tiles count rejected/expiring/unused and open the view", () => {
    const counts = buildVaultHealth(
      [
        { ...base, id: "1", name: "a", status: "REVOKED" },
        { ...base, id: "2", name: "b", token_expires_at: new Date(NOW - DAY).toISOString() },
        { ...base, id: "3", name: "c", token_expires_at: new Date(NOW + 10 * DAY).toISOString() },
        { ...base, id: "4", name: "d", token_expires_at: new Date(NOW + 20 * DAY).toISOString() },
        { ...base, id: "5", name: "e", last_used_at: null },
        { ...base, id: "6", name: "f", last_used_at: new Date(NOW - 31 * DAY).toISOString() },
      ],
      NOW,
    )
    expect(counts).toEqual({ rejected: 2, expiring: 1, unused: 2 })

    const onOpen = vi.fn()
    render(<VaultHealth counts={counts} onOpen={onOpen} />)
    fireEvent.click(screen.getByRole("button", { name: /^2 rejected/i }))
    fireEvent.click(screen.getByRole("button", { name: /^1 expire within 14 days/i }))
    fireEvent.click(screen.getByRole("button", { name: /^2 not used in 30 days/i }))
    expect(onOpen.mock.calls.map((c) => c[0])).toEqual(["rejected", "expiring", "unused"])
    // Old values and Shared with everyone need server data that does not exist yet.
    expect(screen.getAllByRole("button")).toHaveLength(3)
  })
})
