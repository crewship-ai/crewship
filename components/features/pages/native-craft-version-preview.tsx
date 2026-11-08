"use client"

import * as React from "react"
import type { NativeCraftVersionPreviewProps } from "./native-craft-version-history"

// Loaded on demand: the preview frame module imports the editor host (for the
// frame URL), which renders the history; a static import would be a cycle.
const NativeCraftPreview = React.lazy(() => import("./native-craft-preview").then(m => ({ default: m.NativeCraftPreview })))

/** A saved version in the isolated native editor frame, read-only; every access recheck re-reads it. */
export function NativeCraftVersionFramePreview({ version, read, scope }: NativeCraftVersionPreviewProps) {
  if (!scope) return <p role="alert">Preview not available.</p>
  return <React.Suspense fallback={<p role="status">Preview of version {version} · loading…</p>}>
    <NativeCraftPreview humanId={scope.humanId} workspaceId={scope.workspaceId} pageId={scope.pageId} label={`Preview of version ${version}`} read={read} />
  </React.Suspense>
}
