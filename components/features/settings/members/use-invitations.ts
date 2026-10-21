"use client"

import { useQuery } from "@tanstack/react-query"
import { z } from "zod"

import { apiFetch } from "@/lib/api-fetch"

const invitationSchema = z.object({
  id: z.string(),
  email: z.string(),
  role: z.string(),
  expires_at: z.string(),
  created_at: z.string(),
  inviter: z.object({ id: z.string(), email: z.string(), full_name: z.string().nullable().optional() }).nullable().optional(),
})
export type Invitation = z.infer<typeof invitationSchema>

/**
 * Pending invitations (GET /workspaces/{id}/invitations: not accepted yet,
 * expired ones included). Each can be reissued (a new link, valid 7 days)
 * or revoked — see reissueInvitation / revokeInvitation below.
 */
export function useInvitations(workspaceId: string | null, enabled: boolean) {
  return useQuery({
    queryKey: ["workspace-invitations", workspaceId],
    enabled: Boolean(workspaceId) && enabled,
    staleTime: 30_000,
    queryFn: async ({ signal }): Promise<Invitation[]> => {
      const ws = encodeURIComponent(workspaceId ?? "")
      const res = await apiFetch(`/api/v1/workspaces/${ws}/invitations?workspace_id=${ws}`, { signal })
      if (!res.ok) throw new Error("Invitations unavailable")
      const body = await res.json()
      const parsed = z.array(invitationSchema).safeParse(body)
      return parsed.success ? parsed.data : []
    },
  })
}

/** The link a person joins with (the same shape the invite dialog hands out). */
export function invitationLink(token: string): string {
  return `${window.location.origin}/join#invitation_token=${encodeURIComponent(token)}`
}

async function refusal(res: Response, fallback: string): Promise<Error> {
  const body = await res.json().catch(() => null) as { error?: unknown; detail?: unknown } | null
  const msg = body?.detail ?? body?.error
  return new Error(typeof msg === "string" ? msg : `${fallback} (${res.status})`)
}

/**
 * A new link for a pending invitation (POST …/reissue). The old link stops
 * working; the new one runs 7 days and is shown once — the server keeps only
 * its hash. An ADMIN invitation is the owner's to manage (403 otherwise).
 */
export async function reissueInvitation(workspaceId: string, invitationId: string): Promise<{ link: string; expiresAt: string }> {
  const ws = encodeURIComponent(workspaceId)
  const res = await apiFetch(`/api/v1/workspaces/${ws}/invitations/${encodeURIComponent(invitationId)}/reissue?workspace_id=${ws}`, { method: "POST" })
  if (!res.ok) throw await refusal(res, "Couldn’t reissue the invitation")
  const body = await res.json() as { token?: unknown; expires_at?: unknown }
  if (typeof body?.token !== "string") throw new Error("The server returned no link")
  return { link: invitationLink(body.token), expiresAt: typeof body.expires_at === "string" ? body.expires_at : "" }
}

/** Withdraw a pending invitation (DELETE): its link stops working at once. */
export async function revokeInvitation(workspaceId: string, invitationId: string): Promise<void> {
  const ws = encodeURIComponent(workspaceId)
  const res = await apiFetch(`/api/v1/workspaces/${ws}/invitations/${encodeURIComponent(invitationId)}?workspace_id=${ws}`, { method: "DELETE" })
  if (!res.ok) throw await refusal(res, "Couldn’t revoke the invitation")
}

/** Whole days until `iso`, negative once past. */
export function daysUntil(iso: string, now = Date.now()): number {
  return Math.ceil((Date.parse(iso) - now) / 86_400_000)
}

/** "Expires in 3 days", "Expires today", "Expired 2 days ago". */
export function expiryText(iso: string, now = Date.now()): string {
  const d = daysUntil(iso, now)
  if (d > 1) return `Expires in ${d} days`
  if (d === 1) return "Expires tomorrow"
  if (d === 0) return "Expires today"
  return d === -1 ? "Expired yesterday" : `Expired ${-d} days ago`
}

/**
 * The workspace's reveal switch (GET /credentials/reveal-policy, MANAGER+):
 * true, false, or null while unknown. Members shows it beside the Reveal
 * grants — a grant does nothing while the switch is off.
 */
export function useRevealPolicy(workspaceId: string | null, enabled: boolean) {
  return useQuery({
    queryKey: ["reveal-policy", workspaceId],
    enabled: Boolean(workspaceId) && enabled,
    staleTime: 60_000,
    queryFn: async ({ signal }): Promise<boolean | null> => {
      const res = await apiFetch(`/api/v1/credentials/reveal-policy?workspace_id=${encodeURIComponent(workspaceId ?? "")}`, { signal })
      if (!res.ok) return null
      const body = (await res.json().catch(() => null)) as { enabled?: boolean } | null
      return typeof body?.enabled === "boolean" ? body.enabled : null
    },
  })
}
