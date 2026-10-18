"use client"

import { ChevronDown } from "lucide-react"

import {
  DropdownMenu, DropdownMenuContent, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"

/**
 * The breadcrumb's last step as a chooser (PAR-084, v67 "Integrations > Tools v"):
 * the space the page is on, with the other spaces one click away. Picking
 * one goes through the page's own space switch, so unsaved edits are asked
 * about first.
 */
export function SpaceCrumb<T extends string>({
  area,
  value,
  options,
  onChange,
}: {
  /** "Integrations": names the control for assistive tech. */
  area: string
  value: T
  options: { key: T; label: string }[]
  onChange: (key: T) => void
}) {
  const current = options.find((o) => o.key === value)?.label ?? value
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        aria-label={`${area} space: ${current}`}
        data-slot="space-crumb"
        className="inline-flex items-center gap-1 rounded-md outline-none focus-visible:ring-2 focus-visible:ring-primary"
      >
        {current}
        <ChevronDown className="size-3 text-muted-foreground" aria-hidden />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-40">
        <DropdownMenuRadioGroup value={value} onValueChange={(v) => onChange(v as T)}>
          {options.map((o) => (
            <DropdownMenuRadioItem key={o.key} value={o.key}>{o.label}</DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
