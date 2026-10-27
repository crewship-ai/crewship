"use client"
import { useMutation, useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query"
import { useSessionSafe } from "@/hooks/use-auth"
import { useCraftLeaseEnabled } from "../projects/use-artifact-projects-enabled"
import type { ArtifactRequestError } from "../projects/client"
import type { CraftLeaseStatus, CraftProposal } from "../projects/types"
import { acquireCraftLease, decideCraftProposal, fetchCraftAvailability, fetchCraftLease, fetchCraftProposals, requestCraftHandoff } from "./lease-client"

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

export function useCraftLease(workspaceId: string, slug: string | null): UseQueryResult<CraftLeaseStatus, ArtifactRequestError> {
  const available = useCraftAvailable(workspaceId, slug)
  const userId = useUserKey()
  return useQuery<CraftLeaseStatus, ArtifactRequestError>({
    queryKey: craftKeys.lease(workspaceId, userId, slug ?? ""),
    enabled: available,
    queryFn: ({ signal }) => fetchCraftLease(workspaceId, slug!, signal),
    retry: false, gcTime: 30_000, refetchInterval: 5_000, refetchIntervalInBackground: false,
  })
}

export function useCraftLeaseActions(workspaceId: string, slug: string) {
  const client = useQueryClient()
  const userId = useUserKey()
  const onSettled = () => client.invalidateQueries({ queryKey: craftKeys.lease(workspaceId, userId, slug) })
  // A lost race (409) is settled too: the fresh lease shows who holds it now.
  const requestHandoff = useMutation<unknown, ArtifactRequestError, void>({ mutationFn: () => requestCraftHandoff(workspaceId, slug), onSettled })
  const acquire = useMutation<CraftLeaseStatus, ArtifactRequestError, void>({ mutationFn: () => acquireCraftLease(workspaceId, slug), onSettled })
  return { requestHandoff, acquire }
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
