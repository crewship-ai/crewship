"use client"

import { useEffect, useState } from "react"
import { Check, Contrast, Palette, Shapes, SlidersHorizontal, Users } from "lucide-react"
import { useTheme } from "next-themes"

import { cn } from "@/lib/utils"
import { Switch } from "@/components/ui/switch"
import { Button } from "@/components/ui/button"
import { THEME_OPTIONS } from "@/components/layout/theme-switcher"
import { useAppearance } from "@/hooks/use-appearance"
import { retryAppearanceSync, useAppearanceSyncStatus } from "@/hooks/use-appearance-sync"
import type { Density, MotionChoice, SidebarChoice, TextSize, ThemeChoice } from "@/lib/theme/appearance"
import { ACCENTS, WORKSPACE_ACCENT, type AccentChoice } from "@/lib/theme/accents"
import { HARBOR, KNOBS, STYLES, allowedSchemes, knobValue, type Scheme } from "@/lib/theme/appearance-style"
import { useWorkspaceAccentHex } from "@/components/layout/workspace-accent"
import { SettingsCard, SettingsRow, SettingsSegmented } from "../shared"
import { StyleGallery } from "./style-gallery"

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

// What each knob changes, and only that.
const KNOB_HINTS: Record<string, string> = {
  radius: "Scales every corner together. Style default keeps the style's own.",
  cards: "Border is flat with a hairline; Shadow lifts cards off the page.",
  lines: "How strong borders and dividers are. Form field outlines stay readable.",
}

const MOTION_OPTIONS: { value: MotionChoice; label: string }[] = [
  { value: "system", label: "System" },
  { value: "on", label: "On" },
  { value: "off", label: "Off" },
]

// The preview tiles have to show the OTHER palette too — a Light tile drawn
// in the dark theme's tokens would just be another dark tile — so they carry
// literal colours instead of the app tokens. Rough stand-ins for the two
// Harbor grounds; the accent bar is the live brand colour.
const PREVIEW = {
  dark: { ground: "#0B1220", rail: "#111A2C", card: "#16213A", line: "#2A3653" },
  light: { ground: "#F7F8FA", rail: "#FFFFFF", card: "#FFFFFF", line: "#E3E7EE" },
} as const

function ThemePreview({ palette }: { palette: (typeof PREVIEW)[keyof typeof PREVIEW] }) {
  return (
    <div className="flex h-full w-full" style={{ background: palette.ground }}>
      <div className="w-1/4" style={{ background: palette.rail }} />
      <div className="flex flex-1 flex-col gap-1.5 p-2">
        <div className="h-1.5 w-1/2 rounded-full bg-[var(--brand)]" />
        <div className="flex-1 rounded-md" style={{ background: palette.card, boxShadow: `inset 0 0 0 1px ${palette.line}` }} />
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
    <div role="radiogroup" aria-label="Theme" className="grid grid-cols-3 gap-3 p-4">
      {THEME_OPTIONS.map(({ value: option, label, icon: Icon }) => {
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
              "group flex flex-col gap-2 rounded-xl border p-1.5 text-left outline-none transition-colors",
              "hover:border-line-strong focus-visible:ring-2 focus-visible:ring-ring/50",
              "disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:border-border",
              checked ? "border-primary ring-1 ring-primary" : "border-border",
            )}
          >
            <div aria-hidden className="h-16 overflow-hidden rounded-lg border border-border/60">
              {option === "system" ? (
                // System is whichever the OS picks: half of each.
                <div className="flex h-full">
                  <div className="w-1/2"><ThemePreview palette={PREVIEW.dark} /></div>
                  <div className="w-1/2"><ThemePreview palette={PREVIEW.light} /></div>
                </div>
              ) : (
                <ThemePreview palette={PREVIEW[option]} />
              )}
            </div>
            <span className={cn("flex items-center gap-1.5 px-1 pb-0.5 text-control", checked ? "text-foreground" : "text-muted-foreground")}>
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
 * The accent as it will actually look, drawn with the live app tokens and
 * real components — a swatch alone does not show that the button is the
 * deeper "strong" shade or what a link or a chip turns into.
 */
function AccentPreview() {
  const [on, setOn] = useState(true)
  return (
    <div aria-label="Accent preview" role="group" className="flex flex-wrap items-center gap-x-4 gap-y-3 border-t border-border/60 bg-surface-subtle/60 px-4 py-3">
      <Button size="sm" tabIndex={-1}>Primary action</Button>
      <Button size="sm" variant="outline" tabIndex={-1}>Secondary</Button>
      <span className="text-control text-primary-hover underline-offset-2 hover:underline">A link</span>
      <span className="rounded-full bg-chip-info-bg px-2 py-0.5 text-label font-medium text-chip-info-fg">Chip</span>
      <Switch checked={on} onCheckedChange={setOn} aria-label="Preview switch" />
      <span className="flex h-7 items-center rounded-md border border-input bg-card px-2.5 text-label text-muted-foreground ring-2 ring-ring/50">Focused field</span>
    </div>
  )
}

/**
 * The presets as one row of large swatches, in spectrum order (blues, then
 * purples and pink, then the grey), the name under each. A 40px circle is a
 * real target and the colour reads at a glance; the pressed one carries a
 * check and an offset ring. The team colour is not a ninth circle: it is a
 * different KIND of choice (someone else's colour, set elsewhere), so it gets
 * its own row below — see TeamColourRow.
 */
function AccentCircles({ value, onChange }: { value: AccentChoice; onChange: (id: AccentChoice) => void }) {
  return (
    <div role="radiogroup" aria-label="Accent colour" className="flex flex-wrap gap-x-2 gap-y-4 px-4 pb-2 pt-5">
      {ACCENTS.map(({ id, label, swatch }) => {
        const checked = value === id
        return (
          <button
            key={id}
            type="button"
            role="radio"
            aria-checked={checked}
            aria-label={label}
            onClick={() => onChange(id)}
            className="group flex w-16 flex-col items-center gap-2 rounded-lg outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
          >
            <span
              aria-hidden
              className={cn(
                "grid h-10 w-10 place-items-center rounded-full transition-transform group-hover:scale-105",
                "shadow-[inset_0_0_0_1px_rgb(255_255_255_/_0.2),0_1px_3px_rgb(0_0_0_/_0.25)]",
                checked && "ring-2 ring-foreground/80 ring-offset-2 ring-offset-card",
              )}
              style={{ background: swatch }}
            >
              {checked && <Check className="h-4 w-4 text-white drop-shadow-[0_1px_1px_rgb(0_0_0_/_0.5)]" strokeWidth={3} />}
            </span>
            <span className={cn("text-label", checked ? "font-medium text-foreground" : "text-muted-foreground")}>{label}</span>
          </button>
        )
      })}
    </div>
  )
}

/**
 * The workspace's brand colour as the accent — or, when no admin has set
 * one, a note saying where it would come from. Selecting it is a radio in
 * the same group as the circles above (it is the same choice).
 */
function TeamColourRow({ hex, value, onChange }: { hex: string | null | undefined; value: AccentChoice; onChange: (id: AccentChoice) => void }) {
  const checked = value === WORKSPACE_ACCENT
  return (
    <div className="px-4 pb-4 pt-2">
      <div className={cn("flex items-center gap-3 rounded-xl border px-3 py-2.5", checked ? "border-primary bg-primary/5 ring-1 ring-primary" : "border-dashed border-border")} data-testid="workspace-accent-hint">
        <span
          aria-hidden
          className="grid h-8 w-8 shrink-0 place-items-center rounded-full shadow-[inset_0_0_0_1px_rgb(255_255_255_/_0.2)]"
          style={{ background: hex ?? "var(--muted)" }}
        >
          {hex ? (checked && <Check className="h-3.5 w-3.5 text-white drop-shadow-[0_1px_1px_rgb(0_0_0_/_0.5)]" strokeWidth={3} />) : <Users className="h-3.5 w-3.5 text-muted-foreground" />}
        </span>
        <span className="min-w-0 flex-1">
          <span className="block text-control font-medium">Team colour</span>
          <span className="block text-label text-muted-foreground">
            {hex
              ? "Your workspace's brand colour, adjusted where needed so text on it stays readable."
              : checked
                ? "This workspace has no brand colour yet, so Blue shows. An admin can set one in General › Pages appearance."
                : "An admin can set a brand colour in General › Pages appearance."}
          </span>
        </span>
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
      </div>
    </div>
  )
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
  const { theme, setTheme, accent, setAccent, density, setDensity, motion, setMotion, textSize, setTextSize, sidebar, setSidebar, syncEnabled, setSyncEnabled, style, pickStyle, resetStyle, setKnob } = useAppearance()
  const { resolvedTheme } = useTheme()
  const syncStatus = useAppearanceSyncStatus()
  const workspaceHex = useWorkspaceAccentHex()
  const scheme: Scheme = resolvedTheme === "light" || (resolvedTheme !== "dark" && theme === "light") ? "light" : "dark"
  const styleId = style?.style ?? HARBOR.id
  const atDefault = styleId === HARBOR.id && Object.keys(style?.knobs ?? {}).length === 0

  return (
    <div className="space-y-5">
      <SettingsCard
        icon={Shapes}
        title="Style"
        description="The surfaces, corners and colours of the whole app. Choosing one also suggests an accent."
        actions={
          <Button variant="outline" size="xs" onClick={resetStyle} disabled={atDefault} className="coarse:h-[44px]">
            Reset style
          </Button>
        }
      >
        <StyleGallery state={style} scheme={scheme} onPick={pickStyle} />
        <div className="border-t border-border/60">
          {Object.entries(KNOBS).map(([name, knob]) => (
            <SettingsRow key={name} label={knob.label} description={KNOB_HINTS[name]}>
              <SettingsSegmented label={knob.label} options={knob.options} value={knobValue(style, name)} onChange={(v) => setKnob(name, v)} />
            </SettingsRow>
          ))}
        </div>
      </SettingsCard>

      <SettingsCard icon={Contrast} title="Theme" description="Dark, light, or follow your system">
        <ThemeTiles value={theme} onChange={setTheme} allowed={allowedSchemes(styleId)} />
        <SchemeHint styleId={styleId} onSwitch={() => pickStyle(HARBOR.id)} />
      </SettingsCard>

      <SettingsCard icon={Palette} title="Accent" description="Buttons, links, selection and focus">
        <AccentCircles value={accent} onChange={setAccent} />
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
