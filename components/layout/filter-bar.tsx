"use client"

import { Badge } from "@/components/ui/badge"

interface FilterBarProps {
  filters: string[]
  active?: string
  onFilter?: (filter: string) => void
}

export function FilterBar({ filters, active, onFilter }: FilterBarProps) {
  const activeFilter = active ?? filters[0]

  return (
    <div className="flex items-center gap-2 overflow-x-auto pb-1 -mb-1 scrollbar-none">
      {filters.map((filter) => (
        <Badge
          key={filter}
          variant={filter === activeFilter ? "secondary" : "outline"}
          className="cursor-pointer shrink-0"
          onClick={() => onFilter?.(filter)}
        >
          {filter}
        </Badge>
      ))}
    </div>
  )
}
