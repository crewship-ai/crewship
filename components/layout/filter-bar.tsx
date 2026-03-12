"use client"

import { cn } from "@/lib/utils"

interface FilterBarProps {
  filters: string[]
  active?: string
  onFilter?: (filter: string) => void
}

export function FilterBar({ filters, active, onFilter }: FilterBarProps) {
  const activeFilter = active ?? filters[0]

  return (
    <div className="inline-flex items-center gap-0.5 rounded-lg border border-border bg-card p-1">
      {filters.map((filter) => (
        <button
          key={filter}
          onClick={() => onFilter?.(filter)}
          className={cn(
            "rounded-md px-3 py-1.5 text-label font-medium transition-all",
            filter === activeFilter
              ? "bg-accent text-foreground shadow-sm border border-border"
              : "text-muted-foreground hover:text-foreground border border-transparent"
          )}
        >
          {filter}
        </button>
      ))}
    </div>
  )
}
