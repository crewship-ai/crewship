"use client"

import { useEffect, useState } from "react"
import { RefreshCw } from "lucide-react"
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { cn } from "@/lib/utils"

const STYLE_OPTIONS = [
  { value: "robots", label: "Robots" },
  { value: "humans", label: "Humans" },
  { value: "abstract", label: "Abstract" },
  { value: "pixel", label: "Pixel" },
] as const

export interface AvatarPickerDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  agentName: string
  /** Current seed; null/empty means "use name as seed". */
  seed: string | null
  /** Current style; null means inherit from crew. */
  style: string | null
  /** Crew's style for the inherit-fallback preview. */
  crewStyle: string | null
  onSave: (next: { avatar_seed: string; avatar_style: string | null }) => void | Promise<void>
}

/**
 * Avatar picker — opened by clicking the avatar in the canvas header
 * (or, in the crew member grid, by clicking any agent's portrait).
 *
 * Three modes of customisation:
 *   1) Seed quick-pick: 8 pre-generated faces from random seeds.
 *   2) Style switcher: Robots / Humans / Abstract / Pixel.
 *   3) Manual seed entry + Regenerate (random seed).
 *
 * Persists via PATCH /api/v1/agents/{id} with avatar_seed + avatar_style.
 */
export function AvatarPickerDialog({
  open,
  onOpenChange,
  agentName,
  seed,
  style,
  crewStyle,
  onSave,
}: AvatarPickerDialogProps) {
  const [draftSeed, setDraftSeed] = useState(seed ?? agentName)
  const [draftStyle, setDraftStyle] = useState<string | null>(style)
  const [quickSeeds, setQuickSeeds] = useState<string[]>([])
  const [busy, setBusy] = useState(false)

  // Re-seed dialog state on open and pre-generate the quick-pick row.
  useEffect(() => {
    if (!open) return
    setDraftSeed(seed ?? agentName)
    setDraftStyle(style)
    setQuickSeeds(
      Array.from({ length: 8 }, () => Math.random().toString(36).slice(2, 12)),
    )
  }, [open, seed, style, agentName])

  const effectiveStyle = draftStyle ?? crewStyle ?? "robots"
  const previewUrl = getAgentAvatarUrl(draftSeed, effectiveStyle)

  const submit = async () => {
    setBusy(true)
    try {
      await onSave({ avatar_seed: draftSeed, avatar_style: draftStyle })
      onOpenChange(false)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Avatar — {agentName}</DialogTitle>
          <DialogDescription>
            Pick a style and a seed. Same seed always produces the same face.
          </DialogDescription>
        </DialogHeader>

        {/* Big preview */}
        <div className="flex items-center justify-center py-2">
          <img
            src={previewUrl}
            alt=""
            className="w-24 h-24 rounded-2xl border border-white/10 bg-zinc-900"
          />
        </div>

        {/* Style switcher */}
        <div>
          <div className="text-xs text-muted-foreground mb-1.5">Style</div>
          <div className="grid grid-cols-4 gap-1.5">
            <button
              type="button"
              onClick={() => setDraftStyle(null)}
              className={cn(
                "px-2 py-1 rounded border text-xs transition-colors",
                draftStyle === null
                  ? "border-blue-400 bg-blue-500/10 text-blue-300"
                  : "border-white/10 hover:bg-white/5",
              )}
              title={crewStyle ? `Inherit from crew: ${crewStyle}` : "Inherit from crew"}
            >
              Inherit
            </button>
            {STYLE_OPTIONS.map((s) => (
              <button
                key={s.value}
                type="button"
                onClick={() => setDraftStyle(s.value)}
                className={cn(
                  "px-2 py-1 rounded border text-xs transition-colors",
                  draftStyle === s.value
                    ? "border-blue-400 bg-blue-500/10 text-blue-300"
                    : "border-white/10 hover:bg-white/5",
                )}
              >
                {s.label}
              </button>
            ))}
          </div>
        </div>

        {/* Quick-pick seeds */}
        <div>
          <div className="text-xs text-muted-foreground mb-1.5">Quick pick</div>
          <div className="grid grid-cols-8 gap-1.5">
            {quickSeeds.map((qs) => (
              <button
                key={qs}
                type="button"
                onClick={() => setDraftSeed(qs)}
                className={cn(
                  "rounded-lg overflow-hidden border transition-colors",
                  draftSeed === qs ? "border-blue-400" : "border-white/10 hover:border-white/25",
                )}
              >
                <img src={getAgentAvatarUrl(qs, effectiveStyle)} alt="" className="w-full h-auto" />
              </button>
            ))}
          </div>
        </div>

        {/* Manual seed entry */}
        <div>
          <div className="text-xs text-muted-foreground mb-1.5 flex items-center justify-between">
            <span>Seed</span>
            <button
              type="button"
              onClick={() => setDraftSeed(Math.random().toString(36).slice(2, 12))}
              className="text-[11px] flex items-center gap-1 text-blue-300 hover:text-blue-200"
            >
              <RefreshCw className="h-3 w-3" />
              Regenerate
            </button>
          </div>
          <input
            type="text"
            value={draftSeed}
            onChange={(e) => setDraftSeed(e.target.value)}
            className="w-full bg-zinc-950 border border-white/15 rounded px-2 py-1.5 text-sm font-mono outline-none focus:border-blue-400"
          />
          <div className="text-[11px] text-muted-foreground mt-1">
            Identical seeds across agents produce identical faces. Leave the agent name as the
            seed for a deterministic default.
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
            {busy ? "Saving…" : "Save avatar"}
          </button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
