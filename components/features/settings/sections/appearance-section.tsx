"use client"

import { useEffect, useState, type CSSProperties } from "react"
import { Monitor, Moon, Palette, Sparkles, Sun, Type } from "lucide-react"
import { useTheme } from "next-themes"

import { cn } from "@/lib/utils"
import { Switch } from "@/components/ui/switch"
import { Button } from "@/components/ui/button"
import { useAppearance } from "@/hooks/use-appearance"
import { useUndoableAppearance } from "@/hooks/use-appearance-undo"
import { retryAppearanceSync, useAppearanceSyncStatus } from "@/hooks/use-appearance-sync"
import type { Density, MotionChoice, SidebarChoice, TextSize, ThemeChoice } from "@/lib/theme/appearance"
import { ACCENTS, STYLE_ACCENTS, WORKSPACE_ACCENT, type AccentChoice } from "@/lib/theme/accents"
import { HARBOR, KNOBS, STYLES, allowedSchemes, knobValue, renderedStyle, type Scheme } from "@/lib/theme/appearance-style"
import { useWorkspaceAccentHex } from "@/components/layout/workspace-accent"
import { SettingsCard, SettingsRow, SettingsSegmented } from "../shared"
import { accentName, themeName } from "./appearance-names"
import { StyleGallery, StylePreview } from "./style-gallery"
import { WorkspaceDefaultRow, useWorkspaceAppearanceDefault } from "./workspace-default-row"

// Settings › Appearance: "Your look" with one reset, then Colours, Text and
// layout, Motion, and Devices and workspace.
//
// Every control here applies at once — there is nothing to Save, so none of
// it joins the page's floating Save bar. The choices are the same ones the
// top-bar menu offers (both go through useAppearance), and the shell's
// AppearanceSync carries them to the user's other devices while "Sync across
// devices" is on.

const DENSITY_OPTIONS: { value: Density; label: string }[] = [
  { value: "comfortable", label: "Comfortable" },
  { value: "compact", label: "Compact" },
]

const TEXT_SIZE_OPTIONS: { value: TextSize; label: string }[] = [
  { value: "small", label: "Small" },
  { value: "default", label: "Standard" },
  { value: "large", label: "Large" },
]

// The rail's three modes, named as the rail's own menu and ⌘B name them, in
// the same order (ODCH-SET-02).
const SIDEBAR_OPTIONS: { value: SidebarChoice; label: string }[] = [
  { value: "pinned", label: "Expanded" },
  { value: "hover", label: "Expand on hover" },
  { value: "collapsed", label: "Collapsed" },
]

// What each knob changes, and only that. Lines are listed the way the
// prototype does (subtle to strong); the stored values stay the registry's.
const KNOB_HINTS: Record<string, string> = {
  radius: "Scales every corner together. As the style keeps the style's own.",
  cards: "Outlined is flat with a hairline; Raised lifts cards off the page.",
  lines: "How strong borders and dividers are. Form field outlines stay readable.",
}
const KNOB_ORDER: Record<string, string[]> = { lines: ["subtle", "default", "strong"] }

// Light, Dark, System, as the Theme row of the prototype orders them.
const THEME_TILES: { value: ThemeChoice; label: string; icon: typeof Sun }[] = [
  { value: "light", label: "Light", icon: Sun },
  { value: "dark", label: "Dark", icon: Moon },
  { value: "system", label: "System", icon: Monitor },
]

const MOTION_OPTIONS: { value: MotionChoice; label: string }[] = [
  { value: "system", label: "As my system" },
  { value: "on", label: "On" },
  { value: "off", label: "Off" },
]

// v67 `.s-tp`: a wireframe card, not a screenshot — a sidebar of three bars
// (the first in the accent), a top bar and a card with an accent bar. The tile
// has to show the OTHER palette too (a Light tile drawn in the dark theme's
// tokens would just be another dark tile), so it carries literal colours;
// System is the one card split down the middle, light then dark.
const PREVIEW = {
  dark: { ground: "linear-gradient(135deg,#0B1220,#151B2E)", bar: "#1A2234", block: "#2A3550", card: "#121A2B", topbar: "#1A2234" },
  light: { ground: "linear-gradient(135deg,#EAF1FF,#F7F9FC)", bar: "#FFFFFF", block: "#DCE4F2", card: "#FFFFFF", topbar: "#FFFFFF" },
  system: { ground: "linear-gradient(115deg,#EAF1FF 0 50%,#0B1220 50% 100%)", bar: "#FFFFFF", block: "#DCE4F2", card: "#121A2B", topbar: "linear-gradient(90deg,#fff 0 40%,#1A2234 40%)" },
} as const

function ThemePreview({ variant }: { variant: keyof typeof PREVIEW }) {
  const p = PREVIEW[variant]
  const system = variant === "system"
  return (
    <div
      data-slot="theme-preview"
      data-variant={variant}
      aria-hidden
      className="grid h-[5.116rem] grid-cols-[26%_1fr] grid-rows-[0.8077rem_1fr] gap-1.5 overflow-hidden rounded-lg p-1.5"
      style={{ background: p.ground }}
    >
      <div data-slot="theme-preview-sidebar" className="row-span-2 grid content-start gap-1 rounded-md p-1" style={{ background: p.bar }}>
        <b className="block h-1 w-[70%] rounded-sm bg-[var(--brand)]" />
        <b className="block h-1 rounded-sm" style={{ background: p.block }} />
        <b className="block h-1 rounded-sm" style={{ background: p.block }} />
      </div>
      <div data-slot="theme-preview-topbar" className="rounded" style={{ background: p.topbar }} />
      <div data-slot="theme-preview-cards" className="grid grid-cols-2 gap-1 rounded-md p-1" style={{ background: p.card }}>
        <b className="col-span-full block h-2 w-[45%] rounded-sm bg-[var(--brand)]" />
        <b className="block rounded-sm" style={{ background: system ? "#2A3550" : p.block }} />
        <b className="block rounded-sm" style={{ background: system ? "#2A3550" : p.block }} />
      </div>
    </div>
  )
}

function ThemeTiles({ value, onChange, allowed }: { value: ThemeChoice | undefined; onChange: (t: ThemeChoice) => void; allowed: ThemeChoice[] }) {
  // As ThemeSwitcher: next-themes knows the stored theme only after
  // hydration, so nothing is pressed until then.
  const [mounted, setMounted] = useState(false)
  useEffect(() => setMounted(true), [])

  return (
    <div role="radiogroup" aria-label="Theme" className="grid grid-cols-3 gap-2.5 px-4 pb-4 pt-2">
      {THEME_TILES.map(({ value: option, label, icon: Icon }) => {
        const checked = mounted && value === option
        const disabled = !allowed.includes(option)
        return (
          <button
            key={option}
            type="button"
            role="radio"
            aria-checked={checked}
            aria-label={label}
            disabled={disabled}
            onClick={() => onChange(option)}
            className={cn(
              "group grid gap-2 rounded-xl border bg-card p-2 text-left outline-none transition-colors",
              "hover:border-line-strong focus-visible:ring-2 focus-visible:ring-ring/50",
              "disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:border-border",
              checked ? "border-primary ring-[3px] ring-primary/20" : "border-border",
            )}
          >
            <ThemePreview variant={option} />
            <span className="flex items-center gap-1.5 text-control font-semibold text-foreground">
              <Icon className="h-3.5 w-3.5" aria-hidden />
              {label}
            </span>
          </button>
        )
      })}
    </div>
  )
}

/**
 * The accent: eight presets in spectrum order, then the team colour, as one
 * radio group of small round swatches (v67 `.swatches`), named only by their
 * tooltip and accessible name. The team colour is someone else's colour (the
 * workspace brand, set in General), so without one it stays a disabled
 * swatch that says where it would come from.
 */
function AccentSwatches({ value, onChange, teamHex }: { value: AccentChoice; onChange: (id: AccentChoice) => void; teamHex: string | null | undefined }) {
  const swatch = "kit-tap size-[1.4808rem] shrink-0 rounded-full outline-none transition-shadow shadow-[inset_0_0_0_1px_rgb(0_0_0_/_0.12)] focus-visible:ring-2 focus-visible:ring-ring/50"
  const ring = "shadow-[0_0_0_2px_var(--card),0_0_0_4px_var(--sw)]"
  const teamChecked = value === WORKSPACE_ACCENT
  const teamAvailable = !!teamHex || teamChecked
  const teamTitle = teamHex
    ? "Team colour · the workspace brand colour from General › Artifacts appearance"
    : "Team colour · no brand colour yet. An admin can set one in General › Artifacts appearance."
  return (
    <div role="radiogroup" aria-label="Accent colour" className="flex flex-wrap gap-2">
      {ACCENTS.map(({ id, label, swatch: hex }) => {
        const checked = value === id
        return (
          <button
            key={id}
            type="button"
            role="radio"
            aria-checked={checked}
            aria-label={label}
            title={label}
            onClick={() => onChange(id)}
            className={cn(swatch, checked && ring)}
            style={{ background: hex, "--sw": hex } as CSSProperties}
          />
        )
      })}
      <button
        type="button"
        role="radio"
        aria-checked={teamChecked}
        aria-label="Team colour"
        title={teamTitle}
        disabled={!teamAvailable}
        onClick={() => onChange(WORKSPACE_ACCENT)}
        className={cn(swatch, "disabled:cursor-not-allowed disabled:opacity-50", teamChecked && ring)}
        style={{ background: teamHex ?? "conic-gradient(#E5484D,#F5A524,#30A46C,#0091FF,#8E4EC6,#E5484D)", "--sw": teamHex ?? "var(--primary)" } as CSSProperties}
      />
    </div>
  )
}

/** The swatch colour of any accent, for the live preview. */
function accentHexOf(id: AccentChoice, teamHex: string | null | undefined): string | undefined {
  if (id === WORKSPACE_ACCENT) return teamHex ?? undefined
  return ACCENTS.find((a) => a.id === id)?.swatch ?? STYLE_ACCENTS.find((a) => a.id === id)?.swatch
}

/**
 * Why Light and System are off while a single-scheme style is active, and the
 * way out. Harbor is the style that always has both.
 */
function SchemeHint({ styleId, onSwitch }: { styleId: string; onSwitch: () => void }) {
  const def = STYLES.find((s) => s.id === styleId)
  if (!def || def.schemes.length !== 1) return null
  const only = def.schemes[0]
  return (
    <div data-testid="scheme-hint" className="flex flex-wrap items-center gap-x-3 gap-y-2 border-t border-border/60 bg-surface-subtle/60 px-4 py-3">
      <p className="min-w-0 flex-1 text-label text-muted-foreground">
        {def.name} is a {only} style, so {only === "dark" ? "Light" : "Dark"} and System are off.
      </p>
      <Button size="sm" variant="outline" onClick={onSwitch} className="coarse:h-[44px]">
        Switch to {HARBOR.name}
      </Button>
    </div>
  )
}

export function AppearanceSection() {
  const { theme, setTheme, accent, setAccent, density, setDensity, motion, setMotion, textSize, setTextSize, sidebar, setSidebar, avatarAnimation, setAvatarAnimation, syncEnabled, setSyncEnabled, style, pickStyle, setKnob, explicit, resetToWorkspaceDefault } = useAppearance()
  const { resolvedTheme } = useTheme()
  // Every personal change below applies and saves at once, and offers Undo.
  const undoable = useUndoableAppearance()
  const syncStatus = useAppearanceSyncStatus()
  const workspaceHex = useWorkspaceAccentHex()
  const workspaceDefault = useWorkspaceAppearanceDefault()
  const [galleryOpen, setGalleryOpen] = useState(false)
  const scheme: Scheme = resolvedTheme === "light" || (resolvedTheme !== "dark" && theme === "light") ? "light" : "dark"
  const styleId = style?.style ?? HARBOR.id
  const rendered = STYLES.find((s) => s.id === renderedStyle(style, scheme)) ?? HARBOR
  const current = STYLES.find((s) => s.id === styleId) ?? HARBOR
  const hasDefault = Object.keys(workspaceDefault).length > 0
  const ownCount = (["theme", "accent", "style"] as const).filter((f) => explicit.has(f)).length
  const lookSentence =
    ownCount === 0
      ? hasDefault
        ? "This is the workspace default. Change anything below and it applies at once."
        : "This is the built-in look. Change anything below and it applies at once."
      : ownCount === 3 || !hasDefault
        ? "You chose this yourself. It applies here at once."
        : "You chose part of this yourself; the rest is the workspace default."

  return (
    <div className="space-y-5">
      <SettingsCard title="Your look" hideHeader>
        <div data-testid="your-look" className="flex flex-wrap items-center gap-4 p-4">
          <StylePreview style={rendered} scheme={scheme} accentHex={rendered.pack ? undefined : accentHexOf(accent, workspaceHex)} />
          <div className="min-w-0 flex-[1_1_14rem] space-y-0.5">
            <p className="text-label text-muted-foreground">Your look</p>
            <p data-testid="look-summary" className="text-body font-semibold text-foreground">
              {current.name} · {accentName(accent)} · {themeName(theme ?? "system")}
            </p>
            <p data-testid="look-source" className="text-label text-muted-foreground">{lookSentence}</p>
          </div>
          <Button
            variant="outline"
            size="sm"
            disabled={ownCount === 0}
            onClick={undoable(hasDefault ? "Back to the workspace default" : "Back to the built-in look", resetToWorkspaceDefault)}
            className="coarse:h-[44px]"
          >
            Reset to default
          </Button>
        </div>
      </SettingsCard>

      <SettingsCard icon={Palette} title="Colours" description="Light or dark, the accent, and the overall style">
        <div className="px-4 pt-3 text-control font-medium text-foreground">Theme</div>
        <ThemeTiles value={theme} onChange={undoable("Theme changed", setTheme)} allowed={allowedSchemes(styleId)} />
        <SchemeHint styleId={styleId} onSwitch={() => undoable("Style changed", pickStyle)(HARBOR.id)} />
        <SettingsRow
          label="Accent"
          description={
            <>
              Buttons, links and selection · <b data-testid="accent-name" className="font-medium text-foreground">{accentName(accent)}</b>
              {accent === WORKSPACE_ACCENT && !workspaceHex && (
                <span data-testid="workspace-accent-hint"> · This workspace has no brand colour yet, so Blue shows.</span>
              )}
            </>
          }
        >
          <AccentSwatches value={accent} onChange={undoable("Accent changed", setAccent)} teamHex={workspaceHex} />
        </SettingsRow>
        <SettingsRow
          label="Style"
          border={!galleryOpen}
          description={
            <>
              <b className="font-medium text-foreground">{current.name}</b> · {current.blurb}
            </>
          }
        >
          <span aria-hidden className="inline-flex h-6 w-[4.5rem] shrink-0 overflow-hidden rounded-md border border-border">
            {current.swatch.map((color, i) => (
              <i key={i} className="flex-1" style={{ background: color }} />
            ))}
          </span>
          <Button variant="outline" size="sm" aria-expanded={galleryOpen} onClick={() => setGalleryOpen((o) => !o)} className="coarse:min-h-[44px]">
            {galleryOpen ? "Done" : "Change style"}
          </Button>
        </SettingsRow>
        {galleryOpen && (
          <StyleGallery state={style} scheme={scheme} onPick={undoable("Style changed", pickStyle)}>
            <div className="border-t border-border/60">
              {Object.entries(KNOBS).map(([name, knob]) => {
                const order = KNOB_ORDER[name]
                const options = order ? order.map((v) => knob.options.find((o) => o.value === v)!) : knob.options
                return (
                  <SettingsRow key={name} label={knob.label} description={KNOB_HINTS[name]} className="bg-card">
                    <SettingsSegmented label={knob.label} options={options} value={knobValue(style, name)} onChange={undoable("Style changed", (v: string) => setKnob(name, v))} />
                  </SettingsRow>
                )
              })}
            </div>
          </StyleGallery>
        )}
      </SettingsCard>

      <SettingsCard icon={Type} title="Text and layout" description="How much fits on the screen">
        <SettingsRow label="Text size" description="Scales text, spacing and controls together, like zoom. Kept with your account.">
          <SettingsSegmented label="Text size" options={TEXT_SIZE_OPTIONS} value={textSize} onChange={undoable("Text size changed", setTextSize)} />
        </SettingsRow>
        <SettingsRow label="Density" description="Compact tightens lists and tables; text stays the same size.">
          <SettingsSegmented label="Density" options={DENSITY_OPTIONS} value={density} onChange={undoable("Density changed", setDensity)} />
        </SettingsRow>
        <SettingsRow label="Sidebar" description="Same as ⌘B and the switch at the bottom of the sidebar.">
          <SettingsSegmented label="Sidebar" options={SIDEBAR_OPTIONS} value={sidebar} onChange={undoable("Sidebar changed", setSidebar)} />
        </SettingsRow>
      </SettingsCard>

      <SettingsCard icon={Sparkles} title="Motion">
        <SettingsRow label="Reduce motion" description="Turns off transitions and looping animation. As my system follows your operating system.">
          <SettingsSegmented label="Reduce motion" options={MOTION_OPTIONS} value={motion} onChange={undoable("Motion changed", setMotion)} />
        </SettingsRow>
        <SettingsRow label="Animated avatars" description="Agent avatars blink and stretch now and then, at 32 px and up. Reduce motion On also keeps them still.">
          <Switch
            checked={avatarAnimation === "on"}
            onCheckedChange={undoable("Animated avatars changed", (on: boolean) => setAvatarAnimation(on ? "on" : "off"))}
            aria-label="Animated avatars"
          />
        </SettingsRow>
      </SettingsCard>

      <SettingsCard icon={Monitor} title="Devices and workspace" description="Where this look applies">
        <SettingsRow label="Sync across devices" description="Use this look wherever you sign in. Turning it on here adopts the choices already saved to your account.">
          <div className="flex items-center gap-3">
            {syncEnabled && syncStatus === "failed" && (
              <span className="flex items-center gap-2 text-label text-warn" role="status">
                Not synced
                <Button variant="outline" size="xs" onClick={retryAppearanceSync} className="coarse:h-[44px]">
                  Retry
                </Button>
              </span>
            )}
            <Switch checked={syncEnabled} onCheckedChange={setSyncEnabled} aria-label="Sync across devices" />
          </div>
        </SettingsRow>
        <WorkspaceDefaultRow />
      </SettingsCard>
    </div>
  )
}
