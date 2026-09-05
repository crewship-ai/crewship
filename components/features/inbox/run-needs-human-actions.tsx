"use client"

import { useState } from "react"
import { CheckCircle2, Hand, MessageSquare, Send, XCircle } from "lucide-react"
import type { LucideIcon } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import {
  InboxActError,
  type InboxAction,
  type InboxActionSpec,
  type InboxActReceipt,
  type InboxActResult,
  type InboxItem,
} from "@/hooks/use-inbox"

// =============================================================================
// The action row of a run_needs_human card (#2398).
//
// B6 raises the card when a run reports NEEDS_HUMAN; B15 (#2389) gave it three
// server-side actions — answer resumes the run in the session that asked,
// take_over and dismiss settle that session — each leaving a receipt on the
// issue's event log. The CLI drove all of it; the web card fell through
// KindActions' default branch to a PATCH-shaped Dismiss that never reached the
// session. This renders the card's own `actions[]` and calls the act door.
//
// What is rendered is what the server declared, no more: a card written
// before B15 lists only take_over, and that is the one button it gets. The
// vocabulary check below is the client's guard against an action id the
// endpoint would 400 on, not a second copy of the list.
// =============================================================================

/** What the page hands the card: the hook's act mutation, bound to the workspace. */
export type InboxActFn = (action: InboxAction, input?: string) => Promise<InboxActResult>

function isInboxAction(id: string): id is InboxAction {
  return id === "answer" || id === "take_over" || id === "dismiss"
}

const ICON: Record<InboxAction, LucideIcon> = {
  answer: MessageSquare,
  take_over: Hand,
  dismiss: XCircle,
}

/** The receipt the server merged into the card on resolve, when it is one. */
export function receiptOf(item: InboxItem): InboxActReceipt | null {
  const r = item.payload?.receipt
  if (!r || typeof r !== "object" || Array.isArray(r)) return null
  const rec = r as Partial<InboxActReceipt>
  if (typeof rec.action !== "string") return null
  return rec as InboxActReceipt
}

function headlineOf(action: string): string {
  switch (action) {
    case "answer": return "Answered — delivered to the agent's session"
    case "take_over": return "Taken over — the agent's session is idle"
    case "dismiss": return "Dismissed — the agent's session is idle"
  }
  return `Acted: ${action}`
}

/** The one-line toast after a successful act — names the run and the log position. */
function successCopy(result: InboxActResult): string {
  const r = result.receipt
  const at = r.seq != null ? ` (event #${r.seq})` : ""
  if (result.action === "answer") {
    if (r.dispatch_state === "queued") {
      return `Answer queued behind the current run${r.run_id ? ` — run ${r.run_id}` : ""}${at}`
    }
    return `Answer delivered — run ${r.run_id ?? r.source_run_id} resumes${at}`
  }
  if (result.action === "take_over") return `Taken over — the agent's session is idle${at}`
  return `Dismissed — the agent's session is idle${at}`
}

/**
 * The receipt, rendered where the buttons were: the run it resumed, where on
 * the issue's event log it landed, which agent version answered. Shown right
 * after acting and again whenever the resolved card is opened from History —
 * the same field the server merged into payload.receipt.
 */
export function ActReceipt({ receipt }: { receipt: InboxActReceipt }) {
  const parts: string[] = []
  if (receipt.run_id) parts.push(`run ${receipt.run_id}`)
  if (receipt.dispatch_state) parts.push(receipt.dispatch_state)
  if (receipt.delivery_id) parts.push(`delivery ${receipt.delivery_id}`)
  if (receipt.seq != null) parts.push(`event #${receipt.seq}`)
  if (receipt.agent_version != null) parts.push(`agent v${receipt.agent_version}`)
  return (
    <div
      data-testid="act-receipt"
      className="flex flex-col gap-1 rounded-md border border-success/30 bg-success/[.06] px-3 py-2"
    >
      <span className="type-row flex items-center gap-1.5 font-medium text-success">
        <CheckCircle2 className="h-3.5 w-3.5 shrink-0" />
        {headlineOf(receipt.action)}
      </span>
      {parts.length > 0 && (
        <span className="type-meta font-mono text-muted-foreground">{parts.join(" · ")}</span>
      )}
    </div>
  )
}

export function RunNeedsHumanActions({
  item,
  onAct,
  onRefresh,
  disabled,
}: {
  item: InboxItem
  /** Absent on a surface that has no act door; the buttons then say so. */
  onAct?: InboxActFn
  onRefresh: (action?: string) => void | Promise<void>
  disabled: boolean
}) {
  const [busy, setBusy] = useState<InboxAction | null>(null)
  const [answering, setAnswering] = useState(false)
  const [input, setInput] = useState("")
  // Held locally as well as read off the item: the act response is the
  // first place the receipt exists, and the card must flip on it without
  // waiting for a refetch to write it back into payload.
  const [receipt, setReceipt] = useState<InboxActReceipt | null>(null)

  const shown = receipt ?? (item.state === "resolved" ? receiptOf(item) : null)
  if (shown) return <ActReceipt receipt={shown} />
  // Resolved without a receipt: archived, or acted on before receipts
  // existed. The pane's footer states the outcome; nothing to add here.
  if (item.state === "resolved") return null

  const actions = (item.actions ?? []).filter((a): a is InboxActionSpec & { id: InboxAction } => isInboxAction(a.id))
  if (actions.length === 0) {
    return (
      <span className="type-meta text-muted-foreground">
        This card carries no action the server can perform from here — open the issue to continue.
      </span>
    )
  }

  const perform = async (spec: InboxActionSpec & { id: InboxAction }, text?: string) => {
    if (!onAct) return
    if (spec.irreversible) {
      const effect = spec.effect ? ` ${spec.effect}.` : ""
      if (!window.confirm(`${spec.label}?${effect} This cannot be undone.`)) return
    }
    setBusy(spec.id)
    try {
      const result = await onAct(spec.id, text)
      setReceipt(result.receipt)
      setAnswering(false)
      setInput("")
      toast.success(successCopy(result))
    } catch (e) {
      if (e instanceof InboxActError) {
        if (e.code === "already_acted" || e.code === "concurrent") {
          // Somebody else finished first. The hook has already asked the
          // server for its version of the card; the pane moves on to it.
          toast.info(
            e.code === "concurrent"
              ? "Someone acted on this at the same time — showing their decision"
              : `Already acted on elsewhere${e.resolvedAction ? ` (${e.resolvedAction})` : ""} — refreshing`,
          )
          await onRefresh("resolved")
          return
        }
        if (e.code === "undeliverable") {
          // The answer is on the issue as a comment; nothing will pick it
          // up. The card stays open so the person can try again once the
          // cause (a held agent, an unconnected crew) is fixed.
          toast.error(
            `Your answer is on the issue as a comment, but it could not be delivered: ${e.detail ?? e.dispatchState ?? e.message}`,
          )
          return
        }
        toast.error(e.message)
        return
      }
      toast.error(e instanceof Error ? `Could not act on this card: ${e.message}` : "Could not act on this card")
    } finally {
      setBusy(null)
    }
  }

  const locked = disabled || busy !== null || !onAct
  const trimmed = input.trim()

  return (
    <div className="flex flex-col gap-2" data-testid="needs-human-actions">
      <div className="flex flex-wrap items-center gap-2">
        {actions.map((a) => {
          const Icon = ICON[a.id]
          const primary = a.id === "answer"
          return (
            <Button
              key={a.id}
              size="sm"
              variant={primary ? "default" : "outline"}
              disabled={locked}
              // The server's own sentence about what the button does — the
              // effect is its contract, and the tooltip is where a person
              // reads it before clicking.
              title={onAct ? a.effect : "Act on this card from the inbox page"}
              aria-pressed={a.id === "answer" ? answering : undefined}
              onClick={() => {
                if (a.id === "answer") {
                  setAnswering((v) => !v)
                  return
                }
                void perform(a)
              }}
              className={primary ? "gap-1.5 bg-success/20 text-success hover:bg-success/30" : "gap-1.5"}
            >
              <Icon className="h-3.5 w-3.5" />
              {busy === a.id ? `${a.label}…` : a.label}
            </Button>
          )
        })}
      </div>

      {answering && (
        <form
          className="flex flex-col gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            const spec = actions.find((a) => a.id === "answer")
            if (spec && trimmed !== "") void perform(spec, trimmed)
          }}
        >
          <Textarea
            aria-label="Your answer"
            placeholder="What the agent needs to continue…"
            value={input}
            onChange={(e) => setInput(e.target.value)}
            disabled={locked}
            rows={3}
            autoFocus
          />
          <div className="flex items-center gap-2">
            <Button type="submit" size="sm" disabled={locked || trimmed === ""} className="gap-1.5">
              <Send className="h-3.5 w-3.5" />
              {busy === "answer" ? "Sending…" : "Send"}
            </Button>
            <Button type="button" size="sm" variant="ghost" disabled={busy !== null} onClick={() => { setAnswering(false); setInput("") }}>
              Cancel
            </Button>
            <span className="type-meta text-muted-foreground-soft">
              {actions.find((a) => a.id === "answer")?.effect ??
                "Posted as your comment on the issue and delivered to the session that asked; the run resumes from its checkpoint."}
            </span>
          </div>
        </form>
      )}
    </div>
  )
}
