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
