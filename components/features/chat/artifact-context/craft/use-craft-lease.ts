"use client"
import { useCallback, useRef } from "react"
import { useMutation, useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query"
import { useSessionSafe } from "@/hooks/use-auth"
import { useCraftLeaseEnabled } from "../projects/use-artifact-projects-enabled"
import { ArtifactRequestError } from "../projects/client"
import type { CraftLeaseStatus, CraftProposal } from "../projects/types"
import { createCraftSession, decideCraftProposal, fetchCraftAvailability, fetchCraftLease, fetchCraftProposals, requestCraftHandoff, type CraftHandoffRequest } from "./lease-client"

const ROOT = "craft-lease"
const useUserKey = () => useSessionSafe().data?.user.id ?? ""

export const craftKeys = {
  all: (workspaceId: string, userId: string, slug: string) => [ROOT, workspaceId, userId, slug] as const,
  availability: (workspaceId: string, userId: string, slug: string) => [ROOT, workspaceId, userId, slug, "availability"] as const,
  lease: (workspaceId: string, userId: string, slug: string) => [ROOT, workspaceId, userId, slug, "lease"] as const,
  proposals: (workspaceId: string, userId: string, slug: string) => [ROOT, workspaceId, userId, slug, "proposals"] as const,
}

/** False while loading, on any failure and for the 404 of a backend without the routes, so no Craft control renders until it ships. */
export function useCraftAvailable(workspaceId: string, slug: string | null): boolean {
  const flag = useCraftLeaseEnabled()
  const userId = useUserKey()
  const enabled = flag && Boolean(workspaceId && slug)
  const query = useQuery({
    queryKey: craftKeys.availability(workspaceId, userId, slug ?? ""),
    enabled,
    queryFn: ({ signal }) => fetchCraftAvailability(workspaceId, slug!, signal),
    staleTime: 60_000, retry: false,
  })
  return enabled && query.data === true
}

/**
 * The lease as this chat preview sees it. The preview opens one admission
 * (POST sessions) and reuses it for every read and request; admissions cannot
 * be closed, so a re-admission happens only once when the server forgets one
 * (403 after a restart), and a failed read stops the polling. Chat never
 * acquires: Edit opens the editor, which takes the lease on its own admission,
 * so `mine` is never true here and my own hold is recognised by actor id.
 */
export function useCraftLeaseState(workspaceId: string, slug: string | null) {
  const available = useCraftAvailable(workspaceId, slug)
  const userId = useUserKey()
  const client = useQueryClient()
  const scope = `${workspaceId}\u0000${slug ?? ""}`
  const admission = useRef<{ scope: string; token: Promise<string> } | null>(null)

  const withAdmission = useCallback(async <T,>(call: (token: string) => Promise<T>): Promise<T> => {
    const admit = () => {
      const token = createCraftSession(workspaceId, slug!)
      const entry = { scope, token }
      admission.current = entry
      token.catch(() => { if (admission.current === entry) admission.current = null })
      return entry
    }
    const used = admission.current?.scope === scope ? admission.current : admit()
    try {
      return await call(await used.token)
    } catch (error) {
      if (!(error instanceof ArtifactRequestError) || (error.status !== 401 && error.status !== 403)) throw error
      // Only the first caller to see the stale admission replaces it; a concurrent one reuses the new one.
      const next = admission.current === used ? admit() : admission.current ?? admit()
      return call(await next.token)
    }
  }, [workspaceId, slug, scope])

  const lease = useQuery<CraftLeaseStatus, ArtifactRequestError>({
    queryKey: craftKeys.lease(workspaceId, userId, slug ?? ""),
    enabled: available,
    queryFn: ({ signal }) => withAdmission(token => fetchCraftLease(workspaceId, slug!, token, signal)),
    retry: false, gcTime: 30_000, refetchIntervalInBackground: false,
    refetchInterval: query => query.state.status === "error" ? false : 5_000,
  })
  const requestHandoff = useMutation<CraftHandoffRequest, ArtifactRequestError, void>({
    mutationFn: () => withAdmission(token => requestCraftHandoff(workspaceId, slug!, token)),
    // A lost race (409) is settled too: the fresh lease shows who holds it now.
    onSettled: () => client.invalidateQueries({ queryKey: craftKeys.lease(workspaceId, userId, slug ?? "") }),
  })
  const status = available ? lease.data : undefined
  const me = userId ? `user:${userId}` : null
  return {
    status,
    requestHandoff,
    /** Generation the request was sent for; a new holder or re-acquisition is a new lease. */
    requestedGeneration: requestHandoff.data?.generation ?? null,
    heldByMe: Boolean(me && status?.holder?.actor_id === me),
    requestedByMe: Boolean(me && status?.requests.some(request => request.actor_id === me)),
  }
}

export function useCraftProposals(workspaceId: string, slug: string | null): UseQueryResult<CraftProposal[], ArtifactRequestError> {
  const available = useCraftAvailable(workspaceId, slug)
  const userId = useUserKey()
  return useQuery<CraftProposal[], ArtifactRequestError>({
    queryKey: craftKeys.proposals(workspaceId, userId, slug ?? ""),
    enabled: available,
    queryFn: ({ signal }) => fetchCraftProposals(workspaceId, slug!, signal),
    retry: false, gcTime: 30_000, refetchInterval: 5_000, refetchIntervalInBackground: false,
  })
}

export function useCraftProposalActions(workspaceId: string, slug: string) {
  const client = useQueryClient()
  const userId = useUserKey()
  const onSettled = () => client.invalidateQueries({ queryKey: craftKeys.all(workspaceId, userId, slug) })
  const accept = useMutation<unknown, ArtifactRequestError, string>({ mutationFn: id => decideCraftProposal(workspaceId, slug, id, "accept"), onSettled })
  const reject = useMutation<unknown, ArtifactRequestError, string>({ mutationFn: id => decideCraftProposal(workspaceId, slug, id, "reject"), onSettled })
  return { accept, reject }
}
