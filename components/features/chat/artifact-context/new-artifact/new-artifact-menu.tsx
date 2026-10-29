"use client"
import { useState } from "react"
import { Plus, SendHorizontal, Sparkles, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { cn } from "@/lib/utils"
import type { ArtifactKind, ArtifactKindId } from "./new-artifact-kinds"

interface NewArtifactPanelProps {
  kinds: ArtifactKind[]
  /** Receives the starter prompt; the composer puts it in the draft for the human to edit before sending. */
  onPick: (kindId: ArtifactKindId, prompt: string) => void
  /** Renders the close button when given. */
  onClose?: () => void
  className?: string
  /** False when a surrounding element (the popover) already is the labelled dialog. */
  dialog?: boolean
}

const FOCUS = "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

/**
 * Kind tiles with the selected kind's starter prompts below. Usable on its
 * own (e.g. from the composer's "+" menu) or through NewArtifactMenu.
 */
export function NewArtifactPanel({ kinds, onPick, onClose, className, dialog = true }: NewArtifactPanelProps) {
  const [selected, setSelected] = useState<ArtifactKindId | null>(null)
  if (kinds.length === 0) return null
  const kind = kinds.find(k => k.id === selected) ?? kinds[0]

  return <div role={dialog ? "dialog" : undefined} aria-label={dialog ? "New artifact" : undefined} className={cn("grid gap-2.5 text-sm", className)}>
    <div className="flex items-center gap-2">
      <Sparkles aria-hidden className="size-4 text-muted-foreground" />
      <b className="font-semibold">New artifact</b>
      {onClose && <Button type="button" size="icon" variant="ghost" aria-label="Close" className="ml-auto size-7 coarse:size-[44px]" onClick={onClose}><X aria-hidden /></Button>}
    </div>
    <div className="grid grid-cols-2 gap-1.5 sm:grid-cols-[repeat(auto-fill,minmax(112px,1fr))]">
      {kinds.map(k => <button key={k.id} type="button" aria-pressed={k.id === kind.id} onClick={() => setSelected(k.id)}
        className={cn("grid min-w-0 justify-items-start gap-1.5 rounded-card border p-2 text-left hover:bg-muted", FOCUS, k.id === kind.id && "border-transparent ring-2 ring-primary")}>
        <span aria-hidden className={cn("grid h-11 w-full place-items-center rounded-row border", k.accent.chip)}><k.icon className={cn("size-5", k.accent.fg)} /></span>
        <b className="flex items-center gap-1 text-control font-semibold"><k.icon aria-hidden className={cn("size-3.5", k.accent.fg)} />{k.name}</b>
        <small className="text-xs text-muted-foreground">{k.description}</small>
      </button>)}
    </div>
    <ul aria-label={`${kind.name} starter prompts`} className="grid gap-1.5">
      {kind.starterPrompts.map(prompt => <li key={prompt}>
        <button type="button" onClick={() => onPick(kind.id, prompt)}
          className={cn("flex w-full items-center gap-2 rounded-card border px-2.5 py-2 text-left text-foreground/80 hover:bg-muted hover:text-foreground coarse:min-h-[44px]", FOCUS)}>
          <SendHorizontal aria-hidden className="size-3.5 shrink-0 text-muted-foreground" /><span>{prompt}</span>
        </button>
      </li>)}
    </ul>
  </div>
}

interface NewArtifactMenuProps {
  kinds: ArtifactKind[]
  onPick: (kindId: ArtifactKindId, prompt: string) => void
  disabled?: boolean
}

/** A "+" button that opens the panel above the composer. Hidden when no kind is served. */
export function NewArtifactMenu({ kinds, onPick, disabled }: NewArtifactMenuProps) {
  const [open, setOpen] = useState(false)
  if (kinds.length === 0) return null
  return <Popover open={open} onOpenChange={setOpen}>
    <PopoverTrigger asChild>
      <Button type="button" size="icon" variant="ghost" aria-label="New artifact" disabled={disabled} className="coarse:min-h-[44px] coarse:min-w-[44px]">
        <Plus aria-hidden className="size-4" />
      </Button>
    </PopoverTrigger>
    {/* Radix content already has role="dialog"; label it and keep the panel from nesting a second one. */}
    <PopoverContent aria-label="New artifact" align="start" side="top" className="max-h-[min(70vh,520px)] w-[min(28rem,calc(100vw-2rem))] overflow-auto rounded-xl p-3">
      <NewArtifactPanel dialog={false} kinds={kinds} onClose={() => setOpen(false)} onPick={(id, prompt) => { onPick(id, prompt); setOpen(false) }} />
    </PopoverContent>
  </Popover>
}
