"use client"

import { useDocumentsAvailability } from "@/hooks/use-documents-availability"
import { usePageCreationAccess } from "@/hooks/use-page-creation-access"
import { artifactDashboards, artifactSlides, artifactsDesign } from "@/lib/feature-flags"
import { useArtifactProjectsAvailable } from "../projects/use-artifact-project-data"
import type { ArtifactCapabilities } from "./artifact-kinds"

/** What this workspace serves right now, for the registry's `live` predicates. Unconfirmed is false. */
export function useArtifactCapabilities(workspaceId: string): ArtifactCapabilities {
  const pages = usePageCreationAccess(workspaceId)
  const documents = useDocumentsAvailability(workspaceId)
  const projects = useArtifactProjectsAvailable(workspaceId)
  return {
    pages,
    projects,
    dashboards: artifactDashboards(),
    designs: artifactsDesign(),
    slides: artifactSlides(),
    // Word is offered for any confirmed format, the native WordCraft profile
    // (wordcraft_native) included, exactly as Sheet follows gridcraft_native;
    // unconfirmed or failed reads withdraw it.
    documents: documents.data === true || documents.wordcraftNative === true || Boolean(documents.wordcraftSDKPrefix) || documents.wordcraftCommands === true,
    docs: documents.data === true,
    // Sheet is offered only when the server advertises the native GridCraft
    // profile (gridcraft_native); unconfirmed or failed reads withdraw it.
    sheets: documents.gridcraftNative === true,
  }
}
