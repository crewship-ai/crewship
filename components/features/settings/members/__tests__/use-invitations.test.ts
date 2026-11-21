import { describe, it, expect, vi, beforeEach } from "vitest"

import { invitationLink, reissueInvitation, revokeInvitation } from "../use-invitations"

// The server can reissue and revoke a pending invitation
// (POST …/invitations/{id}/reissue, DELETE …/invitations/{id}); Members
// offers both on the Invited view.

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

function json(status: number, body: unknown) {
  return { ok: status >= 200 && status < 300, status, json: async () => body }
}

beforeEach(() => apiFetch.mockReset())

describe("invitation lifecycle", () => {
  it("reissue returns the new link and when it runs out", async () => {
    apiFetch.mockResolvedValue(json(200, { id: "i1", email: "eva@acme.dev", role: "MEMBER", token: "wi1_abc", expires_at: "2026-10-24T00:00:00Z" }))
    const out = await reissueInvitation("ws 1", "i1")
    expect(apiFetch).toHaveBeenCalledWith("/api/v1/workspaces/ws%201/invitations/i1/reissue?workspace_id=ws%201", { method: "POST" })
    expect(out).toEqual({ link: invitationLink("wi1_abc"), expiresAt: "2026-10-24T00:00:00Z" })
    expect(out.link).toBe(`${window.location.origin}/join#invitation_token=wi1_abc`)
  })

  it("revoke removes the invitation on the server", async () => {
    apiFetch.mockResolvedValue(json(200, { ok: true }))
    await revokeInvitation("ws1", "i1")
    expect(apiFetch).toHaveBeenCalledWith("/api/v1/workspaces/ws1/invitations/i1?workspace_id=ws1", { method: "DELETE" })
  })

  it("passes the server's refusal on", async () => {
    apiFetch.mockResolvedValue(json(403, { error: "Only workspace owner can manage an ADMIN invitation" }))
    await expect(revokeInvitation("ws1", "i1")).rejects.toThrow("Only workspace owner can manage an ADMIN invitation")
    apiFetch.mockResolvedValue(json(409, { error: "User is already a member of this workspace" }))
    await expect(reissueInvitation("ws1", "i1")).rejects.toThrow("User is already a member of this workspace")
  })
})

describe("invitation status", () => {
  const day = 86_400_000
  const now = Date.parse("2026-11-19T12:00:00Z")
  const at = (days: number) => new Date(now + days * day).toISOString()

  it("keeps the server's status instead of dropping it, so a revoked invitation is not 'expired'", async () => {
    const { invitationStatus } = await import("../use-invitations")
    // A revoked invitation has its expiry set to 1970: the date alone says expired.
    expect(invitationStatus({ status: "revoked", expires_at: "1970-01-01T00:00:00Z" }, now)).toBe("revoked")
    expect(invitationStatus({ status: "pending", expires_at: at(3) }, now)).toBe("pending")
    expect(invitationStatus({ status: "expired", expires_at: at(-1) }, now)).toBe("expired")
  })

  it("falls back to the date when an older server sends no status", async () => {
    const { invitationStatus } = await import("../use-invitations")
    expect(invitationStatus({ expires_at: at(-2) }, now)).toBe("expired")
    expect(invitationStatus({ expires_at: at(5) }, now)).toBe("pending")
  })

  it("needs a look when expired or ending within two days, never when revoked", async () => {
    const { invitationNeedsLook } = await import("../use-invitations")
    expect(invitationNeedsLook({ status: "expired", expires_at: at(-1) }, now)).toBe(true)
    expect(invitationNeedsLook({ status: "pending", expires_at: at(2) }, now)).toBe(true)
    expect(invitationNeedsLook({ status: "pending", expires_at: at(5) }, now)).toBe(false)
    expect(invitationNeedsLook({ status: "revoked", expires_at: "1970-01-01T00:00:00Z" }, now)).toBe(false)
  })
})
