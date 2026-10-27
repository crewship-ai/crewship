import { chatArtifactProjects, chatCraftLease } from "@/lib/feature-flags"

/** Build-time gate for artifact project cards; separate from the artifact-context flag, whose routes are not served. */
export function useArtifactProjectsEnabled(): boolean {
  return chatArtifactProjects()
}

/** Build-time gate for the proposed Craft lease surface. */
export function useCraftLeaseEnabled(): boolean {
  return chatCraftLease()
}
