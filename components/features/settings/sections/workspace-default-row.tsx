"use client"

import { useMemo, useState } from "react"
import { useTheme } from "next-themes"

import { toast } from "@/lib/toast"
import { Button } from "@/components/ui/button"
import { useAppearance } from "@/hooks/use-appearance"
import { refreshWorkspaceSettings, useWorkspace } from "@/hooks/use-workspace"
import { apiFetch } from "@/lib/api-fetch"
import { isAdminTier } from "@/lib/permissions/tiers"
import { parseWorkspaceAppearanceDefault, toWorkspaceDefaultBody, type WorkspaceAppearanceDefault } from "@/lib/theme/appearance-inherit"
import { STYLES } from "@/lib/theme/appearance-style"
import { SettingsRow } from "../shared"
import { accentName, themeName } from "./appearance-names"

// Settings › Appearance › Devices and workspace › Workspace default (WSD-01/03).
//
// The workspace's default look is what a member sees until they choose their
// own. An owner or admin sets it from their current look ("Use my look") or
// clears it; a member only reads what it is, and not at all when none is set.
// Going back to it is the "Reset to default" button of the "Your look" line.

/** The default of the workspace in view, parsed; `{}` when it has none. */
export function useWorkspaceAppearanceDefault(): WorkspaceAppearanceDefault {
  const { workspace } = useWorkspace()
  const raw = workspace?.appearance_default
  return useMemo(() => parseWorkspaceAppearanceDefault(raw), [raw])
}

/** One word per value of the default: "Slate", "Teal", "Dark". */
export function describeDefault(d: WorkspaceAppearanceDefault): string {
  const parts: string[] = []
  if (d.style) parts.push(STYLES.find((s) => s.id === d.style)?.name ?? d.style)
  if (d.accent) parts.push(accentName(d.accent))
  if (d.theme) parts.push(themeName(d.theme))
  return parts.join(" · ")
}

export function WorkspaceDefaultRow() {
  const { workspace, workspaceId, role } = useWorkspace()
  const appearance = useAppearance()
  const { resolvedTheme } = useTheme()
  const d = useWorkspaceAppearanceDefault()
  const [saving, setSaving] = useState(false)
  const canSet = isAdminTier(role)
  const hasDefault = Object.keys(d).length > 0
  if (!workspace || !workspaceId) return null
  // A member with nothing to inherit has nothing to see here.
  if (!canSet && !hasDefault) return null

  async function save(body: object, done: string) {
    setSaving(true)
    try {
      const response = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId!)}?workspace_id=${encodeURIComponent(workspaceId!)}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ appearance_default: body }),
      })
      if (!response.ok) {
        const err = await response.json().catch(() => null)
        toast.error(err?.error ?? "Unable to save the workspace default")
        return
      }
      await refreshWorkspaceSettings()
      toast.success(done)
    } finally {
      setSaving(false)
    }
  }

  // What the person sees now, including values they inherit: theme is the
  // stored choice (System stays System), not the resolved scheme.
  const current = toWorkspaceDefaultBody({ theme: appearance.theme ?? (resolvedTheme === "light" ? "light" : "dark"), accent: appearance.accent, style: appearance.style })
  const what = hasDefault ? <span data-testid="workspace-default-look" className="font-medium text-foreground">{describeDefault(d)}</span> : null

  return (
    <SettingsRow
      label="Workspace default"
      description={
        canSet ? (
          <>What new {workspace.name} members start with. {what ? <>Now {what}.</> : "None set, so they get Harbor."}</>
        ) : (
          <>What {workspace.name} members get until they choose their own: {what}.</>
        )
      }
    >
      {canSet && (
        <>
          <Button size="sm" variant="outline" disabled={saving} onClick={() => void save(current, "Your current look is now the workspace default")} className="coarse:min-h-[44px]">
            Use my look
          </Button>
          <Button size="sm" variant="ghost" disabled={saving || !hasDefault} onClick={() => void save({}, "Workspace default cleared")} className="coarse:min-h-[44px]">
            Clear
          </Button>
        </>
      )}
    </SettingsRow>
  )
}
