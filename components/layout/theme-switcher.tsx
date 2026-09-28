"use client"

import { useEffect, useState } from "react"
import { Monitor, Moon, Sun, Sunset, type LucideIcon } from "lucide-react"
import { useTheme } from "next-themes"

import { cn } from "@/lib/utils"

const OPTIONS: { value: string; label: string; icon: LucideIcon }[] = [
  { value: "light", label: "Day", icon: Sun },
  { value: "dusk", label: "Dusk", icon: Sunset },
  { value: "dark", label: "Night", icon: Moon },
  { value: "system", label: "System", icon: Monitor },
]

/**
 * Harbor theme picker: Day, Dusk, Night, System. A radio group rather than a
 * cycling button, because four states behind one icon hide which one you are
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
      className={cn("grid grid-cols-4 gap-0.5 rounded-xl border bg-surface-subtle p-0.5", className)}
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
              "flex h-8 items-center justify-center gap-1 rounded-[10px] text-micro font-medium text-muted-foreground transition-colors outline-none",
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
