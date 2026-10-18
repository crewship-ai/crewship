"use client"

import { useEffect, useState, type CSSProperties } from "react"
import { Contrast, Palette, SlidersHorizontal } from "lucide-react"

import { cn } from "@/lib/utils"
import { Switch } from "@/components/ui/switch"
import { Button } from "@/components/ui/button"
import { THEME_OPTIONS } from "@/components/layout/theme-switcher"
import { useAppearance } from "@/hooks/use-appearance"
import { retryAppearanceSync, useAppearanceSyncStatus } from "@/hooks/use-appearance-sync"
import type { Density, MotionChoice, SidebarChoice, TextSize, ThemeChoice } from "@/lib/theme/appearance"
import { ACCENTS, WORKSPACE_ACCENT, type AccentChoice } from "@/lib/theme/accents"
import { useWorkspaceAccentHex } from "@/components/layout/workspace-accent"
import { SettingsCard, SettingsRow, SettingsSegmented } from "../shared"

// Settings › Account › Appearance.
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

// The rail's three modes, in the words the rail's own menu uses.
const SIDEBAR_OPTIONS: { value: SidebarChoice; label: string }[] = [
  { value: "hover", label: "Rail · expand on hover" },
  { value: "collapsed", label: "Rail" },
  { value: "pinned", label: "Pinned open" },
]

const MOTION_OPTIONS: { value: MotionChoice; label: string }[] = [
  { value: "system", label: "System" },
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

function ThemeTiles({ value, onChange }: { value: ThemeChoice | undefined; onChange: (t: ThemeChoice) => void }) {
  // As ThemeSwitcher: next-themes knows the stored theme only after
  // hydration, so nothing is pressed until then.
  const [mounted, setMounted] = useState(false)
  useEffect(() => setMounted(true), [])

  return (
    <div role="radiogroup" aria-label="Theme" className="grid grid-cols-3 gap-2.5 px-4 pb-4 pt-3.5">
      {THEME_OPTIONS.map(({ value: option, label, icon: Icon }) => {
        const checked = mounted && value === option
        return (
          <button
            key={option}
            type="button"
            role="radio"
            aria-checked={checked}
            aria-label={label}
            onClick={() => onChange(option)}
            className={cn(
              "group grid gap-2 rounded-xl border bg-card p-2 text-left outline-none transition-colors",
              "hover:border-line-strong focus-visible:ring-2 focus-visible:ring-ring/50",
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

// The three light rails, as the wireframe drew them: rail colour, the white
// section panel beside it, and the page ground.
/**
 * The accent as it will actually look (v67 `.s-prev`): the primary button, a
 * link, a selected row and a focused field, drawn with the live app tokens —
 * a swatch alone does not show the deeper "strong" shade of the button or
 * what a link turns into.
 */
function AccentPreview() {
  return (
    <div aria-label="Accent preview" role="group" className="flex flex-wrap items-center gap-3 border-t border-border bg-surface-subtle px-4 py-3">
      <Button size="sm" tabIndex={-1}>Primary</Button>
      <span className="text-control text-primary-hover underline-offset-2 hover:underline">A link</span>
      <span className="rounded-lg bg-chip-info-bg px-2.5 py-1 text-label text-chip-info-fg">Selected row</span>
      <span className="flex h-[var(--control-sm)] items-center rounded-md border border-primary bg-card px-2.5 text-control text-foreground ring-[3px] ring-primary/20">Focused field</span>
    </div>
  )
}

/**
 * The presets as one row of small round swatches (v67 `.swatches`), in
 * spectrum order, named only by their tooltip and their accessible name: a
 * 22 px circle is enough to read the colour, and the pressed one carries an
 * offset ring in its own colour. The team colour is not a ninth circle: it is a
 * different KIND of choice (someone else's colour, set elsewhere), so it gets
 * its own row below — see TeamColourRow.
 */
function AccentSwatches({ value, onChange }: { value: AccentChoice; onChange: (id: AccentChoice) => void }) {
  return (
    <div role="radiogroup" aria-label="Accent colour" className="flex flex-wrap gap-2">
      {ACCENTS.map(({ id, label, swatch }) => {
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
            className={cn(
              "kit-tap size-[1.4808rem] shrink-0 rounded-full outline-none transition-shadow",
              "shadow-[inset_0_0_0_1px_rgb(0_0_0_/_0.12)] focus-visible:ring-2 focus-visible:ring-ring/50",
              checked && "shadow-[0_0_0_2px_var(--card),0_0_0_4px_var(--sw)]",
            )}
            style={{ background: swatch, "--sw": swatch } as CSSProperties}
          />
        )
      })}
    </div>
  )
}

/**
 * The workspace's brand colour as the accent — or, when no admin has set
 * one, a note saying where it would come from. Selecting it is a radio in
 * the same group as the swatches above (it is the same choice).
 */
function TeamColourRow({ hex, value, onChange }: { hex: string | null | undefined; value: AccentChoice; onChange: (id: AccentChoice) => void }) {
  const checked = value === WORKSPACE_ACCENT
  return (
    <SettingsRow
      data-testid="workspace-accent-hint"
      label="Team colour"
      description={hex
        ? "The workspace brand colour from General › Pages appearance"
        : checked
          ? "This workspace has no brand colour yet, so Blue shows. An admin can set one in General › Pages appearance."
          : "An admin can set a brand colour in General › Pages appearance."}
    >
      <span
        aria-hidden
        className="grid size-[1.4808rem] shrink-0 place-items-center rounded-full shadow-[inset_0_0_0_1px_rgb(0_0_0_/_0.12)]"
        style={{ background: hex ?? "conic-gradient(#E5484D,#F5A524,#30A46C,#0091FF,#8E4EC6,#E5484D)" }}
      />
      {(hex || checked) && (
        <Button
          size="sm"
          variant={checked ? "secondary" : "outline"}
          role="radio"
          aria-checked={checked}
          aria-label="Team colour"
          onClick={() => onChange(WORKSPACE_ACCENT)}
          disabled={checked}
        >
          {checked ? "In use" : "Use"}
        </Button>
      )}
    </SettingsRow>
  )
}

export function AppearanceSection() {
  const { theme, setTheme, accent, setAccent, density, setDensity, motion, setMotion, textSize, setTextSize, sidebar, setSidebar, syncEnabled, setSyncEnabled } = useAppearance()
  const syncStatus = useAppearanceSyncStatus()
  const workspaceHex = useWorkspaceAccentHex()

  return (
    <div className="space-y-5">
      <SettingsCard icon={Contrast} title="Theme" description="Dark, light, or follow your system">
        <ThemeTiles value={theme} onChange={setTheme} />
      </SettingsCard>

      <SettingsCard icon={Palette} title="Accent" description="Buttons, links, selection and focus">
        <SettingsRow label="Colour">
          <AccentSwatches value={accent} onChange={setAccent} />
        </SettingsRow>
        <TeamColourRow hex={workspaceHex} value={accent} onChange={setAccent} />
        <AccentPreview />
      </SettingsCard>

      <SettingsCard icon={SlidersHorizontal} title="Layout & motion">
        <SettingsRow label="Text size" description="Scales text, spacing and controls together, like zoom — kept with your account.">
          <SettingsSegmented label="Text size" options={TEXT_SIZE_OPTIONS} value={textSize} onChange={setTextSize} />
        </SettingsRow>
        <SettingsRow label="Density" description="Compact tightens list, table and settings rows. Text size stays the same.">
          <SettingsSegmented label="Density" options={DENSITY_OPTIONS} value={density} onChange={setDensity} />
        </SettingsRow>
        <SettingsRow label="Sidebar" description="How the left rail behaves. Same as the switch at the bottom of the rail and ⌘B.">
          <SettingsSegmented label="Sidebar" options={SIDEBAR_OPTIONS} value={sidebar} onChange={setSidebar} />
        </SettingsRow>
        <SettingsRow label="Reduce motion" description="System follows your operating system's setting.">
          <SettingsSegmented label="Reduce motion" options={MOTION_OPTIONS} value={motion} onChange={setMotion} />
        </SettingsRow>
        <SettingsRow
          label="Sync across devices"
          description="Use these choices wherever you sign in. Turning it on here adopts the ones already saved to your account."
        >
          <div className="flex items-center gap-3">
            {syncEnabled && syncStatus === "failed" && (
              <span className="flex items-center gap-2 text-label text-warn" role="status">
                Not synced
                <Button variant="outline" size="xs" onClick={retryAppearanceSync}>
                  Retry
                </Button>
              </span>
            )}
            <Switch checked={syncEnabled} onCheckedChange={setSyncEnabled} aria-label="Sync across devices" />
          </div>
        </SettingsRow>
      </SettingsCard>
    </div>
  )
}
