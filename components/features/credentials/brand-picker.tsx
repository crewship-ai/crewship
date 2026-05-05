"use client"

// BrandPicker — popover trigger that shows the currently-selected
// brand icon and lets the user override it from the full registry.
// Renders ~140 brand tiles in their official colours, grouped by
// category, with a search field on top. Used inline in the Add /
// Edit credential form.
//
// Design choices:
//   • Auto-detect from the typed name still wins until the user
//     manually picks; manual pick latches and overrides further name
//     changes (handled in the parent form, not here).
//   • Uses inline `style={{ color }}` because Tailwind classes can't
//     express ~140 arbitrary brand hex values cleanly.
//   • Category filter is a chip row, not a dropdown, so the user
//     sees what's available without an extra interaction.

import * as React from "react"
import { Search, ChevronDown, Check } from "lucide-react"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import {
  BRAND_REGISTRY,
  BRAND_CATEGORIES,
  GENERIC_BRAND,
  getBrand,
  type BrandCategory,
  type BrandEntry,
} from "@/lib/credential-providers/registry"
import { cn } from "@/lib/utils"

interface BrandPickerProps {
  value: string                    // current provider key
  onChange: (key: string) => void  // user picks a brand → returns the key
  className?: string
}

export function BrandPicker({ value, onChange, className }: BrandPickerProps) {
  const [open, setOpen] = React.useState(false)
  const [query, setQuery] = React.useState("")
  const [activeCat, setActiveCat] = React.useState<BrandCategory | "All">("All")

  const current = getBrand(value)
  const CurrentIcon = current.Icon

  const filtered = React.useMemo(() => {
    const q = query.trim().toLowerCase()
    return BRAND_REGISTRY.filter((b) => {
      if (activeCat !== "All" && b.category !== activeCat) return false
      if (!q) return true
      if (b.label.toLowerCase().includes(q)) return true
      if (b.key.toLowerCase().includes(q)) return true
      if (b.keywords?.some((k) => k.includes(q))) return true
      return false
    })
  }, [query, activeCat])

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className={cn("h-8 gap-1.5 px-2", className)}
          aria-label={`Provider: ${current.label}. Click to change.`}
        >
          <CurrentIcon
            className="h-3.5 w-3.5 shrink-0"
            style={{ color: current.hex }}
          />
          <span className="text-xs font-normal truncate max-w-[110px]">
            {current === GENERIC_BRAND ? "No brand" : current.label}
          </span>
          <ChevronDown className="h-3 w-3 opacity-50 shrink-0" />
        </Button>
      </PopoverTrigger>
      <PopoverContent
        align="start"
        className="w-[420px] p-0 max-h-[480px] flex flex-col"
      >
        {/* Search */}
        <div className="p-2 border-b border-white/10 flex-shrink-0">
          <div className="relative">
            <Search className="absolute left-2 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground" />
            <Input
              autoFocus
              placeholder="Search brands…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              className="pl-7 h-8 text-xs"
            />
          </div>
        </div>

        {/* Category chips */}
        <div className="px-2 pt-2 pb-1 border-b border-white/10 flex flex-wrap gap-1 flex-shrink-0">
          <CategoryChip
            label="All"
            active={activeCat === "All"}
            onClick={() => setActiveCat("All")}
          />
          {BRAND_CATEGORIES.map((c) => (
            <CategoryChip
              key={c}
              label={c}
              active={activeCat === c}
              onClick={() => setActiveCat(c)}
            />
          ))}
        </div>

        {/* Grid */}
        <div className="flex-1 overflow-y-auto p-2">
          {filtered.length === 0 ? (
            <p className="text-xs text-muted-foreground text-center py-8">
              No brand matches &ldquo;{query}&rdquo;.
              <br />
              <button
                type="button"
                onClick={() => { onChange("NONE"); setOpen(false) }}
                className="text-blue-400 hover:underline mt-2 inline-block"
              >
                Use generic icon →
              </button>
            </p>
          ) : (
            <div className="grid grid-cols-6 gap-1.5">
              {filtered.map((b) => (
                <BrandTile
                  key={b.key}
                  brand={b}
                  selected={b.key === value}
                  onClick={() => { onChange(b.key); setOpen(false) }}
                />
              ))}
            </div>
          )}
        </div>

        {/* Footer */}
        <div className="px-3 py-2 border-t border-white/10 flex items-center justify-between text-[10px] text-muted-foreground flex-shrink-0">
          <span>{filtered.length} brands</span>
          <button
            type="button"
            onClick={() => { onChange("NONE"); setOpen(false) }}
            className="hover:text-foreground"
          >
            No brand
          </button>
        </div>
      </PopoverContent>
    </Popover>
  )
}

function CategoryChip({
  label, active, onClick,
}: { label: string; active: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "h-5 px-2 rounded text-[10px] font-medium transition-colors",
        active
          ? "bg-blue-500/20 text-blue-300 border border-blue-400/40"
          : "border border-white/10 text-muted-foreground hover:text-foreground hover:border-white/20",
      )}
    >
      {label}
    </button>
  )
}

function BrandTile({
  brand, selected, onClick,
}: { brand: BrandEntry; selected: boolean; onClick: () => void }) {
  const Icon = brand.Icon
  return (
    <button
      type="button"
      onClick={onClick}
      title={brand.label}
      className={cn(
        "relative aspect-square rounded-md border flex flex-col items-center justify-center gap-1 p-1 transition-all hover:scale-105",
        selected
          ? "border-blue-400/60 bg-blue-500/10 ring-2 ring-blue-400/30"
          : "border-white/10 bg-zinc-950 hover:border-white/30",
      )}
    >
      {selected && (
        <Check className="absolute top-0.5 right-0.5 h-2.5 w-2.5 text-blue-400" />
      )}
      <Icon className="h-5 w-5" style={{ color: brand.hex }} />
      <span className="text-[8px] leading-none text-muted-foreground truncate max-w-full">
        {brand.label}
      </span>
    </button>
  )
}
