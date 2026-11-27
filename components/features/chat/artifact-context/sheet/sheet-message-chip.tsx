"use client"

import { useEffect, useId } from "react"
import { FileSpreadsheet, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { HintTooltip } from "@/components/ui/hint-tooltip"
import { usePage } from "@/hooks/use-pages"
import { artifactsItemHref } from "../artifacts-href"

function formatSaved(iso: string): string {
  const at = new Date(iso)
  return Number.isNaN(at.getTime()) ? "" : at.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })
}

/**
 * The Sheet open beside the chat, above the composer, with the choice to let the
 * agent change it in the next message. The choice is separate from the Sheet
 * being shown, and what it means is always visible, before anything is sent.
 */
export function SheetMessageChip({ workspaceId, pageId, allowed, blocked, warning, notice = null, seen = null, sending, checking, refusal, onAllowedChange, onRemove, standing = false }: {
  /** The choice holds for every message in this chat (the server's default for a Sheet uploaded in the person's own chat). */
  standing?: boolean
  workspaceId: string
  pageId: string
  allowed: boolean
  /** Why the choice cannot be made now, in words. */
  blocked: string | null
  /** What will happen that the person may not expect, in words; the message still goes. */
  warning: string | null
  /** What the person needs to know whether or not the choice is on, such as a choice turned off because the Sheet changed. */
  notice?: string | null
  /** Without an editor open: the version the leave is for, as the server named it, shown with a way to look at it first. */
  seen?: { version: string; savedAt?: string; savedBy?: string } | null
  sending: boolean
  /** The server has not answered yet whether the agent may change this Sheet. */
  checking: boolean
  /** The server's reason the agent may not change this Sheet; the choice is then not offered. */
  refusal: string | null
  onAllowedChange: (allowed: boolean) => void
  onRemove: () => void
}) {
  const id = useId()
  const { page, notFound } = usePage(workspaceId, `@${pageId}`)
  const name = page?.name
  const notPrivate = refusal !== null
  const on = allowed && !blocked && !notPrivate && !checking
  // A choice made before the server answered never outlives that answer.
  useEffect(() => { if (notPrivate && allowed) onAllowedChange(false) }, [notPrivate, allowed]) // eslint-disable-line react-hooks/exhaustive-deps -- only when the answer arrives
  const explanation = refusal ?? blocked ?? (checking ? "Checking whether the agent may change this Sheet…" : notFound ? "This Sheet is not available to you."
    : on ? (standing ? "The agent may change this Sheet in this chat until you turn this off." : "The agent may change this Sheet in this message only.")
    : "The agent does not get this Sheet with the message.")
  return <div role="group" aria-label="Sheet for this message" data-testid="sheet-message-chip"
    className="mx-auto flex w-full max-w-3xl flex-wrap items-center gap-x-3 gap-y-1 px-4 py-1 text-xs">
    <span className="flex min-w-0 flex-1 basis-40 items-center gap-2">
      <FileSpreadsheet className="size-4 shrink-0 text-sheet" aria-hidden />
      <span className="truncate">{notFound ? "Not available" : name ?? "Sheet"}</span>
    </span>
    {/* The hint opens on hover over the switch and its label, and when the switch has focus. */}
    {!notPrivate && <HintTooltip hint="The agent may change this workbook in this message. Every change can be undone."><span className="flex items-center gap-2">
      <Switch id={`${id}-switch`} size="sm" checked={on} disabled={Boolean(blocked) || notFound || sending || checking} aria-describedby={`${id}-about`} onCheckedChange={onAllowedChange} />
      <label htmlFor={`${id}-switch`} className="coarse:py-3">Allow agent edits</label>
    </span></HintTooltip>}
    <Button type="button" variant="ghost" size="icon" className="size-7 coarse:size-[44px]" onClick={onRemove}
      aria-label={`Remove ${name ?? "Sheet"} from this message`}><X className="size-4" aria-hidden /></Button>
    {seen && <p className="flex basis-full flex-wrap items-center gap-x-2 text-muted-foreground">
      <span>Version {seen.version}{seen.savedAt ? `, saved ${formatSaved(seen.savedAt)}` : ""}{seen.savedBy ? ` by ${seen.savedBy}` : ""}</span>
      <a className="underline underline-offset-2 coarse:py-3" href={artifactsItemHref(workspaceId, pageId)} target="_blank" rel="noopener noreferrer">View it first</a>
    </p>}
    {/* What the choice means and what to expect read as one line, so they are not two unrelated notes. */}
    <p id={`${id}-about`} className="basis-full text-muted-foreground"><span>{explanation}</span>{" "}<span aria-live="polite">{(on ? warning : notice) ?? ""}</span></p>
  </div>
}
