"use client"

import { useEffect, useState } from "react"
import { Check, Monitor, Moon, Sun, type LucideIcon } from "lucide-react"

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
 * `showLabels` is the Settings form: a grid of named tiles. Without it, the
 * compact row of dots for tight spots (the name is the tooltip and label).
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
  if (showLabels) {
    // Settings: a grid of named tiles. A 20px dot with a caption under it was
    // hard to aim at and read as a row of loose dots; a tile is a target the
    // size of its name and says which one is on with more than a thin ring.
    return (
      <div role="radiogroup" aria-label="Accent colour" className={cn("grid grid-cols-2 gap-2 sm:grid-cols-3 md:grid-cols-4", className)}>
        {options.map(({ id, label, swatch }) => {
          const checked = accent === id
          return (
            <button
              key={id}
              type="button"
              role="radio"
              aria-checked={checked}
              aria-label={label}
              onClick={() => setAccent(id)}
              className={cn(
                "group flex h-11 coarse:h-[44px] items-center gap-2.5 rounded-xl border px-2.5 text-left outline-none transition-colors",
                "hover:border-line-strong hover:bg-accent/40 focus-visible:ring-2 focus-visible:ring-ring/50",
                checked ? "border-primary bg-primary/5 ring-1 ring-primary" : "border-border",
              )}
            >
              <span
                aria-hidden
                className="grid h-6 w-6 shrink-0 place-items-center rounded-full shadow-[inset_0_0_0_1px_rgb(255_255_255_/_0.25),0_1px_2px_rgb(0_0_0_/_0.25)] transition-transform group-hover:scale-110"
                style={{ background: swatch }}
              >
                {checked && <Check className="h-3.5 w-3.5 text-white drop-shadow-[0_1px_1px_rgb(0_0_0_/_0.5)]" strokeWidth={3} />}
              </span>
              <span className={cn("truncate text-control", checked ? "font-medium text-foreground" : "text-muted-foreground")}>{label}</span>
            </button>
          )
        })}
      </div>
    )
  }
  return (
    <div
      role="radiogroup"
      aria-label="Accent colour"
      className={cn("flex items-center gap-1.5", className)}
    >
      {options.map(({ id, label, swatch }) => {
        const checked = accent === id
        return (
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
              checked && "ring-2 ring-foreground/70 ring-offset-2 ring-offset-popover",
            )}
          >
            <span className="h-5 w-5 rounded-full shadow-[inset_0_0_0_1px_rgb(255_255_255_/_0.25)]" style={{ background: swatch }} aria-hidden />
          </button>
        )
      })}
    </div>
  )
}
