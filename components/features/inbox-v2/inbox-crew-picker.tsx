"use client"

import { useMemo, useState } from "react"
import { Check, ChevronsUpDown, Users } from "lucide-react"

import { CrewIcon } from "@/components/ui/crew-icon"
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { entryIdentity } from "./inbox-entry-identity"
import type { InboxLookup, InboxV2Entry } from "./inbox-v2-types"

/** A sidebar-sized crew selector: the actual crew icon, searchable name/slug,
 * and a current-view count make same-name crews distinguishable. */
export function InboxCrewPicker({ lookup, entries, value, onChange }: {
  lookup: InboxLookup
  entries: InboxV2Entry[]
  value: string | null
  onChange: (crewId: string | null) => void
}) {
  const [open, setOpen] = useState(false)
  const crews = useMemo(() => {
    const counts = new Map<string, number>()
    for (const entry of entries) {
      const id = entryIdentity(entry, lookup).crew?.id
      if (id) counts.set(id, (counts.get(id) ?? 0) + 1)
    }
    return [...lookup.crewById.values()]
      .map((crew) => ({ crew, count: counts.get(crew.id) ?? 0 }))
      .sort((a, b) => b.count - a.count || a.crew.name.localeCompare(b.crew.name) || a.crew.slug.localeCompare(b.crew.slug))
  }, [entries, lookup])
  const selected = value ? lookup.crewById.get(value) : null
  const withItems = crews.filter(({ count }) => count > 0)
  const otherCrews = crews.filter(({ count }) => count === 0)
  const option = ({ crew, count }: (typeof crews)[number]) => <CommandItem
    key={crew.id}
    value={`${crew.name} ${crew.slug} ${crew.id}`}
    onSelect={() => { onChange(crew.id); setOpen(false) }}
    className="min-h-11 gap-2 coarse:min-h-12"
  >
    <CrewIcon icon={crew.icon || "users"} color={crew.color} size="sm" className="!h-6 !w-6 !shrink-0 !rounded" />
    <span className="min-w-0 flex-1">
      <span className="block truncate text-xs text-foreground">{crew.name}</span>
      <span className="block truncate text-[10px] text-muted-foreground">{crew.slug}</span>
    </span>
    {count > 0 && <span className="text-[10px] tabular-nums text-muted-foreground">{count}</span>}
    {value === crew.id && <Check className="h-3.5 w-3.5 text-primary-hover" />}
  </CommandItem>

  return <Popover open={open} onOpenChange={setOpen}>
    <PopoverTrigger asChild>
      <button
        type="button"
        aria-label="Filter by crew"
        aria-expanded={open}
        className="kit-tap flex h-8 w-full min-w-0 items-center gap-2 rounded-md border border-white/[0.08] bg-white/[0.04] px-2.5 text-left text-xs text-foreground transition-colors hover:border-primary/30 focus-visible:outline-2 focus-visible:outline-primary coarse:h-12"
      >
        {selected
          ? <CrewIcon icon={selected.icon || "users"} color={selected.color} size="sm" className="!h-5 !w-5 !shrink-0 !rounded" />
          : <span className="flex h-5 w-5 shrink-0 items-center justify-center rounded bg-primary/10 text-primary-hover"><Users className="h-3.5 w-3.5" /></span>}
        <span className="min-w-0 flex-1 truncate">{selected?.name ?? "All crews"}</span>
        <ChevronsUpDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden />
      </button>
    </PopoverTrigger>
    <PopoverContent align="start" className="w-[min(340px,calc(100vw-24px))] p-0">
      <Command>
        <CommandInput placeholder="Find a crew or slug…" className="h-9 text-xs" />
        <CommandList className="max-h-[min(380px,55dvh)]">
          <CommandEmpty>No matching crew.</CommandEmpty>
          <CommandGroup>
            <CommandItem value="All crews" onSelect={() => { onChange(null); setOpen(false) }} className="min-h-10 gap-2 coarse:min-h-12">
              <span className="flex h-5 w-5 shrink-0 items-center justify-center rounded bg-primary/10 text-primary-hover"><Users className="h-3.5 w-3.5" /></span>
              <span className="min-w-0 flex-1 text-xs">All crews</span>
              <span className="text-[10px] tabular-nums text-muted-foreground">{entries.length}</span>
              {!value && <Check className="h-3.5 w-3.5 text-primary-hover" />}
            </CommandItem>
          </CommandGroup>
          {withItems.length > 0 && <CommandGroup heading="With items in this view">{withItems.map(option)}</CommandGroup>}
          {otherCrews.length > 0 && <CommandGroup heading={withItems.length ? "Other crews" : "Crews"}>{otherCrews.map(option)}</CommandGroup>}
        </CommandList>
      </Command>
    </PopoverContent>
  </Popover>
}
