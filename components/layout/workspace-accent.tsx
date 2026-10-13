"use client"

import { useEffect, useMemo } from "react"

import { useWorkspace, useWorkspacePagesTheme } from "@/hooks/use-workspace"
import {
  WORKSPACE_ACCENT_CSS_KEY,
  WORKSPACE_ACCENT_CSS_PATTERN,
  WORKSPACE_ACCENT_STYLE_ID,
} from "@/lib/theme/accents"
import { workspaceAccentCss } from "@/lib/theme/derive-accent"

const HEX = /^#[0-9a-f]{6}$/i

/**
 * The current workspace's brand colour, null when its admin never set one,
 * undefined while that is not known yet.
 *
 * Read from `pages_theme.accent` (Settings › General › Pages appearance), the
 * one brand colour a workspace already stores. Absent `pages_theme` means
 * "never customised"; the Pages default palette is not a brand and is not
 * offered as one.
 */
export function useWorkspaceAccentHex(): string | null | undefined {
  const { workspace, workspaceId, loading } = useWorkspace()
  const theme = useWorkspacePagesTheme(workspaceId ?? undefined) as { accent?: unknown } | null | undefined
  // undefined = not known yet (the workspace list is still loading). Telling
  // that apart from null matters: treating "loading" as "no colour" would
  // strip the stylesheet the boot script painted and flash Blue.
  if (loading || !workspace) return undefined
  const accent = theme?.accent
  return typeof accent === "string" && HEX.test(accent) ? accent.toUpperCase() : null
}

/**
 * Keeps the derived workspace accent painted: a <style> holding the two
 * `data-accent="workspace"` rules, regenerated whenever the workspace or its
 * colour changes, and a copy in localStorage for the boot script's next
 * first paint.
 *
 * Mounted once in the shell. It paints regardless of the user's own choice —
 * the rules only match while data-accent="workspace" — so picking Workspace
 * shows the colour at once, with nothing to fetch.
 */
export function WorkspaceAccentStyle() {
  const hex = useWorkspaceAccentHex()
  const css = useMemo(() => (hex ? workspaceAccentCss(hex) : null), [hex])

  useEffect(() => {
    if (hex === undefined) return
    let el = document.getElementById(WORKSPACE_ACCENT_STYLE_ID) as HTMLStyleElement | null
    if (!css || !WORKSPACE_ACCENT_CSS_PATTERN.test(css)) {
      // No colour for this workspace: drop the rules (and the cache), so a
      // Workspace choice falls back to Blue instead of the last workspace's.
      el?.remove()
      try { localStorage.removeItem(WORKSPACE_ACCENT_CSS_KEY) } catch { /* blocked storage */ }
      return
    }
    if (!el) {
      el = document.createElement("style")
      el.id = WORKSPACE_ACCENT_STYLE_ID
      document.head.appendChild(el)
    }
    if (el.textContent !== css) el.textContent = css
    try { localStorage.setItem(WORKSPACE_ACCENT_CSS_KEY, css) } catch { /* blocked storage */ }
  }, [hex, css])

  return null
}
