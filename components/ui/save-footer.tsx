"use client"

import { Check, Loader2 } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import type { SaveStatus } from "@/hooks/use-dirty-form"

/**
 * SaveFooter — the one Save affordance for cards that edit typed-in values.
 *
 * Design (see .claude/context/wireframes/save-affordance-system.html):
 *  · Renders NOTHING while the form is clean. A Save bar that is always
 *    there stops meaning anything; it earns its space by appearing.
 *  · Desktop: a strip pinned to the bottom of its card, so it is obvious
 *    which card it commits.
 *  · Mobile (<640px): the same element docks to the viewport bottom, where
 *    a card-bottom strip would sit below the fold. One implementation, two
 *    anchorings — not a separate mobile component.
 *  · Optional `reason` turns it into the audit-note form that policy and
 *    agent-autonomy writes require, gating Save until a note is typed.
 *
 * Pair with useDirtyForm, which owns the baseline/draft and status machine.
 * Atomic controls (switches, uploads, deletes) do NOT use this — they commit
 * on the spot and confirm with a toast.
 */
export function SaveFooter({
  dirty,
  status,
  error,
  onSave,
  onCancel,
  reason,
  onReasonChange,
  reasonLabel = "Reason (required)",
  reasonPlaceholder = "why are you making this change?",
  canSave = true,
  saveLabel = "Save",
  className,
}: {
  dirty: boolean
  status: SaveStatus
  error?: string | null
  onSave: () => void
  onCancel: () => void
  /** Provide together with onReasonChange to require an audit note. */
  reason?: string
  onReasonChange?: (value: string) => void
  reasonLabel?: string
  reasonPlaceholder?: string
  /** Caller-side validation veto (e.g. a forbidden option combination). */
  canSave?: boolean
  saveLabel?: string
  className?: string
}) {
  const saving = status === "saving"
  const saved = status === "saved"
  const failed = status === "error"
  const wantsReason = reason !== undefined && onReasonChange !== undefined

  // `saved` outlives `dirty`: submit() rebases the baseline the instant the
  // write lands, so the form is already clean when the confirmation shows.
  // Collapsing on dirty alone would swallow the only success signal.
  if (!dirty && !saved && !failed) return null

  const blocked = saving || !canSave || (wantsReason && reason.trim() === "")

  return (
    <div
      role="status"
      aria-live="polite"
      className={cn(
        "flex flex-col gap-2 px-4 py-2.5 border-t",
        saved
          ? "border-success/25 bg-success/[0.06]"
          : failed
            ? "border-destructive/30 bg-destructive/[0.05]"
            : "border-primary/25 bg-primary/[0.05]",
        // Docked on phones: full-bleed above the home indicator, lifted over
        // the content it belongs to.
        "max-sm:fixed max-sm:bottom-0 max-sm:left-0 max-sm:right-0 max-sm:z-40",
        "max-sm:border-t max-sm:bg-card max-sm:shadow-[0_-8px_24px_rgba(0,0,0,.45)]",
        "max-sm:pb-[max(0.625rem,env(safe-area-inset-bottom))]",
        className,
      )}
    >
      {wantsReason && !saved && (
        <div className="space-y-1.5">
          <label className="block text-[11px] uppercase tracking-wider text-muted-foreground">
            {reasonLabel}
          </label>
          <Input
            value={reason}
            onChange={(e) => onReasonChange(e.target.value)}
            placeholder={reasonPlaceholder}
            disabled={saving}
            className="h-7 text-xs"
          />
        </div>
      )}

      <div className="flex items-center justify-between gap-3">
        <span
          className={cn(
            "text-[11.5px] min-w-0 truncate",
            saved ? "text-success" : failed ? "text-destructive" : "text-primary-hover",
          )}
        >
          {saved
            ? "Saved"
            : failed
              ? (error ?? "Save failed")
              : "Unsaved changes"}
        </span>

        {!saved && (
          <div className="flex items-center gap-1.5 shrink-0 max-sm:flex-1 max-sm:justify-stretch">
            {/* type="button" is load-bearing: the shared Button leaves `type`
                unset, which HTML defaults to "submit". Dropped inside a
                <form> that would fire onSave AND the form's onSubmit for one
                click — two writes, visible only as a duplicate PATCH. */}
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={onCancel}
              disabled={saving}
              className="h-7 text-xs max-sm:flex-1"
            >
              Cancel
            </Button>
            <Button
              type="button"
              variant="soft"
              size="sm"
              onClick={onSave}
              disabled={blocked}
              className="h-7 gap-1.5 text-xs max-sm:flex-1"
            >
              {saving && <Loader2 className="h-3 w-3 animate-spin" />}
              {saving ? "Saving…" : saveLabel}
            </Button>
          </div>
        )}

        {saved && <Check className="h-3.5 w-3.5 text-success shrink-0" />}
      </div>
    </div>
  )
}
