import { chatArtifactProjects } from "@/lib/feature-flags"

/** Build-time gate for artifact project cards; separate from the artifact-context flag, whose routes are not served. */
export function useArtifactProjectsEnabled(): boolean {
  return chatArtifactProjects()
}
