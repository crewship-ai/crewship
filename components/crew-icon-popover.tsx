"use client"

import { useState, useMemo } from "react"
import { Search, Pencil } from "lucide-react"
import { Input } from "@/components/ui/input"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { CrewIcon } from "@/components/ui/crew-icon"
import {
  searchCrewIcons, getCrewIconDef,
  CREW_ICON_CATEGORIES, GRADIENT_PALETTES,
} from "@/lib/entities"
import { cn } from "@/lib/utils"

interface CrewIconPopoverProps {
  icon: string
  color: string
  size?: "sm" | "md" | "lg" | "xl"
  onIconChange: (icon: string) => void
  onColorChange: (color: string) => void
}

export function CrewIconPopover({ icon, color, size = "xl", onIconChange, onColorChange }: CrewIconPopoverProps) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState("")
  const [activeCategory, setActiveCategory] = useState<string | null>(null)

  const results = useMemo(() => {
    if (activeCategory) return searchCrewIcons(activeCategory)
    return searchCrewIcons(query)
  }, [query, activeCategory])

  function handleCategoryClick(cat: string) {
    if (activeCategory === cat) {
      setActiveCategory(null)
      setQuery("")
    } else {
      setActiveCategory(cat)
      setQuery("")
    }
  }

  function handleSearchChange(value: string) {
    setQuery(value)
    setActiveCategory(null)
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button type="button" className="relative group cursor-pointer" aria-label="Choose crew icon">
          <CrewIcon icon={icon} color={color} size={size} />
          <div className={cn(
            "absolute inset-0 bg-black/0 group-hover:bg-black/10 transition-all flex items-center justify-center",
            size === "xl" ? "rounded-2xl" : size === "lg" ? "rounded-xl" : size === "md" ? "rounded-xl" : "rounded-lg",
          )}>
            <Pencil className={cn(
              "text-white opacity-0 group-hover:opacity-100 transition-opacity drop-shadow-md",
              size === "sm" ? "h-2.5 w-2.5" : "h-3.5 w-3.5",
            )} />
          </div>
        </button>
      </PopoverTrigger>
      <PopoverContent className="w-[340px] sm:w-[400px] p-0 rounded-2xl" align="start" sideOffset={8}>
        {/* Live preview + color picker */}
        <div className="px-4 pt-4 pb-3 border-b">
          <div className="flex items-center justify-between mb-3">
            {GRADIENT_PALETTES.map((p) => {
              const active = color === p.id
              return (
                <button
                  key={p.id}
                  type="button"
                  onClick={() => onColorChange(p.id)}
                  className="transition-all hover:scale-105 shrink-0"
                >
                  <CrewIcon
                    icon={icon}
                    color={p.id}
                    size="sm"
                    className={cn(
                      "transition-all",
                      active ? "ring-2 ring-primary ring-offset-2 ring-offset-background scale-110" : "opacity-50 hover:opacity-100",
                    )}
                  />
                </button>
              )
            })}
          </div>
          <p className="text-micro text-muted-foreground">
            Pick a color, then choose an icon below
          </p>
        </div>

        {/* Search */}
        <div className="px-4 pt-3 pb-2">
          <div className="relative">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => handleSearchChange(e.target.value)}
              placeholder="Search icons..."
              className="pl-9 h-8 text-xs"
            />
          </div>
        </div>

        {/* Category chips */}
        <div className="px-4 pb-2">
          <div className="flex flex-wrap gap-1">
            {CREW_ICON_CATEGORIES.map((cat) => (
              <button
                key={cat}
                type="button"
                onClick={() => handleCategoryClick(cat)}
                className={cn(
                  "px-2 py-0.5 text-micro rounded-full capitalize transition-colors",
                  activeCategory === cat
                    ? "bg-primary text-primary-foreground font-medium"
                    : "bg-muted/60 text-muted-foreground hover:bg-muted hover:text-foreground",
                )}
              >
                {cat}
              </button>
            ))}
          </div>
        </div>

        {/* Icon grid */}
        <div className="px-4 pb-4">
          <div className="grid grid-cols-8 gap-1 max-h-[240px] overflow-y-auto rounded-lg border bg-muted/20 p-2">
            {results.map((name) => {
              const def = getCrewIconDef(name)
              const IconComp = def.icon
              const isSelected = icon === name
              return (
                <button
                  key={name}
                  type="button"
                  title={def.label}
                  onClick={() => { onIconChange(name); setOpen(false) }}
                  className={cn(
                    "aspect-square rounded-lg flex items-center justify-center transition-all",
                    isSelected
                      ? "bg-primary text-primary-foreground shadow-sm scale-110"
                      : "text-muted-foreground hover:bg-accent hover:text-foreground",
                  )}
                >
                  <IconComp className="h-4 w-4" />
                </button>
              )
            })}
            {results.length === 0 && (
              <p className="col-span-8 text-center text-xs text-muted-foreground py-8">
                No icons found
              </p>
            )}
          </div>
          <p className="text-micro text-muted-foreground mt-2 text-center">
            {results.length} icons {activeCategory ? `in ${activeCategory}` : "available"}
          </p>
        </div>
      </PopoverContent>
    </Popover>
  )
}
