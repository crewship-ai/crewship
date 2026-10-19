"use client"

import { useMemo, useState } from "react"
import { Building2 } from "lucide-react"
import { useTheme } from "next-themes"

import { toast } from "@/lib/toast"
import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import { useAppearance } from "@/hooks/use-appearance"
import { refreshWorkspaceSettings, useWorkspace } from "@/hooks/use-workspace"
import { apiFetch } from "@/lib/api-fetch"
import { isAdminTier } from "@/lib/permissions/tiers"
import { ACCENTS, WORKSPACE_ACCENT } from "@/lib/theme/accents"
import type { ExplicitField } from "@/lib/theme/appearance-explicit"
import { parseWorkspaceAppearanceDefault, toWorkspaceDefaultBody, type WorkspaceAppearanceDefault } from "@/lib/theme/appearance-inherit"
import { STYLES } from "@/lib/theme/appearance-style"
import { SettingsCard, SettingsRow } from "../shared"

// Settings › Account › Appearance › Workspace default (WSD-01/03).
//
// The workspace's default look is what a member sees until they choose their
// own. An owner or admin sets it from their current look; everyone can see it
// and go back to it with "Use workspace default".

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
  if (d.accent) parts.push(d.accent === WORKSPACE_ACCENT ? "Team colour" : (ACCENTS.find((a) => a.id === d.accent)?.label ?? d.accent))
  if (d.theme) parts.push(d.theme === "system" ? "System" : d.theme === "dark" ? "Dark" : "Light")
  return parts.join(" · ")
}

/**
 * Whether a value is the user's own or comes from the workspace, for a card
 * header. Silent while the workspace has no default: there is nothing to tell
 * apart then.
 */
export function InheritChip({ field }: { field: ExplicitField }) {
  const { explicit } = useAppearance()
  const d = useWorkspaceAppearanceDefault()
  if (Object.keys(d).length === 0) return null
  const own = explicit.has(field)
  return (
    <StatusPill
      data-testid={`inherit-${field}`}
      tone={own ? "blue" : "muted"}
      label={own ? "Set by you" : "Workspace default"}
    />
  )
}

export function WorkspaceDefaultCard() {
  const { workspace, workspaceId, role } = useWorkspace()
  const appearance = useAppearance()
  const { resolvedTheme } = useTheme()
  const d = useWorkspaceAppearanceDefault()
  const [saving, setSaving] = useState(false)
  const canSet = isAdminTier(role)
  const hasDefault = Object.keys(d).length > 0
  const ownCount = (["theme", "accent", "style"] as const).filter((f) => appearance.explicit.has(f)).length
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

  return (
    <SettingsCard
      icon={Building2}
      title="Workspace default"
      description={`The look ${workspace.name} members get until they choose their own.`}
    >
      <SettingsRow
        label="Default look"
        description={hasDefault ? describeDefault(d) : "None set. Everyone starts from the built-in look."}
      >
        {canSet && (
          <>
            <Button size="sm" variant="outline" disabled={saving} onClick={() => void save(current, "Your current look is now the workspace default")} className="coarse:min-h-[44px]">
              Use my look as default
            </Button>
            <Button size="sm" variant="ghost" disabled={saving || !hasDefault} onClick={() => void save({}, "Workspace default cleared")} className="coarse:min-h-[44px]">
              Reset
            </Button>
          </>
        )}
      </SettingsRow>
      <SettingsRow
        label="Your look"
        description={ownCount === 0 ? "You use the workspace default." : `You set ${ownCount === 1 ? "1 value" : `${ownCount} values`} yourself: theme, accent and style stay as you chose them.`}
      >
        <Button size="sm" variant="outline" disabled={ownCount === 0} onClick={appearance.resetToWorkspaceDefault} className="coarse:min-h-[44px]">
          Use workspace default
        </Button>
      </SettingsRow>
    </SettingsCard>
  )
}
