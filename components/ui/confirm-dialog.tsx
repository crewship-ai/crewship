"use client"

import * as React from "react"
import { AlertTriangle, Check, X } from "lucide-react"

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { cn } from "@/lib/utils"

/**
 * The one confirmation for an irreversible action (README §2): what is lost,
 * what is kept, where to recover — and, for the actions that take a whole
 * crew with them, the name typed back. Replaces the browser's `confirm()`,
 * which said one sentence in the browser's own chrome and could not be styled,
 * tested or read by a screen reader as part of the page.
 */
export type ConsequenceTone = "lost" | "kept" | "warn"

export interface Consequence {
  tone: ConsequenceTone
  text: React.ReactNode
}

export interface ConfirmDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  /** One sentence: what this does. */
  description?: React.ReactNode
  /** What is lost, kept, or worth a warning — each its own line. */
  consequences?: Consequence[]
  confirmLabel: string
  cancelLabel?: string
  /** Red confirm for deletes and revokes; the default is the soft primary. */
  destructive?: boolean
  /** Require this exact text before the confirm button enables. */
  typeToConfirm?: string
  /** Called on confirm; the dialog closes when it resolves. */
  onConfirm: () => void | Promise<void>
}

const TONE_ICON: Record<ConsequenceTone, { Icon: React.ComponentType<{ className?: string }>; className: string }> = {
  lost: { Icon: X, className: "text-destructive" },
  kept: { Icon: Check, className: "text-success" },
  warn: { Icon: AlertTriangle, className: "text-warn" },
}

export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  consequences = [],
  confirmLabel,
  cancelLabel = "Cancel",
  destructive = false,
  typeToConfirm,
  onConfirm,
}: ConfirmDialogProps) {
  const [typed, setTyped] = React.useState("")
  const [busy, setBusy] = React.useState(false)
  const inputId = React.useId()

  React.useEffect(() => {
    if (!open) {
      setTyped("")
      setBusy(false)
    }
  }, [open])

  const gated = typeToConfirm != null && typed.trim() !== typeToConfirm
  const disabled = busy || gated

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent data-testid="confirm-dialog">
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          {description && <AlertDialogDescription>{description}</AlertDialogDescription>}
        </AlertDialogHeader>
        {consequences.length > 0 && (
          <ul className="flex flex-col gap-1.5 text-label">
            {consequences.map((c, i) => {
              const { Icon, className } = TONE_ICON[c.tone]
              return (
                <li key={i} className="flex items-start gap-2" data-tone={c.tone}>
                  <Icon className={cn("mt-0.5 h-3.5 w-3.5 shrink-0", className)} aria-hidden />
                  <span className="min-w-0 text-foreground/85">{c.text}</span>
                </li>
              )
            })}
          </ul>
        )}
        {typeToConfirm != null && (
          <label htmlFor={inputId} className="flex flex-col gap-1.5 text-label text-muted-foreground">
            <span>
              Type <code className="rounded bg-muted px-1 py-0.5 font-mono text-micro text-foreground/85">{typeToConfirm}</code> to confirm
            </span>
            <input
              id={inputId}
              autoComplete="off"
              spellCheck={false}
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              className="h-8 rounded-md border border-border bg-background px-2 font-mono text-sm text-foreground"
            />
          </label>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>{cancelLabel}</AlertDialogCancel>
          <AlertDialogAction
            disabled={disabled}
            aria-disabled={disabled || undefined}
            className={cn(destructive && "bg-destructive text-white hover:bg-destructive/90")}
            onClick={async (e) => {
              // Radix closes on click by default; keep it open until the
              // work is done so a failure can leave the person where they were.
              e.preventDefault()
              if (disabled) return
              setBusy(true)
              try {
                await onConfirm()
                onOpenChange(false)
              } catch {
                // The caller has already said what went wrong (a toast); the
                // dialog stays so the person is where they were.
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy ? "Working…" : confirmLabel}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
