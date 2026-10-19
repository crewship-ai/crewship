"use client"
import { Button } from "@/components/ui/button"
import { buildLibrary, type LibraryFilter, type LibraryItem } from "./library-model"

const FILTERS: { id: LibraryFilter; label: string }[] = [
  { id: "all", label: "All" }, { id: "files", label: "Files" }, { id: "artifacts", label: "Artifacts" }, { id: "tools", label: "Tools" }, { id: "work", label: "Work" },
]
interface Props { items: LibraryItem[]; filter: LibraryFilter; onFilterChange: (filter: LibraryFilter) => void; onOpen: (linkId: string) => void }

/** Linked-only conversation library. Restricted links render without name, type or owner. */
export function ConversationLibrary({ items, filter, onFilterChange, onOpen }: Props) {
  const { counts, entries } = buildLibrary(items, filter)
  return <section aria-label="Conversation library" className="space-y-3">
    <div role="group" aria-label="Filter library" className="flex flex-wrap gap-2">
      {FILTERS.map(option => <Button key={option.id} type="button" size="sm" variant={filter === option.id ? "default" : "outline"} aria-pressed={filter === option.id} className="coarse:min-h-12" onClick={() => onFilterChange(option.id)}>{option.label} ({counts[option.id]})</Button>)}
    </div>
    {entries.length === 0 ? <p role="status" className="text-sm text-muted-foreground">Nothing linked to this conversation yet.</p>
      : <ul className="space-y-1">{entries.map(entry => <li key={entry.linkId}>
        {entry.restricted ? <span className="text-sm text-muted-foreground">Restricted item</span>
          : <Button type="button" variant="ghost" className="h-auto w-full justify-start truncate coarse:min-h-12" onClick={() => onOpen(entry.linkId)}>{entry.label}</Button>}
      </li>)}</ul>}
  </section>
}
