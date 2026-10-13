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
 * expired ones included). The server has no resend or revoke, so the page
 * only shows them and when each runs out.
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
