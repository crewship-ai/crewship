"use client"
import { Button } from "@/components/ui/button"
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
  const options = switchableLinks(links)
  const currentKey = targetKey(current)
  return <div className="flex min-w-0 items-center gap-2">
    <span className="text-xs text-muted-foreground" data-testid="viewer-target-label">{targetLabel(current)}</span>
    {current.mode === "revision" && onViewLatest && <Button type="button" size="sm" variant="ghost" className="coarse:min-h-12" onClick={onViewLatest}>View latest</Button>}
    {options.length > 1 && <select aria-label="Linked items" className="min-w-0 rounded border bg-background p-1 text-sm coarse:min-h-12" value={options.some(option => targetKey(option.target) === currentKey) ? currentKey : ""}
      onChange={event => { const next = options.find(option => targetKey(option.target) === event.target.value); if (next) onSwitch(next.target) }}>
      {!options.some(option => targetKey(option.target) === currentKey) && <option value="" disabled>Select an item</option>}
      {options.map(option => <option key={targetKey(option.target)} value={targetKey(option.target)}>{option.title} · {targetLabel(option.target)}</option>)}
    </select>}
  </div>
}
