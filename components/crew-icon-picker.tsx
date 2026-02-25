"use client"

import { useState, useMemo } from "react"
import { Search } from "lucide-react"
import { Input } from "@/components/ui/input"
import { Badge } from "@/components/ui/badge"
import { getCrewIconUrl, searchCrewIcons, CREW_ICON_CATEGORIES } from "@/lib/crew-icon"

interface CrewIconPickerProps {
  selected: string
  onSelect: (iconName: string) => void
}

export function CrewIconPicker({ selected, onSelect }: CrewIconPickerProps) {
  const [query, setQuery] = useState("")

  const results = useMemo(() => searchCrewIcons(query), [query])

  return (
    <div className="space-y-3">
      <div className="flex items-start gap-3">
        {selected && (
          <img
            src={getCrewIconUrl(selected)}
            alt={selected}
            className="h-14 w-14 rounded-xl border shrink-0"
          />
        )}
        <div className="flex-1 space-y-2">
          <div className="relative">
            <Search className="absolute left-2.5 top-2.5 h-3.5 w-3.5 text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search icons... (e.g. business, engineering, design)"
              className="pl-8 text-xs"
            />
          </div>
          <div className="flex flex-wrap gap-1">
            {CREW_ICON_CATEGORIES.map((cat) => (
              <Badge
                key={cat}
                variant={query === cat ? "default" : "outline"}
                className="cursor-pointer text-[10px] capitalize"
                onClick={() => setQuery(query === cat ? "" : cat)}
              >
                {cat}
              </Badge>
            ))}
          </div>
        </div>
      </div>

      <div className="grid grid-cols-8 gap-1.5 max-h-48 overflow-y-auto">
        {results.map((iconName) => (
          <button
            key={iconName}
            type="button"
            onClick={() => onSelect(iconName)}
            title={iconName}
            className={`rounded-lg border p-1.5 transition-colors hover:bg-muted ${
              selected === iconName ? "border-primary bg-primary/5 ring-1 ring-primary" : "border-border"
            }`}
          >
            <img
              src={getCrewIconUrl(iconName)}
              alt={iconName}
              className="h-7 w-7 rounded mx-auto"
            />
          </button>
        ))}
        {results.length === 0 && (
          <p className="col-span-8 text-center text-xs text-muted-foreground py-4">No icons found</p>
        )}
      </div>
    </div>
  )
}
