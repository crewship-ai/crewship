"use client"

/**
 * One treatment for "a scope of this surface could not be fetched".
 *
 * The chat surface reads several lists that can each fail on their own: the
 * per-agent thread lists behind the tree, and the crew scope in the right
 * rail's Files tab. Both used to write the same defect —
 *
 *   .then((r) => (r.ok ? r.json() : []))
 *
 * — which turns a 500 into an empty array, and an empty array renders as a
 * sentence the UI has no standing to say: "this agent has no conversations",
 * "no shared crew files". Someone whose server is briefly unhappy is told
 * their history is gone.
 *
 * So both go through here instead, and neither can drift from the other: a
 * scope that failed says it failed, names the failure, and offers a retry.
 *
 * `ScopeFailure` is deliberately small — it renders INSIDE a list, in a 280px
 * column or a 380px drawer, not as a full-page error. It uses the existing
 * vocabulary (Button, the destructive token, type-meta) and adds no new
 * spacing or colour.
 */

import { useCallback, useState } from "react"
import { AlertTriangle, RefreshCw } from "lucide-react"

import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

/** Message for a failed response, carrying the fact that separates 403 from 502. */
export function httpError(status: number): Error {
  return new Error(`HTTP ${status}`)
}

/** Human-readable form of whatever a fetch threw. */
export function scopeErrorMessage(err: unknown): string {
  if (err instanceof Error) return err.message
  return String(err)
}

/**
 * A retry token. Bumping it is what re-runs an effect that has a `nonce` in
 * its dependency list — the smallest thing that turns "this failed" into
 * "this failed, and you can ask again".
 */
export function useRetry(): { nonce: number; retry: () => void } {
  const [nonce, setNonce] = useState(0)
  const retry = useCallback(() => setNonce((n) => n + 1), [])
  return { nonce, retry }
}

export function ScopeFailure({
  label,
  detail,
  onRetry,
  className,
  "data-testid": testId,
}: {
  /** What could not be loaded, in the reader's words. */
  label: string
  /** The status or message, verbatim. Without it every retry is a guess. */
  detail: string
  /** Omitted when the caller has no way to re-ask — then no button is drawn,
   *  rather than a Retry that does nothing. */
  onRetry?: () => void
  className?: string
  "data-testid"?: string
}) {
  return (
    <div
      data-testid={testId}
      role="alert"
      className={cn("flex flex-col gap-1.5 px-3 py-2", className)}
    >
      <span className="flex items-start gap-1.5 text-xs text-foreground/80">
        <AlertTriangle className="mt-px h-3.5 w-3.5 shrink-0 text-destructive" aria-hidden />
        <span className="min-w-0">
          {label}
          <span className="block text-[10px] leading-relaxed text-muted-foreground">{detail}</span>
        </span>
      </span>
      {onRetry && (
        <Button variant="outline" size="xs" className="self-start" onClick={onRetry}>
          <RefreshCw className="h-3 w-3" aria-hidden />
          Retry
        </Button>
      )}
    </div>
  )
}
