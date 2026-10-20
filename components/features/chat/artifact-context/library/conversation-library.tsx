"use client"
import { Button } from "@/components/ui/button"
import { useArtifactContextEnabled } from "../use-artifact-context-enabled"
import { buildLibrary, type LibraryFilter, type LibraryItem } from "./library-model"

const FILTERS: { id: LibraryFilter; label: string }[] = [
  { id: "all", label: "All" }, { id: "files", label: "Files" }, { id: "artifacts", label: "Artifacts" }, { id: "tools", label: "Tools" }, { id: "work", label: "Work" },
]
interface Props {
  items: LibraryItem[]
  filter: LibraryFilter
  /** "unavailable" is a temporary failure and keeps what was loaded; it is not a revoke. */
  status?: "ready" | "loading" | "unavailable"
  onRetry?: () => void
  onFilterChange: (filter: LibraryFilter) => void
  onOpen: (linkId: string) => void
}

/** Linked-only conversation library. Restricted links render without name, type or owner. */
export function ConversationLibrary({ items, filter, status = "ready", onRetry, onFilterChange, onOpen }: Props) {
  const enabled = useArtifactContextEnabled()
  if (!enabled) return null
  const { counts, entries } = buildLibrary(items, filter)
  return <section aria-label="Conversation library" aria-busy={status === "loading" || undefined} className="space-y-3">
    <div role="group" aria-label="Filter library" className="flex flex-wrap gap-2">
      {FILTERS.map(option => <Button key={option.id} type="button" size="sm" variant={filter === option.id ? "default" : "outline"} aria-pressed={filter === option.id} className="coarse:min-h-[44px]" onClick={() => onFilterChange(option.id)}>{option.label} ({counts[option.id]})</Button>)}
    </div>
    {status === "unavailable" && <div role="alert" className="text-sm">
      <p>The library could not be refreshed. This is not a removal of access.</p>
      {onRetry && <Button type="button" size="sm" variant="outline" className="coarse:min-h-[44px]" onClick={onRetry}>Try again</Button>}
    </div>}
    {status === "loading" && entries.length === 0 ? <p role="status" className="text-sm text-muted-foreground">Loading library…</p>
      : entries.length === 0 ? <p role="status" className="text-sm text-muted-foreground">{filter === "all" ? "Nothing linked to this conversation yet." : "No linked items of this kind."}</p>
      : <ul className="space-y-1">{entries.map(entry => <li key={entry.linkId}>
        {entry.restricted ? <span className="text-sm text-muted-foreground">Restricted item</span>
          : <Button type="button" variant="ghost" className="h-auto w-full justify-start coarse:min-h-[44px]" onClick={() => onOpen(entry.linkId)}><span className="truncate">{entry.label}</span></Button>}
      </li>)}</ul>}
  </section>
}
