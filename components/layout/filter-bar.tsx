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
    <div className="flex items-center gap-2">
      {filters.map((filter) => (
        <Badge
          key={filter}
          variant={filter === activeFilter ? "secondary" : "outline"}
          className="cursor-pointer"
          onClick={() => onFilter?.(filter)}
        >
          {filter}
        </Badge>
      ))}
    </div>
  )
}
