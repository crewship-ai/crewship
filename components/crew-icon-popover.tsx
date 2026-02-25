"use client"

import { useState, useMemo, useRef, useEffect } from "react"
import { Search, Pencil } from "lucide-react"
import { Input } from "@/components/ui/input"
import {
  getCrewIconUrl, searchCrewIcons, CREW_ICON_CATEGORIES, ICON_COLORS,
} from "@/lib/crew-icon"

interface CrewIconPopoverProps {
  icon: string
  color: string
  onIconChange: (icon: string) => void
  onColorChange: (color: string) => void
}

export function CrewIconPopover({ icon, color, onIconChange, onColorChange }: CrewIconPopoverProps) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState("")
  const [activeCategory, setActiveCategory] = useState<string | null>(null)
  const anchorRef = useRef<HTMLDivElement>(null)

  const results = useMemo(() => {
    if (activeCategory) return searchCrewIcons(activeCategory)
    return searchCrewIcons(query)
  }, [query, activeCategory])

  useEffect(() => {
    if (!open) return
    function handleClick(e: MouseEvent) {
      if (anchorRef.current && !anchorRef.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    document.addEventListener("mousedown", handleClick)
    return () => document.removeEventListener("mousedown", handleClick)
  }, [open])

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

  const normalizedColor = color.replace("#", "")

  return (
    <div className="relative" ref={anchorRef}>
      <button
        type="button"
        onClick={() => setOpen(!open)}
        className="relative group cursor-pointer"
      >
        <img
          src={getCrewIconUrl(icon, color)}
          alt="Crew icon"
          className="h-14 w-14 rounded-2xl shadow-sm"
        />
        <div className="absolute inset-0 rounded-2xl bg-black/0 group-hover:bg-black/10 transition-all flex items-center justify-center">
          <Pencil className="h-3.5 w-3.5 text-white opacity-0 group-hover:opacity-100 transition-opacity drop-shadow-md" />
        </div>
      </button>

      {open && (
        <div className="absolute top-[68px] left-0 z-50 w-80 sm:w-96 bg-popover border rounded-2xl shadow-2xl overflow-hidden">
          {/* Color picker */}
          <div className="px-4 pt-4 pb-3 border-b bg-muted/30">
            <p className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider mb-2.5">
              Background Color
            </p>
            <div className="flex flex-wrap gap-2">
              {ICON_COLORS.map((c) => (
                <button
                  key={c}
                  type="button"
                  onClick={() => onColorChange(c)}
                  className={`w-7 h-7 rounded-full transition-all hover:scale-110 ${
                    normalizedColor === c
                      ? "ring-2 ring-primary ring-offset-2 ring-offset-background scale-110"
                      : "hover:ring-1 hover:ring-border hover:ring-offset-1"
                  }`}
                  style={{ backgroundColor: `#${c}` }}
                />
              ))}
            </div>
          </div>

          {/* Search + categories + icons */}
          <div className="p-4 space-y-3">
            <div className="relative">
              <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
              <Input
                value={query}
                onChange={(e) => handleSearchChange(e.target.value)}
                placeholder="Search icons..."
                className="pl-9 h-9"
              />
            </div>

            <div className="flex flex-wrap gap-1">
              {CREW_ICON_CATEGORIES.map((cat) => (
                <button
                  key={cat}
                  type="button"
                  onClick={() => handleCategoryClick(cat)}
                  className={`px-2 py-0.5 text-[10px] rounded-md capitalize transition-colors ${
                    activeCategory === cat
                      ? "bg-primary text-primary-foreground font-medium"
                      : "bg-muted text-muted-foreground hover:bg-muted/80"
                  }`}
                >
                  {cat}
                </button>
              ))}
            </div>

            <div className="grid grid-cols-8 gap-1.5 max-h-44 overflow-y-auto pt-1">
              {results.map((name) => (
                <button
                  key={name}
                  type="button"
                  title={name}
                  onClick={() => { onIconChange(name); setOpen(false) }}
                  className={`aspect-square rounded-xl border flex items-center justify-center transition-all ${
                    icon === name
                      ? "border-primary bg-primary/10 ring-1 ring-primary shadow-sm"
                      : "border-transparent hover:bg-muted hover:border-border"
                  }`}
                >
                  <img
                    src={getCrewIconUrl(name)}
                    alt={name}
                    className="w-5 h-5"
                  />
                </button>
              ))}
              {results.length === 0 && (
                <p className="col-span-8 text-center text-xs text-muted-foreground py-6">
                  No icons found
                </p>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
