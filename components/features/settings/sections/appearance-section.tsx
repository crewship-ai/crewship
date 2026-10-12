"use client"

import { useEffect, useState } from "react"
import { Contrast, Layers, Palette, SlidersHorizontal } from "lucide-react"

import { cn } from "@/lib/utils"
import { Switch } from "@/components/ui/switch"
import { AccentPicker, THEME_OPTIONS } from "@/components/layout/theme-switcher"
import { useAppearance } from "@/hooks/use-appearance"
import type { Density, MotionChoice, ThemeChoice } from "@/lib/theme/appearance"
import { BASES, type BaseId } from "@/lib/theme/bases"
import { WORKSPACE_ACCENT } from "@/lib/theme/accents"
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
  light: { ground: "#F4F7FB", rail: "#E8EDF5", card: "#FFFFFF", line: "#D5DCE8" },
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

function ThemeTiles({ value, onChange }: { value: ThemeChoice | undefined; onChange: (t: ThemeChoice) => void }) {
  // As ThemeSwitcher: next-themes knows the stored theme only after
  // hydration, so nothing is pressed until then.
  const [mounted, setMounted] = useState(false)
  useEffect(() => setMounted(true), [])

  return (
    <div role="radiogroup" aria-label="Theme" className="grid grid-cols-3 gap-3 p-4">
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
              "group flex flex-col gap-2 rounded-xl border p-1.5 text-left outline-none transition-colors",
              "hover:border-line-strong focus-visible:ring-2 focus-visible:ring-ring/50",
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

/**
 * One tile per base: its light ground and card on the left, its dark ones on
 * the right, so both modes of a base are judged at once — the choice applies
 * to whichever theme is showing. Literal colours for the same reason as the
 * theme tiles; the bar is the live accent.
 */
function BaseTiles({ value, onChange }: { value: BaseId; onChange: (b: BaseId) => void }) {
  const [mounted, setMounted] = useState(false)
  useEffect(() => setMounted(true), [])
  return (
    <div role="radiogroup" aria-label="Base palette" className="grid grid-cols-2 gap-3 p-4 sm:grid-cols-4">
      {BASES.map((b) => {
        const checked = mounted && value === b.id
        return (
          <button
            key={b.id}
            type="button"
            role="radio"
            aria-checked={checked}
            aria-label={`${b.label}: ${b.description}`}
            onClick={() => onChange(b.id)}
            className={cn(
              "flex flex-col gap-2 rounded-xl border p-1.5 text-left outline-none transition-colors",
              "hover:border-line-strong focus-visible:ring-2 focus-visible:ring-ring/50",
              checked ? "border-primary ring-1 ring-primary" : "border-border",
            )}
          >
            <div aria-hidden className="flex h-14 overflow-hidden rounded-lg border border-border/60">
              {([b.light, b.dark] as const).map(([ground, card], i) => (
                <div key={i} className="flex flex-1 flex-col gap-1 p-1.5" style={{ background: ground }}>
                  <div className="h-1 w-1/2 rounded-full bg-[var(--brand)]" />
                  <div className="flex-1 rounded" style={{ background: card, boxShadow: `inset 0 0 0 1px ${i ? "#ffffff14" : "#0000000f"}` }} />
                </div>
              ))}
            </div>
            <span className="px-1 pb-0.5">
              <span className={cn("block text-control", checked ? "text-foreground" : "text-muted-foreground")}>{b.label}</span>
              <span className="block text-label text-muted-foreground-soft">{b.description}</span>
            </span>
          </button>
        )
      })}
    </div>
  )
}

export function AppearanceSection() {
  const { theme, setTheme, accent, base, setBase, density, setDensity, motion, setMotion, syncEnabled, setSyncEnabled } = useAppearance()
  const workspaceHex = useWorkspaceAccentHex()

  return (
    <div className="space-y-5">
      <SettingsCard icon={Contrast} title="Theme" description="Dark, light, or follow your system">
        <ThemeTiles value={theme} onChange={setTheme} />
      </SettingsCard>

      <SettingsCard icon={Palette} title="Accent" description="The one brand colour the interface is tinted with">
        <AccentPicker showLabels className="p-4" />
        {/* Where the Workspace swatch comes from — or why it is missing. */}
        <p className="border-t border-border/60 px-4 py-2.5 text-label text-muted-foreground" data-testid="workspace-accent-hint">
          {workspaceHex
            ? "Workspace uses your team's brand colour, adjusted where needed so text on it stays readable."
            : accent === WORKSPACE_ACCENT
              ? "This workspace has no brand colour yet, so Workspace shows Blue. An admin can set one in General › Pages appearance."
              : "An admin can set a team brand colour in General › Pages appearance; it then appears here as Workspace."}
        </p>
      </SettingsCard>

      <SettingsCard icon={Layers} title="Base" description="The neutral tone of backgrounds, cards, sidebar and borders">
        <BaseTiles value={base} onChange={setBase} />
      </SettingsCard>

      <SettingsCard icon={SlidersHorizontal} title="Layout & motion">
        <SettingsRow label="Density" description="Compact tightens list, table and settings rows. Text size stays the same.">
          <SettingsSegmented label="Density" options={DENSITY_OPTIONS} value={density} onChange={setDensity} />
        </SettingsRow>
        <SettingsRow label="Reduce motion" description="System follows your operating system's setting.">
          <SettingsSegmented label="Reduce motion" options={MOTION_OPTIONS} value={motion} onChange={setMotion} />
        </SettingsRow>
        <SettingsRow
          label="Sync across devices"
          description="Use these choices wherever you sign in. Turning it on here adopts the ones already saved to your account."
        >
          <Switch checked={syncEnabled} onCheckedChange={setSyncEnabled} aria-label="Sync across devices" />
        </SettingsRow>
      </SettingsCard>
    </div>
  )
}
