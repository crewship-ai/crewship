"use client"

import { useMemo, useState } from "react"
import { Check, ChevronDown, Users } from "lucide-react"
import { Command, CommandEmpty, CommandInput, CommandItem, CommandList } from "@/components/ui/command"
import { CrewIcon } from "@/components/ui/crew-icon"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { usePagedList } from "@/hooks/use-paged-list"
import { cn } from "@/lib/utils"
import type { ChatTreeAgent } from "./chat-tree-data"

interface CrewOption {
  id: string
  name: string
  icon?: string | null
  color?: string | null
}

function AllCrewsIcon() {
  return <span className="flex size-5 shrink-0 items-center justify-center rounded bg-white/[0.06] text-muted-foreground"><Users className="size-3" aria-hidden /></span>
}

/** A sidebar-sized crew selector using the same crew identity as the rest of the app. */
export function ChatCrewPicker({ workspaceId, agents, value, onChange }: {
  workspaceId: string | null
  agents: ChatTreeAgent[]
  value: string | null
  onChange: (crewId: string | null) => void
}) {
  const [open, setOpen] = useState(false)
  const crews = usePagedList<CrewOption>({
    url: workspaceId ? `/api/v1/crews?workspace_id=${encodeURIComponent(workspaceId)}` : null,
  })
  const selected = crews.items.find((crew) => crew.id === value)
  const counts = useMemo(() => {
    const result = new Map<string, number>()
    for (const agent of agents) if (agent.crew_id) result.set(agent.crew_id, (result.get(agent.crew_id) ?? 0) + 1)
    return result
  }, [agents])

  return <Popover open={open} onOpenChange={setOpen}>
    <PopoverTrigger asChild>
      <button type="button" role="combobox" aria-label="Filter agents by crew" aria-expanded={open} className={cn(
        "kit-tap mx-2 mb-1 flex h-8 w-[calc(100%-16px)] items-center gap-2 rounded-md border px-2 text-left text-xs transition-colors",
        value ? "border-primary/30 bg-primary/10 text-foreground" : "border-white/[0.08] bg-white/[0.03] text-muted-foreground hover:bg-white/[0.06]",
      )}>
        {selected ? <CrewIcon icon={selected.icon || "users"} color={selected.color} size="sm" className="!size-5 !rounded" /> : <AllCrewsIcon />}
        <span className="min-w-0 flex-1 truncate">{selected?.name ?? (value ? "Selected crew" : "All crews")}</span>
        {value && <span className="text-[10px] tabular-nums text-muted-foreground">{counts.get(value) ?? 0}</span>}
        <ChevronDown className="size-3 shrink-0 opacity-60" aria-hidden />
      </button>
    </PopoverTrigger>
    <PopoverContent align="start" sideOffset={4} className="w-[264px] max-w-[calc(100vw-24px)] p-1">
      <Command>
        <CommandInput placeholder="Search crews…" className="h-8 text-xs" />
        <CommandList>
          <CommandEmpty>{crews.loading ? "Loading crews…" : "No crews found."}</CommandEmpty>
          <CommandItem value="All crews" onSelect={() => { onChange(null); setOpen(false) }} className="min-h-8 gap-2 text-xs">
            <AllCrewsIcon /><span className="min-w-0 flex-1 truncate">All crews</span>
            {!value && <Check className="size-3.5 shrink-0 text-primary" aria-hidden />}
          </CommandItem>
          {crews.items.map((crew) => <CommandItem key={crew.id} value={`${crew.name} ${crew.id}`} onSelect={() => { onChange(value === crew.id ? null : crew.id); setOpen(false) }} className="min-h-8 gap-2 text-xs">
            <CrewIcon icon={crew.icon || "users"} color={crew.color} size="sm" className="!size-5 !rounded" />
            <span className="min-w-0 flex-1 truncate">{crew.name}</span>
            <span className="text-[10px] tabular-nums text-muted-foreground">{counts.get(crew.id) ?? 0}</span>
            {value === crew.id && <Check className="size-3.5 shrink-0 text-primary" aria-hidden />}
          </CommandItem>)}
        </CommandList>
      </Command>
      {crews.error && <button type="button" className="kit-tap w-full px-2 py-1.5 text-left text-xs text-muted-foreground hover:text-foreground" onClick={() => { void crews.refresh() }}>Could not load crews. Retry</button>}
      {crews.hasMore && <button type="button" disabled={crews.loadingMore} className="kit-tap w-full px-2 py-1.5 text-left text-xs text-primary" onClick={() => { void crews.loadMore() }}>{crews.loadingMore ? "Loading…" : "Load more crews"}</button>}
    </PopoverContent>
  </Popover>
}
