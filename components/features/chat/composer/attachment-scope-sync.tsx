"use client"

import { useAttachmentScopeSync } from "@/hooks/use-attachment-scope"
import { useCurrentWorkspaceId } from "@/hooks/use-workspace"

/**
 * Mounted once inside the auth provider (components/providers.tsx), for every
 * page, so a sign-out or a change of person or workspace cancels the old
 * scope's uploads and drops its chips even when no composer is on screen.
 * Subscribes only; it never triggers the workspace load.
 */
export function AttachmentScopeSync() {
  useAttachmentScopeSync(useCurrentWorkspaceId())
  return null
}
