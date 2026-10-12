"use client"

import { useState } from "react"
import { Eye, Palette } from "lucide-react"

import { SettingsCard, SettingsRow, controlHeight } from "@/components/features/settings/shared"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { SaveFooter } from "@/components/ui/save-footer"
import { useDirtyForm } from "@/hooks/use-dirty-form"
import { useWorkspacePagesTheme, refreshWorkspaceSettings } from "@/hooks/use-workspace"
import { apiFetch } from "@/lib/api-fetch"
import { isAdminTier } from "@/lib/permissions/tiers"
import { normalizePageTheme, DEFAULT_PAGE_THEME, colorContrast, pageThemeVars, type PageTheme } from "@/lib/pages/theme"
import { PagesThemePreviewPanel } from "./pages-theme-preview"
import { cn } from "@/lib/utils"

// Settings › General › Pages appearance: the workspace palette agents build
// Page applications with. Everything drawn from the palette (preset tiles,
// swatches, the mini Page) uses literal inline colours, never app tokens —
// it shows what a Page will look like, not the Crewship interface.

const HEX = /^#[0-9a-f]{6}$/i

const LABELS: Record<keyof PageTheme, string> = {
  accent: "Brand accent", background: "Background", surface: "Cards",
  text: "Text", muted: "Secondary text", border: "Borders",
}

const GROUPS: { title: string; keys: (keyof PageTheme)[] }[] = [
  { title: "Brand", keys: ["accent"] },
  { title: "Surfaces", keys: ["background", "surface", "border"] },
  { title: "Text", keys: ["text", "muted"] },
]

const NOTES: Partial<Record<keyof PageTheme, string>> = {
  accent: "Also offered to everyone as the Workspace accent in Appearance",
}

/** Starting palettes. Every one keeps text and secondary text at WCAG AA (4.5:1) on both background and cards. */
const PAGE_THEME_PRESETS: { id: string; label: string; theme: PageTheme }[] = [
  { id: "mint", label: "Mint", theme: { ...DEFAULT_PAGE_THEME } },
  { id: "midnight", label: "Midnight blue", theme: { accent: "#6ea8ff", background: "#0b1220", surface: "#141e33", text: "#e6edf7", muted: "#9fb0c8", border: "#27344f" } },
  { id: "paper", label: "Paper", theme: { accent: "#2563eb", background: "#fafaf7", surface: "#ffffff", text: "#1c2430", muted: "#566072", border: "#e2e4e8" } },
  { id: "graphite", label: "Graphite", theme: { accent: "#5eead4", background: "#141516", surface: "#1d1e20", text: "#ececed", muted: "#a1a1a8", border: "#323438" } },
]

const sameTheme = (a: PageTheme, b: PageTheme) =>
  (Object.keys(LABELS) as (keyof PageTheme)[]).every((k) => a[k].toLowerCase() === b[k].toLowerCase())

/** "AA 12.3:1" at or above 4.5:1, "Low 3.1:1" below it. */
function contrastBadge(fg: string, bg: string) {
  const ratio = colorContrast(fg, bg)
  return { ok: ratio >= 4.5, text: `${ratio >= 4.5 ? "AA" : "Low"} ${ratio.toFixed(1)}:1` }
}

function Swatch({ color }: { color: string }) {
  return <span aria-hidden className="block h-8 w-8 rounded-full shadow-[inset_0_0_0_1px_rgb(255_255_255_/_0.25),0_0_0_1px_rgb(0_0_0_/_0.08)]" style={{ background: color }} />
}

function ContrastBadge({ fg, bg, against, surfaceName }: { fg: string; bg: string; against: string; surfaceName: string }) {
  const { ok, text } = contrastBadge(fg, bg)
  return (
    <span
      data-testid="contrast-badge"
      title={`Contrast on ${surfaceName}`}
      aria-label={`Contrast on ${surfaceName}: ${text}`}
      className={cn(
        "inline-flex items-center gap-1 rounded-full border px-1.5 py-0.5 text-label tabular-nums leading-none",
        ok ? "border-success/30 bg-success/10 text-success" : "border-warn/30 bg-warn/10 text-warn",
      )}
    >
      <span aria-hidden className="h-2 w-2 rounded-full shadow-[inset_0_0_0_1px_rgb(127_127_127_/_0.5)]" style={{ background: against }} />
      {text}
    </span>
  )
}

/** Mini preview: the palette's ground, a card and the accent bar. */
function PresetPreview({ theme }: { theme: PageTheme }) {
  return (
    <div className="flex h-full flex-col gap-1 p-1.5" style={{ background: theme.background }}>
      <div className="h-1 w-1/2 rounded-full" style={{ background: theme.accent }} />
      <div className="flex flex-1 flex-col justify-end gap-0.5 rounded p-1" style={{ background: theme.surface, boxShadow: `inset 0 0 0 1px ${theme.border}` }}>
        <div className="h-1 w-2/3 rounded-full" style={{ background: theme.text }} />
        <div className="h-1 w-1/2 rounded-full" style={{ background: theme.muted }} />
      </div>
    </div>
  )
}

function PresetTiles({ draft, onPick }: { draft: PageTheme; onPick: (theme: PageTheme) => void }) {
  return (
    <div role="radiogroup" aria-label="Start from" className="grid grid-cols-2 gap-3 sm:grid-cols-4">
      {PAGE_THEME_PRESETS.map((p) => {
        const checked = sameTheme(draft, p.theme)
        return (
          <button
            key={p.id}
            type="button"
            role="radio"
            aria-checked={checked}
            aria-label={p.label}
            onClick={() => onPick(p.theme)}
            className={cn(
              "flex flex-col gap-2 rounded-xl border p-1.5 text-left outline-none transition-colors",
              "hover:border-line-strong focus-visible:ring-2 focus-visible:ring-ring/50",
              checked ? "border-primary ring-1 ring-primary" : "border-border",
            )}
          >
            <div aria-hidden className="h-12 overflow-hidden rounded-lg border border-border/60"><PresetPreview theme={p.theme} /></div>
            <span className={cn("px-1 pb-0.5 text-control", checked ? "text-foreground" : "text-muted-foreground")}>{p.label}</span>
          </button>
        )
      })}
    </div>
  )
}

/** A compact Page drawn in the palette: top bar, a card, an accent button and link. */
function MiniPage({ theme }: { theme: PageTheme }) {
  const onAccent = pageThemeVars(theme)["--crewship-page-on-accent"]
  return (
    <div
      data-testid="pages-mini-preview"
      aria-label="Sample Page in this palette"
      role="img"
      className="flex h-40 flex-col overflow-hidden rounded-xl"
      style={{ background: theme.background, color: theme.text, boxShadow: `inset 0 0 0 1px ${theme.border}` }}
    >
      <div className="flex items-center gap-2 px-3 py-2" style={{ borderBottom: `1px solid ${theme.border}` }}>
        <span className="h-2.5 w-2.5 rounded-sm" style={{ background: theme.accent }} />
        <span className="text-label font-semibold">Team dashboard</span>
        <span className="ml-auto text-label" style={{ color: theme.muted }}>Overview</span>
      </div>
      <div className="flex flex-1 flex-col p-3">
        <div className="flex flex-1 flex-col rounded-lg p-3" style={{ background: theme.surface, border: `1px solid ${theme.border}` }}>
          <p className="text-control font-semibold leading-tight">Your team’s next Page</p>
          <p className="mt-1 text-label leading-snug" style={{ color: theme.muted }}>A shared palette. A custom application.</p>
          <div className="mt-auto flex items-center gap-3 pt-2">
            <span className="rounded-md px-2.5 py-1 text-label font-medium" style={{ background: theme.accent, color: onAccent }}>Get started</span>
            <span className="text-label underline underline-offset-2" style={{ color: theme.accent }}>Learn more</span>
          </div>
        </div>
      </div>
    </div>
  )
}

export function PagesAppearanceCard({ workspaceId, role }: { workspaceId: string; role: string | null }) {
  const sharedTheme = useWorkspacePagesTheme(workspaceId)
  const theme = normalizePageTheme(sharedTheme)
  const form = useDirtyForm(theme)
  const editable = isAdminTier(role)
  const valid = Object.values(form.draft).every(v => HEX.test(v))
  // What the swatches, badges and previews show: the draft, with any
  // half-typed hex falling back to the saved colour.
  const shown = editable ? normalizePageTheme({ ...theme, ...Object.fromEntries(Object.entries(form.draft).filter(([, v]) => HEX.test(v))) }) : theme
  const contrast = Math.min(colorContrast(shown.text, shown.background), colorContrast(shown.text, shown.surface))
  const [previewOpen, setPreviewOpen] = useState(false)
  const applyPreset = (preset: PageTheme) => { for (const key of Object.keys(LABELS) as (keyof PageTheme)[]) form.set(key, preset[key]) }

  return <>
  {previewOpen && <PagesThemePreviewPanel
    theme={normalizePageTheme(editable ? form.draft : theme)}
    dirty={form.isDirty}
    editable={editable}
    onChange={(key, value) => form.set(key, value)}
    onClose={() => setPreviewOpen(false)}
  />}
  <SettingsCard icon={Palette} title="Pages appearance"
    actions={<Button variant="outline" size="sm" className="h-7 gap-1.5 px-2.5 text-xs" aria-pressed={previewOpen} onClick={() => setPreviewOpen((v) => !v)}><Eye className="h-3.5 w-3.5" />Preview</Button>}
    description="Company colors for Page applications — the Pages your agents build as apps. Agents receive this palette when they build one. Panel-only Pages and public share links keep the standard look.">
    {editable && <div className="border-b border-border p-4">
      <div className="mb-2.5 text-control text-foreground">Start from</div>
      <PresetTiles draft={form.draft} onPick={applyPreset} />
    </div>}
    <div className="grid md:grid-cols-[minmax(0,1fr)_18rem]">
      <div className="min-w-0 md:border-r md:border-border">
        {GROUPS.map((group) => <div key={group.title} role="group" aria-label={`${group.title} colors`} className="border-b border-border last:border-b-0">
          <div className="px-4 pt-3 pb-0.5 text-label font-medium uppercase tracking-wide text-muted-foreground">{group.title}</div>
          {group.keys.map((key) => {
            const label = LABELS[key]
            const isText = key === "text" || key === "muted"
            return <SettingsRow key={key} border={false} label={label} description={NOTES[key] ?? (isText ? <span className="mt-1 flex flex-wrap gap-1.5">
              <ContrastBadge fg={shown[key]} bg={shown.background} against={shown.background} surfaceName="background" />
              <ContrastBadge fg={shown[key]} bg={shown.surface} against={shown.surface} surfaceName="cards" />
            </span> : undefined)}>
              {editable ? <>
                <span className="relative rounded-full focus-within:ring-2 focus-within:ring-ring/50 focus-within:ring-offset-2 focus-within:ring-offset-card">
                  <Swatch color={shown[key]} />
                  <input aria-label={`${label} picker`} type="color" className="absolute inset-0 h-full w-full cursor-pointer rounded-full opacity-0" value={shown[key]} onChange={e => form.set(key, e.target.value)} />
                </span>
                <Input aria-label={label} className={cn(controlHeight, "w-28 font-mono")} value={form.draft[key]} maxLength={7} spellCheck={false} onChange={e => form.set(key, e.target.value)} />
              </> : <>
                <Swatch color={theme[key]} />
                <span className="w-20 font-mono text-control">{theme[key]}</span>
              </>}
            </SettingsRow>
          })}
        </div>)}
      </div>
      <div className="border-t border-border p-4 md:border-t-0">
        <div className="mb-2.5 text-control text-foreground">Live sample</div>
        <MiniPage theme={shown} />
        {contrast < 4.5 && <p role="status" className="mt-3 text-label text-warn">Text contrast is below 4.5:1. Adjust the text or background colors for readability.</p>}
      </div>
    </div>
    {editable && <SaveFooter dirty={form.isDirty} count={form.dirtyCount} status={form.status} error={form.error} canSave={valid} onCancel={form.reset} onSave={() => void form.submit(async draft => {
      const response = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ pages_theme: draft }) })
      if (!response.ok) { const body = await response.json().catch(() => null); throw new Error(body?.error ?? "Unable to save Pages colors") }
      await refreshWorkspaceSettings()
    })} />}
  </SettingsCard>
  </>
}
