"use client"
import { useMutation, useQuery, useQueryClient, type UseMutationResult, type UseQueryResult } from "@tanstack/react-query"
import { useSessionSafe } from "@/hooks/use-auth"
import { useArtifactProjectsEnabled } from "./use-artifact-projects-enabled"
import {
  createArtifactComment, fetchArtifactAvailability, fetchArtifactProject, restoreArtifactVersion, shareArtifactVersion,
  type ArtifactRequestError,
} from "./client"
import type { ArtifactComment, ArtifactProjectSnapshot, CreateArtifactCommentBody } from "./types"

/**
 * Every key carries the workspace and the signed-in user, so a cached snapshot
 * of one identity is never served to another after a sign-in switch.
 */
const ROOT = "artifact-project"
const useUserKey = () => useSessionSafe().data?.user.id ?? ""

export const artifactProjectKeys = {
  availability: (workspaceId: string, userId: string) => [ROOT, "availability", workspaceId, userId] as const,
  snapshots: (workspaceId: string, userId: string, slug: string) => [ROOT, "snapshot", workspaceId, userId, slug] as const,
  snapshot: (workspaceId: string, userId: string, slug: string, version?: number) => [ROOT, "snapshot", workspaceId, userId, slug, version ?? "latest"] as const,
}

/** False while loading, on error, with the flag off or without a workspace: the surface appears only once the server says it works. */
export function useArtifactProjectsAvailable(workspaceId: string | null | undefined): boolean {
  const flag = useArtifactProjectsEnabled()
  const userId = useUserKey()
  const query = useQuery({
    queryKey: artifactProjectKeys.availability(workspaceId ?? "", userId),
    enabled: flag && Boolean(workspaceId),
    queryFn: ({ signal }) => fetchArtifactAvailability(workspaceId!, signal),
    staleTime: 60_000, retry: false,
  })
  return flag && Boolean(workspaceId) && query.data === true
}

export function useArtifactProjectSnapshot(workspaceId: string, slug: string | null, version?: number): UseQueryResult<ArtifactProjectSnapshot, ArtifactRequestError> {
  const flag = useArtifactProjectsEnabled()
  const userId = useUserKey()
  return useQuery<ArtifactProjectSnapshot, ArtifactRequestError>({
    queryKey: artifactProjectKeys.snapshot(workspaceId, userId, slug ?? "", version),
    enabled: flag && Boolean(workspaceId && slug),
    queryFn: ({ signal }) => fetchArtifactProject(workspaceId, slug!, version, signal),
    retry: false, gcTime: 30_000,
    // The agent publishes new versions and answers comments while the card is open; a pinned version never changes.
    refetchInterval: version === undefined ? 5_000 : false,
    refetchIntervalInBackground: false,
  })
}

type VersionBody = { base_version: number; version: number }

export function useArtifactProjectMutations(workspaceId: string, slug: string): {
  restore: UseMutationResult<{ version: number; actor?: string }, ArtifactRequestError, VersionBody>
  share: UseMutationResult<{ shared_version: number; url?: string }, ArtifactRequestError, VersionBody>
  comment: UseMutationResult<ArtifactComment, ArtifactRequestError, CreateArtifactCommentBody>
} {
  const client = useQueryClient()
  const userId = useUserKey()
  // Only a success refreshes: on a 409 the UI keeps what the user saw, shows the conflict and offers a reload.
  const onSuccess = () => client.invalidateQueries({ queryKey: artifactProjectKeys.snapshots(workspaceId, userId, slug) })
  const restore = useMutation<{ version: number; actor?: string }, ArtifactRequestError, VersionBody>({ mutationFn: body => restoreArtifactVersion(workspaceId, slug, body), onSuccess })
  const share = useMutation<{ shared_version: number; url?: string }, ArtifactRequestError, VersionBody>({ mutationFn: body => shareArtifactVersion(workspaceId, slug, body), onSuccess })
  const comment = useMutation<ArtifactComment, ArtifactRequestError, CreateArtifactCommentBody>({ mutationFn: body => createArtifactComment(workspaceId, slug, body), onSuccess })
  return { restore, share, comment }
}
