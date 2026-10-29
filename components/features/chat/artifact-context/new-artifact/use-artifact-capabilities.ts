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
    documents: documents.data === true || Boolean(documents.wordcraftSDKPrefix) || documents.wordcraftCommands,
    // A blank workbook cannot be typed into until something imports into it,
    // and the browser has no import yet: Sheet stays listed, not offered.
    sheets: false,
  }
}
