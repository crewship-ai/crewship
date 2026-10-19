"use client"
import { Button } from "@/components/ui/button"
import { useArtifactContextEnabled } from "./use-artifact-context-enabled"
import { switchableLinks, targetKey, targetLabel, type ConversationLink, type LinkedViewerTarget } from "./linked-viewer-targets"

interface ViewerLinkSwitcherProps {
  links: ConversationLink[]
  current: LinkedViewerTarget
  onSwitch: (target: LinkedViewerTarget) => void
  /** Moves a pinned revision to the explicit latest; never automatic. */
  onViewLatest?: () => void
}

/** Viewer header control: pinned revision vs explicit latest, and only accessible conversation links. */
export function ViewerLinkSwitcher({ links, current, onSwitch, onViewLatest }: ViewerLinkSwitcherProps) {
  const enabled = useArtifactContextEnabled()
  if (!enabled) return null
  const options = switchableLinks(links)
  const currentKey = targetKey(current)
  const inList = options.some(option => targetKey(option.target) === currentKey)
  // Same title and mode on two items: add the item id so they can be told apart.
  const text = (option: (typeof options)[number]) => `${option.title} · ${targetLabel(option.target)}`
  const counts = new Map<string, number>()
  options.forEach(option => counts.set(text(option), (counts.get(text(option)) ?? 0) + 1))
  return <div className="flex min-w-0 items-center gap-2">
    <span className="text-xs text-muted-foreground" aria-live="polite" data-testid="viewer-target-label">{targetLabel(current)}</span>
    {current.mode === "revision" && onViewLatest && <Button type="button" size="sm" variant="ghost" className="coarse:min-h-12" onClick={onViewLatest}>View latest</Button>}
    {(options.length > 1 || (options.length === 1 && !inList)) && <select aria-label="Linked items" className="min-w-0 rounded border bg-background p-1 text-sm coarse:min-h-12" value={inList ? currentKey : ""}
      onChange={event => { const next = options.find(option => targetKey(option.target) === event.target.value); if (next) onSwitch(next.target) }}>
      {!inList && <option value="" disabled>Select an item</option>}
      {options.map(option => <option key={targetKey(option.target)} value={targetKey(option.target)}>{text(option)}{(counts.get(text(option)) ?? 0) > 1 ? ` · ${option.target.pageId.slice(0, 8)}` : ""}</option>)}
    </select>}
  </div>
}
