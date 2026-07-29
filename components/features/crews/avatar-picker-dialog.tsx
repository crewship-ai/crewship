"use client"

import { useEffect, useState } from "react"
import { RefreshCw } from "lucide-react"
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog"
import { AVATAR_STYLES, getAgentAvatarUrl, DEFAULT_AVATAR_STYLE } from "@/lib/agent-avatar"
import { useAvatarStylesVersion } from "@/hooks/use-avatar-styles"
import { cn } from "@/lib/utils"

// Style options derived from the real DiceBear catalog in lib/agent-avatar.
// Values MUST be the actual style slug ("bottts-neutral", "adventurer", …)
// — earlier hand-typed labels ("robots", "humans") fell through to the
// default and silently kept the avatar Robots no matter what the user
// picked.
const STYLE_OPTIONS = Object.entries(AVATAR_STYLES).map(([value, meta]) => ({
  value,
  label: meta.label,
}))

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
  // Upgrade lazy-loaded DiceBear styles from placeholder to real avatar.
  useAvatarStylesVersion()
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

  const effectiveStyle = draftStyle ?? crewStyle ?? DEFAULT_AVATAR_STYLE
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
            className="w-24 h-24 rounded-2xl border border-white/10 bg-muted"
          />
        </div>

        {/* Style switcher — small previews so the user can compare faces. */}
        {/* Inherit is not a style, so it does not sit in the grid of styles.
            It used to, as a twelfth tile whose preview renders
            crewStyle ?? DEFAULT_AVATAR_STYLE — and the default is
            bottts-neutral, labelled "Robots" one tile over. For any agent
            whose crew has no style set, that was two adjacent tiles with the
            identical face and nothing saying why. It is a REFERENCE, so it
            says out loud what it currently resolves to. */}
        <div>
          <button
            type="button"
            onClick={() => setDraftStyle(null)}
            className={cn(
              "flex w-full items-center gap-2.5 rounded-lg border p-2 text-left transition-colors",
              draftStyle === null
                ? "border-primary bg-primary/10"
                : "border-white/10 hover:bg-white/5",
            )}
          >
            <img
              src={getAgentAvatarUrl(draftSeed, crewStyle ?? DEFAULT_AVATAR_STYLE)}
              alt=""
              className="h-8 w-8 rounded"
            />
            <span className="min-w-0 flex-1">
              <span className={cn("type-row block font-medium", draftStyle === null && "text-primary")}>
                Follow the crew
              </span>
              <span className="type-meta block truncate text-muted-foreground">
                {crewStyle
                  ? `currently ${AVATAR_STYLES[crewStyle]?.label ?? crewStyle}`
                  : `the crew has none set, so: ${AVATAR_STYLES[DEFAULT_AVATAR_STYLE].label} (the default)`}
              </span>
            </span>
          </button>
        </div>

        <div>
          <div className="type-meta mb-1.5 text-muted-foreground">Or pick one for this agent</div>
          <div data-testid="avatar-style-grid" className="grid grid-cols-3 gap-1.5">
            {STYLE_OPTIONS.map((s) => (
              <button
                key={s.value}
                type="button"
                onClick={() => setDraftStyle(s.value)}
                className={cn(
                  "rounded border text-xs transition-colors p-1.5 flex items-center gap-2",
                  draftStyle === s.value
                    ? "border-primary bg-primary/10 text-primary"
                    : "border-white/10 hover:bg-white/5",
                )}
              >
                <img
                  src={getAgentAvatarUrl(draftSeed, s.value)}
                  alt=""
                  className="w-7 h-7 rounded"
                />
                <span className="text-left flex-1 truncate">{s.label}</span>
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
                aria-label={`Use avatar seed ${qs}`}
                className={cn(
                  "rounded-lg overflow-hidden border transition-colors",
                  draftSeed === qs ? "border-primary" : "border-white/10 hover:border-white/25",
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
              className="text-[11px] flex items-center gap-1 text-primary hover:text-primary"
            >
              <RefreshCw className="h-3 w-3" />
              Regenerate
            </button>
          </div>
          <input
            type="text"
            value={draftSeed}
            onChange={(e) => setDraftSeed(e.target.value)}
            aria-label="Avatar seed"
            className="w-full bg-background border border-white/15 rounded px-2 py-1.5 text-sm font-mono outline-none focus:border-primary"
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
            className="text-sm px-3 py-1.5 rounded bg-primary hover:bg-primary text-white disabled:opacity-40"
          >
            {busy ? "Saving…" : "Save avatar"}
          </button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
