"use client"

import { useEffect, useState } from "react"
import { toast } from "sonner"
import { Check, X } from "lucide-react"
import Link from "next/link"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import { Badge } from "@/components/ui/badge"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { KindBadge } from "./kind-badge"
import { formatDateTime } from "@/lib/time"
import { decideApproval } from "@/hooks/use-approvals"
import type { ApprovalRow } from "@/lib/types/approvals"

interface ApprovalDetailProps {
  row: ApprovalRow | null
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Fired after a successful decide — parent does optimistic patching. */
  onDecided: (id: string, status: "approved" | "denied", comment: string) => void
}

/**
 * Right-side sheet with full approval detail — payload JSON, linked agent /
 * crew / mission, approve / deny form. Parent handles optimistic UI.
 */
export function ApprovalDetail({ row, open, onOpenChange, onDecided }: ApprovalDetailProps) {
  const [comment, setComment] = useState("")
  const [submitting, setSubmitting] = useState<null | "approved" | "denied">(null)

  // Reset the textarea when the selected approval changes so the previous
  // row's comment doesn't leak into the next decision.
  useEffect(() => {
    setComment("")
  }, [row?.id])

  async function handleDecide(status: "approved" | "denied") {
    if (!row) return
    setSubmitting(status)
    try {
      await decideApproval(row.id, status, comment)
      onDecided(row.id, status, comment)
      toast.success(`Approval ${status}`)
      setComment("")
      onOpenChange(false)
    } catch (err) {
      toast.error(`Failed to ${status === "approved" ? "approve" : "deny"}`, {
        description: err instanceof Error ? err.message : undefined,
      })
    } finally {
      setSubmitting(null)
    }
  }

  const isPending = row?.status === "pending"
  const payload = row?.payload

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="sm:max-w-md w-full">
        {row ? (
          <>
            <SheetHeader>
              <SheetTitle className="flex items-center gap-2 flex-wrap">
                <KindBadge kind={row.kind} />
                <span className="text-sm font-medium">Approval</span>
                <span className="ml-auto text-[10px] font-mono text-muted-foreground tabular-nums">
                  {row.id.slice(0, 8)}
                </span>
              </SheetTitle>
              <SheetDescription className="text-xs">
                Requested {formatDateTime(row.created_at)}
                {row.requested_by ? ` by ${row.requested_by}` : ""}
              </SheetDescription>
            </SheetHeader>

            <div className="px-4 space-y-4 overflow-y-auto flex-1 min-h-0">
              <section>
                <div className="text-[10px] uppercase tracking-wider text-muted-foreground/80 font-semibold mb-1">
                  Reason
                </div>
                <p className="text-sm text-foreground/90 whitespace-pre-wrap">
                  {row.reason || <span className="text-muted-foreground italic">(no reason)</span>}
                </p>
              </section>

              <section className="flex flex-wrap gap-2">
                {row.crew_id && (
                  <Badge asChild variant="outline" className="text-[10px] font-mono border-border/60">
                    <Link href={`/crews/${encodeURIComponent(row.crew_id)}`}>crew · {row.crew_id.slice(0, 8)}</Link>
                  </Badge>
                )}
                {row.agent_id && (
                  <Badge asChild variant="outline" className="text-[10px] font-mono border-border/60">
                    <Link href={`/crews/agents/${encodeURIComponent(row.agent_id)}`}>agent · {row.agent_id.slice(0, 8)}</Link>
                  </Badge>
                )}
                {row.mission_id && (
                  <Badge asChild variant="outline" className="text-[10px] font-mono border-border/60">
                    <Link href={`/missions/${encodeURIComponent(row.mission_id)}/timeline`}>
                      mission · {row.mission_id.slice(0, 8)}
                    </Link>
                  </Badge>
                )}
              </section>

              {payload && Object.keys(payload).length > 0 && (
                <section>
                  <div className="text-[10px] uppercase tracking-wider text-muted-foreground/80 font-semibold mb-1">
                    Payload
                  </div>
                  <pre className="max-h-64 overflow-auto rounded border border-border/50 bg-muted/30 p-2 text-[10px] font-mono text-muted-foreground">
                    {JSON.stringify(payload, null, 2)}
                  </pre>
                </section>
              )}

              {row.status !== "pending" && row.comment && (
                <section>
                  <div className="text-[10px] uppercase tracking-wider text-muted-foreground/80 font-semibold mb-1">
                    Decision comment
                  </div>
                  <p className="text-sm text-foreground/90 whitespace-pre-wrap">{row.comment}</p>
                  {row.decided_by && (
                    <p className="mt-1 text-[11px] text-muted-foreground">
                      by <span className="font-mono">{row.decided_by}</span>
                      {row.decided_at ? ` · ${formatDateTime(row.decided_at)}` : ""}
                    </p>
                  )}
                </section>
              )}

              {isPending && (
                <section className="space-y-2">
                  <div className="text-[10px] uppercase tracking-wider text-muted-foreground/80 font-semibold">
                    Comment
                  </div>
                  <Textarea
                    value={comment}
                    onChange={(e) => setComment(e.target.value)}
                    placeholder="Optional — add context for the audit trail"
                    rows={3}
                    className="text-xs"
                  />
                  <div className="flex gap-2">
                    <Button
                      size="sm"
                      className="flex-1 h-8"
                      onClick={() => handleDecide("approved")}
                      disabled={submitting !== null}
                    >
                      <Check className="h-3 w-3 mr-1.5" />
                      {submitting === "approved" ? "Approving…" : "Approve"}
                    </Button>
                    <Button
                      variant="outline"
                      size="sm"
                      className="flex-1 h-8 border-red-500/40 text-red-300 hover:bg-red-500/10"
                      onClick={() => handleDecide("denied")}
                      disabled={submitting !== null}
                    >
                      <X className="h-3 w-3 mr-1.5" />
                      {submitting === "denied" ? "Denying…" : "Deny"}
                    </Button>
                  </div>
                </section>
              )}
            </div>
          </>
        ) : null}
      </SheetContent>
    </Sheet>
  )
}
