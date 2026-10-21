"use client"

import { useAuth } from "@/hooks/use-auth"
import { useAppearanceSync } from "@/hooks/use-appearance-sync"
import { useSoundPreferencesSync } from "@/hooks/use-sound-preferences-sync"
import { useWorkspace } from "@/hooks/use-workspace"

/**
 * Mounted once by the dashboard layout, and only for a trusted session: the
 * preference routes are not on the restricted allowlist. Renders nothing.
 *
 * Carries the user's personal settings to the server (GET/PUT
 * /api/v1/me/preferences): appearance (hooks/use-appearance-sync.ts) and the
 * current workspace's notification sounds (hooks/use-sound-preferences-sync.ts).
 * The workspace's default look reaches everyone who has not chosen their own
 * (WSD-03).
 */
export function AppearanceSync() {
  const { session } = useAuth()
  const { workspaceId, workspace } = useWorkspace()
  // undefined until the workspace is known: nothing is inherited before that.
  useAppearanceSync(true, workspace ? (workspace.appearance_default ?? null) : undefined)
  useSoundPreferencesSync(true, session?.user?.id, workspaceId)
  return null
}
