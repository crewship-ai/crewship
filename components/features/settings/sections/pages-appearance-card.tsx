"use client"

import { useState } from "react"
import { Eye, Palette, Plus, RotateCcw } from "lucide-react"
import { toast } from "sonner"

import { SettingsCard, controlHeight } from "@/components/features/settings/shared"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { SaveFooter } from "@/components/ui/save-footer"
import { useDirtyForm } from "@/hooks/use-dirty-form"
import { useWorkspacePagesTheme, refreshWorkspaceSettings } from "@/hooks/use-workspace"
import { apiFetch } from "@/lib/api-fetch"
import { isAdminTier } from "@/lib/permissions/tiers"
import { normalizePageTheme, PAGE_THEME_PRESETS, colorContrast, pageThemePreset, pageThemeVars, type PageTheme } from "@/lib/pages/theme"
import { PagesThemePreviewPanel } from "./pages-theme-preview"
import { cn } from "@/lib/utils"

// Settings › General › Pages appearance: the workspace palette agents build
// Page applications with. Everything drawn from the palette (preset tiles,
// swatches, the mini Page) uses literal inline colours, never app tokens —
// it shows what a Page will look like, not the Crewship interface.

const HEX = /^#[0-9a-f]{6}$/i

const LABELS: Record<keyof PageTheme, string> = {
  accent: "Accent", background: "Background", surface: "Cards",
  border: "Borders", text: "Text", muted: "Secondary text",
}
const ORDER: (keyof PageTheme)[] = ["accent", "background", "surface", "border", "text", "muted"]

function Swatch({ color, size = "h-7 w-7" }: { color: string; size?: string }) {
  return <span aria-hidden className={cn("block shrink-0 rounded-full shadow-[inset_0_0_0_1px_rgb(255_255_255_/_0.25),0_0_0_1px_rgb(0_0_0_/_0.08)]", size)} style={{ background: color }} />
}

/** One AA badge for a text colour: the worse of its contrast on background
 *  and on cards; both exact ratios in the tooltip. */
function ContrastBadge({ fg, theme }: { fg: string; theme: PageTheme }) {
  const onBg = colorContrast(fg, theme.background)
  const onCard = colorContrast(fg, theme.surface)
  const ok = Math.min(onBg, onCard) >= 4.5
  const detail = `${onBg.toFixed(1)}:1 on background · ${onCard.toFixed(1)}:1 on cards`
  return (
    <span
      data-testid="contrast-badge"
      title={detail}
      aria-label={`Contrast ${ok ? "AA" : "low"}: ${detail}`}
      className={cn(
        "rounded-full px-1.5 py-px text-micro font-semibold leading-none",
        ok ? "bg-success/10 text-success" : "bg-warn/10 text-warn",
      )}
    >
      {ok ? "AA" : "Low"}
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

/**
 * The palettes in one row (two columns on a phone, three on a tablet), the
 * default first and Custom last: custom colours are the exception. Custom is
 * pressed whenever the draft matches no preset; picking it only reveals the
 * colour list, it does not change a colour.
 */
function ThemeTiles({ draft, editing, onPick, onCustom }: { draft: PageTheme; editing: boolean; onPick: (theme: PageTheme) => void; onCustom: () => void }) {
  const preset = pageThemePreset(draft)
  return (
    <div role="radiogroup" aria-label="Pages theme" className="grid grid-cols-2 gap-2.5 sm:grid-cols-3 lg:grid-cols-5">
      {PAGE_THEME_PRESETS.map((p, i) => {
        const checked = preset?.id === p.id
        return (
          <button
            key={p.id}
            type="button"
            role="radio"
            aria-checked={checked}
            aria-label={p.label}
            onClick={() => onPick(p.theme)}
            className={cn(
              "flex min-w-0 flex-col gap-2 rounded-xl border p-1.5 text-left outline-none transition-colors",
              "hover:border-line-strong focus-visible:ring-2 focus-visible:ring-ring/50",
              checked ? "border-primary ring-1 ring-primary" : "border-border",
            )}
          >
            <div aria-hidden className="h-12 overflow-hidden rounded-lg border border-border/60"><PresetPreview theme={p.theme} /></div>
            <span className={cn("truncate px-1 pb-0.5 text-control", checked ? "text-foreground" : "text-muted-foreground")}>
              {p.label}
              {i === 0 && <span className="ml-1 text-label text-primary-hover">· default</span>}
            </span>
          </button>
        )
      })}
      <button
        type="button"
        role="radio"
        aria-checked={!preset}
        aria-label="Custom"
        onClick={onCustom}
        className={cn(
          "flex min-w-0 flex-col gap-2 rounded-xl border border-dashed p-1.5 text-left outline-none transition-colors",
          "hover:border-line-strong focus-visible:ring-2 focus-visible:ring-ring/50",
          !preset ? "border-solid border-primary ring-1 ring-primary" : editing ? "border-line-strong" : "border-border",
        )}
      >
        <div aria-hidden className="grid h-12 place-items-center overflow-hidden rounded-lg border border-border/60 bg-muted/40">
          {preset ? <Plus className="h-4 w-4 text-muted-foreground" /> : <PresetPreview theme={draft} />}
        </div>
        <span className={cn("truncate px-1 pb-0.5 text-control", !preset ? "text-foreground" : "text-muted-foreground")}>Custom…</span>
      </button>
    </div>
  )
}

/** A Page drawn in the palette: top bar, three figures, a card with an accent
 *  button and link. Literal colours throughout — it is a Page, not the app. */
function MiniPage({ theme }: { theme: PageTheme }) {
  const onAccent = pageThemeVars(theme)["--crewship-page-on-accent"]
  const card = { background: theme.surface, border: `1px solid ${theme.border}` }
  return (
    <div
      data-testid="pages-mini-preview"
      aria-label="Sample Page in this palette"
      role="img"
      className="overflow-hidden rounded-xl"
      style={{ background: theme.background, color: theme.text, boxShadow: `inset 0 0 0 1px ${theme.border}` }}
    >
      <div className="flex items-center gap-2 px-3 py-2" style={{ borderBottom: `1px solid ${theme.border}` }}>
        <span className="h-2 w-2 rounded-full" style={{ background: theme.accent }} />
        <span className="text-label font-semibold">Team dashboard</span>
        <span className="ml-auto truncate text-label" style={{ color: theme.muted }}>Overview · Reports</span>
      </div>
      <div className="grid grid-cols-3 gap-2 p-3">
        {[["Open", "24"], ["Overdue", "3"], ["Paid", "$8.2k"]].map(([label, value]) => (
          <div key={label} className="min-w-0 rounded-lg p-2" style={card}>
            <div className="truncate text-micro" style={{ color: theme.muted }}>{label}</div>
            <div className="text-[1.05rem] font-semibold leading-tight tabular-nums">{value}</div>
          </div>
        ))}
      </div>
      <div className="mx-3 mb-3 rounded-lg p-3" style={card}>
        <p className="text-control font-semibold leading-tight">Your team’s next Page</p>
        <p className="mt-1 text-label leading-snug" style={{ color: theme.muted }}>A shared palette. A custom application.</p>
        <div className="mt-2.5 flex flex-wrap items-center gap-3">
          <span className="rounded-md px-2.5 py-1 text-label font-semibold" style={{ background: theme.accent, color: onAccent }}>Get started</span>
          <span className="text-label underline underline-offset-2" style={{ color: theme.accent }}>Learn more</span>
        </div>
      </div>
    </div>
  )
}

export function PagesAppearanceCard({ workspaceId, role }: { workspaceId: string; role: string | null }) {
  const sharedTheme = useWorkspacePagesTheme(workspaceId)
  const theme = normalizePageTheme(sharedTheme)
  // Nothing saved = following the Crewship default (and its future changes).
  const followsDefault = !sharedTheme || (typeof sharedTheme === "object" && Object.keys(sharedTheme as object).length === 0)
  const form = useDirtyForm(theme)
  const editable = isAdminTier(role)
  const valid = Object.values(form.draft).every(v => HEX.test(v))
  // What the swatches, badges and previews show: the draft, with any
  // half-typed hex falling back to the saved colour.
  const shown = editable ? normalizePageTheme({ ...theme, ...Object.fromEntries(Object.entries(form.draft).filter(([, v]) => HEX.test(v))) }) : theme
  const contrast = Math.min(colorContrast(shown.text, shown.background), colorContrast(shown.text, shown.surface))
  const [previewOpen, setPreviewOpen] = useState(false)
  const [resetting, setResetting] = useState(false)
  const applyPreset = (preset: PageTheme) => { for (const key of ORDER) form.set(key, preset[key]) }
  const preset = pageThemePreset(shown)
  const status = followsDefault && !form.isDirty ? "Crewship default" : preset ? preset.label : "Custom"

  async function resetToDefault() {
    setResetting(true)
    try {
      // `{}` rather than the default's colours: the workspace then follows
      // the Crewship palette, including any later change to it.
      const response = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ pages_theme: {} }) })
      if (!response.ok) { const body = await response.json().catch(() => null); toast.error(body?.error ?? "Unable to reset Pages colors"); return }
      form.reset()
      await refreshWorkspaceSettings()
      toast.success("Pages use the Crewship default again")
    } finally {
      setResetting(false)
    }
  }

  return <>
  {previewOpen && <PagesThemePreviewPanel
    theme={normalizePageTheme(editable ? form.draft : theme)}
    dirty={form.isDirty}
    editable={editable}
    onChange={(key, value) => form.set(key, value)}
    onClose={() => setPreviewOpen(false)}
  />}
  <SettingsCard icon={Palette} title="Pages appearance"
    actions={<span className="flex flex-wrap items-center justify-end gap-2">
      <span data-testid="pages-theme-status" className="rounded-full bg-primary/10 px-2 py-0.5 text-micro font-semibold text-primary-hover">{status}</span>
      <Button variant="outline" size="sm" className="h-7 gap-1.5 px-2.5 text-xs" aria-pressed={previewOpen} onClick={() => setPreviewOpen((v) => !v)}><Eye className="h-3.5 w-3.5" />Preview</Button>
    </span>}
    description="Colours your agents use when they build Page apps. Until you pick something else, Pages wear the Crewship palette.">
    {editable && <div className="border-b border-border p-4">
      <div className="mb-2.5 text-label font-medium uppercase tracking-wide text-muted-foreground">Theme</div>
      <ThemeTiles draft={shown} editing={false} onPick={applyPreset} onCustom={() => document.getElementById("pages-color-accent")?.focus()} />
    </div>}
    <div className="grid lg:grid-cols-[minmax(0,20rem)_minmax(0,1fr)]">
      <div role="group" aria-label="Page colors" className="min-w-0 px-4 py-3 lg:border-r lg:border-border">
        <div className="mb-1 flex items-baseline justify-between gap-2">
          <span className="text-label font-medium uppercase tracking-wide text-muted-foreground">Colours</span>
          {editable && <span className="hidden text-label text-muted-foreground-soft sm:inline">Click a swatch to edit</span>}
        </div>
        {ORDER.map((key) => {
          const label = LABELS[key]
          const isText = key === "text" || key === "muted"
          return (
            <div key={key} className="flex min-h-10 items-center gap-2.5 py-1">
              {editable ? (
                <span className="relative shrink-0 rounded-full focus-within:ring-2 focus-within:ring-ring/50 focus-within:ring-offset-2 focus-within:ring-offset-card">
                  <Swatch color={shown[key]} />
                  <input aria-label={`${label} picker`} type="color" className="absolute inset-0 h-full w-full cursor-pointer rounded-full opacity-0" value={shown[key]} onChange={e => form.set(key, e.target.value)} />
                </span>
              ) : <Swatch color={theme[key]} />}
              <span className="flex min-w-0 flex-1 items-center gap-1.5 text-control">
                <span className="truncate">{label}</span>
                {isText && <ContrastBadge fg={shown[key]} theme={shown} />}
              </span>
              {editable
                ? <Input id={`pages-color-${key}`} aria-label={label} className={cn(controlHeight, "w-24 shrink-0 font-mono text-xs")} value={form.draft[key]} maxLength={7} spellCheck={false} onChange={e => form.set(key, e.target.value)} />
                : <span className="w-24 shrink-0 text-right font-mono text-xs text-muted-foreground">{theme[key]}</span>}
            </div>
          )
        })}
        {editable && <p className="mt-1.5 text-label text-muted-foreground-soft">Editing a colour makes the theme Custom. The accent is also everyone&apos;s Team colour in Appearance.</p>}
      </div>
      <div className="min-w-0 border-t border-border p-4 lg:border-t-0">
        <div className="mb-2.5 text-label font-medium uppercase tracking-wide text-muted-foreground">Live sample</div>
        <MiniPage theme={shown} />
        {contrast < 4.5 && <p role="status" className="mt-3 text-label text-warn">Text contrast is below 4.5:1. Adjust the text or background colors for readability.</p>}
      </div>
    </div>
    {editable && !followsDefault && !form.isDirty && (
      <div className="flex justify-start border-t border-border px-4 py-2.5">
        <Button variant="ghost" size="sm" className="h-7 gap-1.5 px-2 text-xs text-muted-foreground" disabled={resetting} onClick={() => void resetToDefault()}>
          <RotateCcw className="h-3.5 w-3.5" />Reset to Crewship default
        </Button>
      </div>
    )}
    {editable && <SaveFooter dirty={form.isDirty} count={form.dirtyCount} status={form.status} error={form.error} canSave={valid} onCancel={form.reset} onSave={() => void form.submit(async draft => {
      const response = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ pages_theme: draft }) })
      if (!response.ok) { const body = await response.json().catch(() => null); throw new Error(body?.error ?? "Unable to save Pages colors") }
      await refreshWorkspaceSettings()
    })} />}
  </SettingsCard>
  </>
}
