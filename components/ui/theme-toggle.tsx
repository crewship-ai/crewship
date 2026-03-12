"use client"

import { useTheme } from "next-themes"
import { Moon, Sun, Monitor } from "lucide-react"
import { cn } from "@/lib/utils"

export function ThemeToggle() {
  const { theme, setTheme } = useTheme()

  return (
    <div className="flex items-center gap-1 p-0.5 rounded-lg bg-muted/50 border">
      {[
        { value: "light", icon: Sun, label: "Light" },
        { value: "system", icon: Monitor, label: "System" },
        { value: "dark", icon: Moon, label: "Dark" },
      ].map(({ value, icon: Icon, label }) => (
        <button
          key={value}
          onClick={() => setTheme(value)}
          className={cn(
            "flex items-center gap-1.5 px-2.5 py-1 rounded-md text-[11px] font-medium transition-colors",
            theme === value
              ? "bg-background text-foreground shadow-sm"
              : "text-muted-foreground hover:text-foreground",
          )}
          title={label}
        >
          <Icon className="h-3 w-3" />
          {label}
        </button>
      ))}
    </div>
  )
}
