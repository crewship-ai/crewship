"use client"

import { useEffect, useState } from "react"
import { Monitor, Moon, Sun, type LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { useAppearance } from "@/hooks/use-appearance"
import { ACCENTS, WORKSPACE_ACCENT, type AccentChoice } from "@/lib/theme/accents"
import { useWorkspaceAccentHex } from "@/components/layout/workspace-accent"
import type { ThemeChoice } from "@/lib/theme/appearance"

export const THEME_OPTIONS: { value: ThemeChoice; label: string; icon: LucideIcon }[] = [
  { value: "dark", label: "Dark", icon: Moon },
  { value: "light", label: "Light", icon: Sun },
  { value: "system", label: "System", icon: Monitor },
]

/**
 * Harbor theme picker: Dark, Light, System. A radio group rather than a
 * cycling button, because three states behind one icon hide which one you are
 * in. The stored choice is what shows as pressed; next-themes resolves System.
 *
 * Goes through useAppearance like every other appearance picker, so a change
 * here is the same change Settings › Appearance makes and the shell's
 * AppearanceSync writes it back for the user's other devices.
 *
 * `size="sm"` is the inline form for a menu row: 24px buttons, about 84px
 * wide in all, so it sits beside a label instead of taking a row of its own.
 */
export function ThemeSwitcher({ className, size = "default" }: { className?: string; size?: "default" | "sm" }) {
  const { theme, setTheme } = useAppearance()
  const sm = size === "sm"
  // next-themes only knows the stored theme after hydration; render the
  // group unpressed until then instead of flashing the wrong option.
  const [mounted, setMounted] = useState(false)
  useEffect(() => setMounted(true), [])

  return (
    <div
      role="radiogroup"
      aria-label="Theme"
      className={cn(
        "grid grid-cols-3 gap-0.5 border bg-surface-subtle p-0.5",
        sm ? "w-[84px] rounded-lg" : "rounded-xl",
        className,
      )}
    >
      {THEME_OPTIONS.map(({ value, label, icon: Icon }) => {
        const checked = mounted && theme === value
        return (
          <button
            key={value}
            type="button"
            role="radio"
            aria-checked={checked}
            aria-label={label}
            title={label}
            onClick={(e) => {
              // Inside a dropdown, keep the menu open so the change is visible.
              e.preventDefault()
              setTheme(value)
            }}
            className={cn(
              "flex items-center justify-center gap-1 text-micro font-medium text-muted-foreground transition-colors outline-none",
              sm ? "h-6 rounded-md" : "h-8 rounded-lg",
              "hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50",
              checked && "bg-card text-primary-hover shadow-xs",
            )}
          >
            <Icon className={sm ? "h-3 w-3" : "h-3.5 w-3.5"} aria-hidden />
          </button>
        )
      })}
    </div>
  )
}

/**
 * Accent theme picker: one swatch per entry in lib/theme/accents.ts. The
 * swatch colour comes from the registry, so adding an accent there adds it
 * here with no change to this component.
 *
 * `showLabels` prints each name under its swatch — Settings › Appearance has
 * the room, the menu does not (there the name is the tooltip and aria-label).
 */
type AccentOption = { id: AccentChoice; label: string; swatch: string }

export function AccentPicker({ className, showLabels = false }: { className?: string; showLabels?: boolean }) {
  const { accent, setAccent } = useAppearance()
  const workspaceHex = useWorkspaceAccentHex()
  // The workspace's brand colour, offered once an admin has set one — or kept
  // in the row while it is the user's choice, so the pressed swatch never
  // vanishes (it then paints Blue until a colour exists again).
  const options: AccentOption[] = ACCENTS.map(({ id, label, swatch }): AccentOption => ({ id, label, swatch }))
  if (workspaceHex || accent === WORKSPACE_ACCENT) {
    options.push({ id: WORKSPACE_ACCENT, label: "Workspace", swatch: workspaceHex ?? "var(--brand)" })
  }
  return (
    <div
      role="radiogroup"
      aria-label="Accent colour"
      className={cn("flex items-center", showLabels ? "flex-wrap gap-3" : "gap-1.5", className)}
    >
      {options.map(({ id, label, swatch }) => {
        const checked = accent === id
        const button = (
          <button
            key={id}
            type="button"
            role="radio"
            aria-checked={checked}
            aria-label={label}
            title={label}
            onClick={(e) => {
              // Inside a dropdown, keep the menu open so the change is visible.
              e.preventDefault()
              setAccent(id)
            }}
            className={cn(
              "grid h-7 w-7 place-items-center rounded-full outline-none transition-transform",
              "hover:scale-110 focus-visible:ring-2 focus-visible:ring-ring/50",
              // The offset ring is cut from whatever the picker sits on: the menu's
              // popover, or the settings card.
              checked && cn("ring-2 ring-foreground/70 ring-offset-2", showLabels ? "ring-offset-card" : "ring-offset-popover"),
            )}
          >
            <span className="h-5 w-5 rounded-full shadow-[inset_0_0_0_1px_rgb(255_255_255_/_0.25)]" style={{ background: swatch }} aria-hidden />
          </button>
        )
        if (!showLabels) return button
        return (
          <div key={id} className="flex w-14 flex-col items-center gap-1.5">
            {button}
            <span aria-hidden className={cn("text-label", checked ? "text-foreground" : "text-muted-foreground")}>{label}</span>
          </div>
        )
      })}
    </div>
  )
}
