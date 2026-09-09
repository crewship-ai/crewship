"use client"

import { SettingsCard } from "@/components/features/settings/shared"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { SaveFooter } from "@/components/ui/save-footer"
import { useDirtyForm } from "@/hooks/use-dirty-form"
import { useWorkspacePagesTheme, refreshWorkspaceSettings } from "@/hooks/use-workspace"
import { apiFetch } from "@/lib/api-fetch"
import { isAdminTier } from "@/lib/permissions/tiers"
import { normalizePageTheme, DEFAULT_PAGE_THEME, colorContrast, type PageTheme } from "@/lib/pages/theme"

export function PagesAppearanceCard({ workspaceId, role }: { workspaceId: string; role: string | null }) {
  const sharedTheme = useWorkspacePagesTheme(workspaceId)
  const theme = normalizePageTheme(sharedTheme)
  const form = useDirtyForm(theme)
  const editable = isAdminTier(role)
  const valid = Object.values(form.draft).every(v => /^#[0-9a-f]{6}$/i.test(v))
  const contrast = valid && Math.min(colorContrast(form.draft.text, form.draft.background), colorContrast(form.draft.text, form.draft.surface))
  const labels: Record<keyof PageTheme, string> = { accent: "Brand accent", background: "Background", surface: "Cards", text: "Text", muted: "Secondary text", border: "Borders" }
  return <SettingsCard title="Pages appearance" description="Shared company colors for custom Pages. Agents can use these colors while keeping their own layouts and components.">
    <div className="grid gap-4 p-4 sm:grid-cols-2">
      {(Object.keys(labels) as (keyof PageTheme)[]).map(key => <div key={key} className="flex items-center justify-between gap-3 text-sm">
        <span>{labels[key]}</span>
        {editable ? <span className="flex items-center gap-2">
          <input aria-label={`${labels[key]} picker`} type="color" className="h-8 w-9 cursor-pointer rounded border bg-transparent" value={/^#[0-9a-f]{6}$/i.test(form.draft[key]) ? form.draft[key] : theme[key]} onChange={e => form.set(key, e.target.value)} />
          <Input aria-label={labels[key]} className="w-28 font-mono" value={form.draft[key]} maxLength={7} onChange={e => form.set(key, e.target.value)} />
        </span> : <span className="font-mono">{theme[key]}</span>}
      </div>)}
    </div>
    <div className="mx-4 mb-4 rounded-xl border p-5" style={{ background: normalizePageTheme(form.draft).background, color: normalizePageTheme(form.draft).text, borderColor: normalizePageTheme(form.draft).border }}>
      <p className="font-semibold">Your team’s next Page</p>
      <p className="mt-2 text-sm" style={{ color: normalizePageTheme(form.draft).muted }}>A shared palette. A custom application.</p>
      <div className="mt-4 h-1 w-24 rounded" style={{ background: normalizePageTheme(form.draft).accent }} />
    </div>
    {contrast !== false && contrast < 4.5 && <p role="status" className="px-4 pb-3 text-sm text-warn">Text contrast is below 4.5:1. Adjust the text or background colors for readability.</p>}
    {editable && <>
      <Button variant="ghost" className="mx-4 mb-3" onClick={() => { for (const key of Object.keys(DEFAULT_PAGE_THEME) as (keyof PageTheme)[]) form.set(key, DEFAULT_PAGE_THEME[key]) }}>Use default colors</Button>
      <SaveFooter dirty={form.isDirty} status={form.status} error={form.error} canSave={valid} onCancel={form.reset} onSave={() => void form.submit(async draft => {
        const response = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ pages_theme: draft }) })
        if (!response.ok) { const body = await response.json().catch(() => null); throw new Error(body?.error ?? "Unable to save Pages colors") }
        await refreshWorkspaceSettings()
      })} />
    </>}
  </SettingsCard>
}
