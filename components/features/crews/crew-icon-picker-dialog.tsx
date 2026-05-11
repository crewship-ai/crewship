"use client"

import { useEffect, useMemo, useState } from "react"
import { Search } from "lucide-react"
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog"
import { CrewIcon } from "@/components/ui/crew-icon"
import { CREW_ICONS, GRADIENT_PALETTES } from "@/lib/entities"
import { cn } from "@/lib/utils"

export interface CrewIconPickerDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  crewName: string
  /** Current icon slug (from CREW_ICONS catalog). */
  icon: string | null
  /** Current color id (from GRADIENT_PALETTES). */
  color: string | null
  onSave: (next: { icon: string; color: string }) => void | Promise<void>
}

/**
 * Crew icon + color picker. Grid of all CREW_ICONS with search filter,
 * row of GRADIENT_PALETTES below. Live preview at the top so you see
 * the combination before committing.
 *
 * Replaces the inline ▾ dropdown that previously promised an icon
 * picker but had nothing wired up — clicking did nothing.
 */
export function CrewIconPickerDialog({
  open,
  onOpenChange,
  crewName,
  icon,
  color,
  onSave,
}: CrewIconPickerDialogProps) {
  const [draftIcon, setDraftIcon] = useState(icon ?? "briefcase")
  const [draftColor, setDraftColor] = useState(color ?? "blue")
  const [search, setSearch] = useState("")
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (open) {
      setDraftIcon(icon ?? "briefcase")
      setDraftColor(color ?? "blue")
      setSearch("")
    }
  }, [open, icon, color])

  const filteredIcons = useMemo(() => {
    if (!search.trim()) return CREW_ICONS
    const q = search.toLowerCase()
    return CREW_ICONS.filter((i) => i.name.toLowerCase().includes(q))
  }, [search])

  const submit = async () => {
    setBusy(true)
    try {
      await onSave({ icon: draftIcon, color: draftColor })
      onOpenChange(false)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Icon — {crewName}</DialogTitle>
          <DialogDescription>
            Pick an icon and a color. Same icon can be reused across crews
            with different colors as a quick visual differentiator.
          </DialogDescription>
        </DialogHeader>

        {/* Big preview */}
        <div className="flex items-center justify-center py-2">
          <CrewIcon icon={draftIcon} color={draftColor} size="xl" className="scale-150" />
        </div>

        {/* Color palette */}
        <div>
          <div className="text-xs text-muted-foreground mb-1.5">Color</div>
          <div className="flex items-center gap-1.5 flex-wrap">
            {GRADIENT_PALETTES.map((p) => (
              <button
                key={p.id}
                type="button"
                onClick={() => setDraftColor(p.id)}
                className={cn(
                  "w-7 h-7 rounded border-2 transition-all",
                  draftColor === p.id
                    ? "border-foreground scale-110"
                    : "border-transparent hover:border-white/20",
                )}
                style={{ background: p.dot }}
                title={p.id}
              />
            ))}
          </div>
        </div>

        {/* Icon grid */}
        <div>
          <div className="text-xs text-muted-foreground mb-1.5 flex items-center justify-between">
            <span>Icon</span>
            <span className="text-[10px] text-muted-foreground/60">
              {filteredIcons.length} of {CREW_ICONS.length}
            </span>
          </div>
          <div className="relative mb-2">
            <Search className="absolute left-2 top-1/2 -translate-y-1/2 h-3 w-3 text-muted-foreground" />
            <input
              type="text"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search icons…"
              className="w-full bg-zinc-950 border border-white/15 rounded pl-7 pr-2 py-1.5 text-xs outline-none focus:border-blue-400"
            />
          </div>
          <div className="grid grid-cols-10 gap-1 max-h-[260px] overflow-y-auto p-1 rounded bg-zinc-950/30 border border-white/5">
            {filteredIcons.map((i) => {
              const Icon = i.icon
              const active = draftIcon === i.name
              return (
                <button
                  key={i.name}
                  type="button"
                  onClick={() => setDraftIcon(i.name)}
                  title={i.name}
                  className={cn(
                    "aspect-square rounded grid place-items-center transition-colors",
                    active
                      ? "bg-blue-500/25 border border-blue-400"
                      : "hover:bg-white/5 border border-transparent",
                  )}
                >
                  <Icon className="h-4 w-4 text-foreground/80" />
                </button>
              )
            })}
            {filteredIcons.length === 0 && (
              <div className="col-span-10 text-center text-xs text-muted-foreground py-6">
                No icons match &ldquo;{search}&rdquo;
              </div>
            )}
          </div>
        </div>

        <DialogFooter>
          <button
            type="button"
            className="text-sm px-3 py-1.5 rounded text-muted-foreground hover:text-foreground"
            onClick={() => onOpenChange(false)}
            disabled={busy}
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={busy}
            className="text-sm px-3 py-1.5 rounded bg-blue-500 hover:bg-blue-400 text-white disabled:opacity-40"
          >
            {busy ? "Saving…" : "Save"}
          </button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
