"use client"

import { useEffect, useState } from "react"
import { Monitor, Moon, Sun, type LucideIcon } from "lucide-react"
import { useTheme } from "next-themes"

import { cn } from "@/lib/utils"
import { useAccent } from "@/hooks/use-accent"
import { ACCENTS } from "@/lib/theme/accents"

const OPTIONS: { value: string; label: string; icon: LucideIcon }[] = [
  { value: "dark", label: "Dark", icon: Moon },
  { value: "light", label: "Light", icon: Sun },
  { value: "system", label: "System", icon: Monitor },
]

/**
 * Harbor theme picker: Dark, Light, System. A radio group rather than a
 * cycling button, because three states behind one icon hide which one you are
 * in. The stored choice is what shows as pressed; next-themes resolves System.
 */
export function ThemeSwitcher({ className }: { className?: string }) {
  const { theme, setTheme } = useTheme()
  // next-themes only knows the stored theme after hydration; render the
  // group unpressed until then instead of flashing the wrong option.
  const [mounted, setMounted] = useState(false)
  useEffect(() => setMounted(true), [])

  return (
    <div
      role="radiogroup"
      aria-label="Theme"
      className={cn("grid grid-cols-3 gap-0.5 rounded-xl border bg-surface-subtle p-0.5", className)}
    >
      {OPTIONS.map(({ value, label, icon: Icon }) => {
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
              "flex h-8 items-center justify-center gap-1 rounded-lg text-micro font-medium text-muted-foreground transition-colors outline-none",
              "hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50",
              checked && "bg-card text-primary-hover shadow-xs",
            )}
          >
            <Icon className="h-3.5 w-3.5" aria-hidden />
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
 */
export function AccentPicker({ className }: { className?: string }) {
  const [accent, setAccent] = useAccent()
  return (
    <div role="radiogroup" aria-label="Accent colour" className={cn("flex items-center gap-1.5", className)}>
      {ACCENTS.map(({ id, label, swatch }) => {
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
