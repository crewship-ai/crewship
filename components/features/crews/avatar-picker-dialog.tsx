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
export interface AvatarPickerBodyProps {
  /** Fallback seed when `seed` is empty — normally the agent's name. */
  agentName: string
  /** Current seed. Empty means "use agentName". */
  seed: string
  /** Current style; null means follow the crew. */
  style: string | null
  /** Crew's style, for the follow-the-crew preview. */
  crewStyle: string | null
  onChange: (next: { seed: string; style: string | null }) => void
}

/**
 * The picker itself — preview, style grid, quick seeds, manual seed.
 *
 * Split out of the dialog so the create-agent surface can render it as a
 * PANEL instead of stacking a second Radix dialog on top of the first. That
 * pattern was already removed from New crew's icon picker for the same
 * reasons: two overlays means two focus traps and two Escape handlers
 * fighting over one keystroke, the discard guard on the outer surface never
 * sees the inner one, and on a phone the inner dialog renders inside the
 * outer's bottom sheet at whatever width is left.
 *
 * Controlled, with no Save of its own. The dialog wraps it in draft state
 * and a footer; the panel writes straight through to the form's draft, the
 * way the crew icon picker does — there is nothing to commit because the
 * agent does not exist yet.
 */
export function AvatarPickerBody({
  agentName,
  seed,
  style,
  crewStyle,
  onChange,
}: AvatarPickerBodyProps) {
  // Upgrade lazy-loaded DiceBear styles from placeholder to real avatar.
  useAvatarStylesVersion()
  const draftSeed = seed || agentName
  const draftStyle = style

  // Generated once per mount rather than per open: both callers unmount this
  // when they close (Radix drops dialog content, the panel is conditional),
  // so a fresh row arrives each time without an effect watching `open`.
  const [quickSeeds] = useState(() =>
    Array.from({ length: 8 }, () => Math.random().toString(36).slice(2, 12)),
  )

  const setDraftSeed = (next: string) => onChange({ seed: next, style: draftStyle })
  const setDraftStyle = (next: string | null) => onChange({ seed: draftSeed, style: next })

  const effectiveStyle = draftStyle ?? crewStyle ?? DEFAULT_AVATAR_STYLE
  const previewUrl = getAgentAvatarUrl(draftSeed, effectiveStyle)

  return (
    <>
        {/* Four stacked full-width blocks — a 96px preview, a 25-tile grid of
            40px faces with labels, eight quick picks each a full grid column
            wide, and a seed field with its own heading — ran past 650px and
            pushed the quick picks off the bottom of the surface. Nothing is
            removed here; the same four things are laid out at a size that
            fits, because every one of them is a thumbnail and none needed to
            be the biggest thing on screen.

            The seed and its quick picks now sit WITH the preview: all three
            answer "which face", and splitting them across the panel with the
            style grid in between was what made the panel tall. */}
        <div className="flex items-center gap-3">
          <img
            src={previewUrl}
            alt=""
            data-testid="avatar-preview"
            className="h-14 w-14 shrink-0 rounded-xl border border-white/10 bg-muted"
          />
          <div className="flex min-w-0 flex-1 flex-col gap-1.5">
            <div className="flex items-center gap-2">
              <input
                type="text"
                value={draftSeed}
                onChange={(e) => setDraftSeed(e.target.value)}
                aria-label="Avatar seed"
                className="min-w-0 flex-1 rounded border border-white/15 bg-background px-2 py-1.5 font-mono text-sm outline-none focus:border-primary"
              />
              <button
                type="button"
                onClick={() => setDraftSeed(Math.random().toString(36).slice(2, 12))}
                className="flex shrink-0 items-center gap-1 text-[11px] text-primary hover:text-primary"
              >
                <RefreshCw className="h-3 w-3" />
                Regenerate
              </button>
            </div>

            {/* Fixed-size thumbnails rather than a grid of eight equal
                columns: at panel width each column was ~90px, so the quick
                picks rendered larger than the preview they feed. */}
            <div className="flex flex-wrap gap-1.5" data-testid="avatar-quick-pick">
              {quickSeeds.map((qs) => (
                <button
                  key={qs}
                  type="button"
                  onClick={() => setDraftSeed(qs)}
                  aria-label={`Use avatar seed ${qs}`}
                  className={cn(
                    "h-8 w-8 shrink-0 overflow-hidden rounded-md border transition-colors",
                    draftSeed === qs ? "border-primary" : "border-white/10 hover:border-white/25",
                  )}
                >
                  <img src={getAgentAvatarUrl(qs, effectiveStyle)} alt="" className="h-full w-full" />
                </button>
              ))}
            </div>
          </div>
        </div>

        <p className="type-meta text-muted-foreground">
          The same seed always produces the same face — leave the agent&apos;s name for a
          deterministic default.
        </p>

        {/* Inherit is not a style, so it does not sit in the grid of styles.
            It used to, as a twelfth tile whose preview renders
            crewStyle ?? DEFAULT_AVATAR_STYLE — and the default is
            bottts-neutral, labelled "Robots" one tile over. For any agent
            whose crew has no style set, that was two adjacent tiles with the
            identical face and nothing saying why. It is a REFERENCE, so it
            says out loud what it currently resolves to — now on one line
            rather than a stacked pair, which is the same sentence in half the
            height. */}
        <button
          type="button"
          onClick={() => setDraftStyle(null)}
          className={cn(
            "flex w-full items-center gap-2 rounded-lg border px-2 py-1.5 text-left transition-colors",
            draftStyle === null
              ? "border-primary bg-primary/10"
              : "border-white/10 hover:bg-white/5",
          )}
        >
          <img
            src={getAgentAvatarUrl(draftSeed, crewStyle ?? DEFAULT_AVATAR_STYLE)}
            alt=""
            className="h-6 w-6 shrink-0 rounded"
          />
          <span className={cn("type-row shrink-0 font-medium", draftStyle === null && "text-primary")}>
            Follow the crew
          </span>
          <span className="type-meta min-w-0 flex-1 truncate text-muted-foreground">
            {crewStyle
              ? `currently ${AVATAR_STYLES[crewStyle]?.label ?? crewStyle}`
              // Keeps naming the resolved style AND the word "default". Both
              // matter: with no crew style set this control draws the same
              // face as a tile in the grid below, and saying "the default" is
              // what stops that reading as a duplicate.
              : `none set — the default, ${AVATAR_STYLES[DEFAULT_AVATAR_STYLE].label}`}
          </span>
        </button>

        {/* The catalogue is 25 styles. As a three-column list of
            icon-beside-label tiles that was nine rows of mostly whitespace, and
            the labels won the eye over the faces — which are the thing being
            chosen. Face on top, name under it: the grid became something you
            scan rather than read. It is denser again here — the faces are the
            content, the labels are the caption, and at 32px five rows fit
            where three did. */}
        <div>
          <div className="type-meta mb-1.5 flex items-baseline gap-2 text-muted-foreground">
            <span>Or pick one for this agent</span>
            <span className="text-muted-foreground-soft">{STYLE_OPTIONS.length} styles</span>
          </div>
          <div
            data-testid="avatar-style-grid"
            className="grid max-h-[210px] grid-cols-5 gap-1 overflow-y-auto pr-1 sm:grid-cols-7"
          >
            {STYLE_OPTIONS.map((s) => (
              <button
                key={s.value}
                type="button"
                onClick={() => setDraftStyle(s.value)}
                title={s.label}
                className={cn(
                  "flex flex-col items-center gap-0.5 rounded-md border p-1 transition-colors",
                  draftStyle === s.value
                    ? "border-primary bg-primary/10"
                    : "border-white/10 hover:bg-white/5",
                )}
              >
                <img
                  src={getAgentAvatarUrl(draftSeed, s.value)}
                  alt=""
                  className="h-8 w-8 rounded"
                />
                <span
                  className={cn(
                    "w-full truncate text-center text-[9.5px] leading-tight",
                    draftStyle === s.value ? "text-primary" : "text-muted-foreground",
                  )}
                >
                  {s.label}
                </span>
              </button>
            ))}
          </div>
        </div>

    </>
  )
}

/**
 * The picker as a standalone dialog — the canvas header and the crew member
 * grid open it over a page, where a dialog is the right shape.
 *
 * Holds the draft so Cancel means something: nothing is written until Save
 * calls onSave, which PATCHes /api/v1/agents/{id}. The create-agent surface
 * does NOT use this — it renders AvatarPickerBody as a panel, because there
 * is no agent to PATCH yet and a dialog inside a dialog is the thing that
 * was wrong with it.
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
  const [busy, setBusy] = useState(false)

  // Re-seed from props on open: the dialog outlives any one agent in the
  // crew member grid, where clicking a second portrait reuses this instance.
  useEffect(() => {
    if (!open) return
    setDraftSeed(seed ?? agentName)
    setDraftStyle(style)
  }, [open, seed, style, agentName])

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
      <DialogContent className="max-h-[88vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Avatar — {agentName}</DialogTitle>
          <DialogDescription>
            Pick a style and a seed. Same seed always produces the same face.
          </DialogDescription>
        </DialogHeader>

        <AvatarPickerBody
          agentName={agentName}
          seed={draftSeed}
          style={draftStyle}
          crewStyle={crewStyle}
          onChange={(next) => { setDraftSeed(next.seed); setDraftStyle(next.style) }}
        />

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
