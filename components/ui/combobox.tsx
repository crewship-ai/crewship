"use client"

import * as React from "react"
import { Check, ChevronsUpDown } from "lucide-react"

import { cn } from "@/lib/utils"
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"

export interface ComboboxOption {
  value: string
  label: string
  /** A second line: a role, a crew, a slug. Searchable. */
  description?: string
  /** Extra search terms (an email, a slug) that are not shown. */
  keywords?: string[]
  /** A leading icon or avatar. */
  icon?: React.ReactNode
  disabled?: boolean
}

/**
 * Combobox (Harbor 2 kit, COM-13): one searchable picker for dialogs and
 * forms, instead of local pickers and native `<select>`s. A button that names
 * the choice (`role="combobox"`), and a popover with a search field over the
 * labels, descriptions and keywords. Picking closes it.
 *
 * Inside a CreateSurface or another dialog pass `modal` (see the note in
 * components/ui/popover.tsx: without it the list will not scroll).
 */
export function Combobox({
  value,
  onChange,
  options,
  ariaLabel,
  placeholder = "Choose…",
  searchPlaceholder = "Search…",
  emptyText = "Nothing matches.",
  modal = false,
  disabled = false,
  id,
  className,
  contentClassName,
}: {
  value: string | null
  onChange: (value: string) => void
  options: readonly ComboboxOption[]
  /** The field's name ("Who", "Crew"). */
  ariaLabel: string
  /** Trigger text while nothing is chosen. */
  placeholder?: string
  searchPlaceholder?: string
  emptyText?: string
  modal?: boolean
  disabled?: boolean
  id?: string
  className?: string
  contentClassName?: string
}) {
  const [open, setOpen] = React.useState(false)
  const selected = options.find((o) => o.value === value) ?? null
  return (
    <Popover open={open} onOpenChange={setOpen} modal={modal}>
      <PopoverTrigger asChild>
        <button
          id={id}
          type="button"
          role="combobox"
          aria-label={ariaLabel}
          aria-expanded={open}
          aria-haspopup="listbox"
          disabled={disabled}
          data-slot="combobox"
          className={cn(
            "kit-tap flex h-[var(--control-md)] w-full min-w-0 items-center gap-2 rounded-md border border-control-border bg-card px-2.5 text-left text-control transition-colors",
            "hover:border-ring focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50",
            className,
          )}
        >
          {selected?.icon}
          <span className={cn("min-w-0 flex-1 truncate", selected ? "text-foreground" : "text-muted-foreground")}>
            {selected?.label ?? placeholder}
          </span>
          <ChevronsUpDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" className={cn("w-[min(360px,calc(100vw-24px))] p-0", contentClassName)}>
        <Command label={`Search ${ariaLabel}`}>
          <CommandInput placeholder={searchPlaceholder} aria-label={`Search ${ariaLabel}`} className="h-9 text-control" />
          <CommandList className="max-h-[min(360px,55dvh)]">
            <CommandEmpty>{emptyText}</CommandEmpty>
            <CommandGroup>
              {options.map((o) => {
                const on = o.value === value
                return (
                  <CommandItem
                    key={o.value}
                    value={`${o.label} ${o.value}`}
                    keywords={[...(o.description ? [o.description] : []), ...(o.keywords ?? [])]}
                    disabled={o.disabled}
                    data-checked={on ? "true" : undefined}
                    onSelect={() => {
                      if (o.disabled) return
                      onChange(o.value)
                      setOpen(false)
                    }}
                    className="min-h-10 gap-2 coarse:min-h-12"
                  >
                    {o.icon}
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-control text-foreground">{o.label}</span>
                      {o.description && <span className="block truncate text-micro text-muted-foreground">{o.description}</span>}
                    </span>
                    {on && <Check className="h-3.5 w-3.5 shrink-0 text-primary-hover" aria-hidden />}
                  </CommandItem>
                )
              })}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}
