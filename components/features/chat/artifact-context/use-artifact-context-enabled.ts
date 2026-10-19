import { chatArtifactContext } from "@/lib/feature-flags"

/** Single gate for every artifact-context surface; each mounted component calls it and renders nothing when off. */
export function useArtifactContextEnabled(): boolean {
  return chatArtifactContext()
}
