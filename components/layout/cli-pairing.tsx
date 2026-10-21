"use client"

import * as React from "react"
import Link from "next/link"
import { AlertTriangle, Check, Copy, RefreshCw } from "lucide-react"

import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import { Note } from "@/components/ui/note"
import { PageBanner } from "@/components/layout/app-banners"
import { cn } from "@/lib/utils"

/**
 * CLI pairing (Harbor 2 kit, COM-09; chrome-states.md G15, onboarding O04):
 * signs a terminal in with a one-time device code
 * (POST /api/v1/auth/pair/start → `crewship login --pair` → poll). One
 * component for Connect CLI and the first-run setup.
 *
 *   usePairing(active)  the requests: a fresh code each time it becomes
 *                       active, the 2 s poll, the countdown to expiry
 *   <PairingBlock>      the command to paste, then one line per state:
 *                       pending (countdown), paired, expired (new code),
 *                       failed (Retry)
 *
 * The setup keeps its own requests (it guards against minting two codes
 * across its steps) and uses PairingBlock alone.
 */

export type PairStatus = "idle" | "starting" | "pending" | "consumed" | "expired" | "failed"

/** Where a paired CLI's token is listed. */
export const SESSIONS_HREF = "/settings?tab=sessions"

/**
 * The command a user pastes on the machine where their CLI lives — code AND
 * server. Without --server the CLI defaults to http://localhost:8080, which
 * only works for someone running the server on that same machine; the
 * browser already knows where the server is, so it goes into the snippet.
 * The local default is left out so a developer's snippet stays short.
 */
export function pairCommand(code: string, origin: string): string {
  const isLocalDefault = origin === "http://localhost:8080" || origin === "http://127.0.0.1:8080"
  return `crewship login --pair --code=${code}${isLocalDefault ? "" : ` --server=${origin}`}`
}

/** "m:ss" for the code's remaining life. */
export function formatCountdown(sec: number): string {
  const m = Math.floor(sec / 60)
  const s = sec % 60
  return `${m}:${s.toString().padStart(2, "0")}`
}

/** The pairing requests and their state. A fresh code each time `active` turns true. */
export function usePairing(active: boolean) {
  const [status, setStatus] = React.useState<PairStatus>("idle")
  const [code, setCode] = React.useState<string | null>(null)
  const [expiresAt, setExpiresAt] = React.useState<string | null>(null)
  const [remainingSec, setRemaining] = React.useState<number | null>(null)
  const [error, setError] = React.useState<string | null>(null)
  const [copied, setCopied] = React.useState(false)

  const start = React.useCallback(async () => {
    setError(null)
    setCode(null)
    setExpiresAt(null)
    setStatus("starting")
    try {
      const res = await apiFetch("/api/v1/auth/pair/start", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({}),
      })
      const data = await res.json().catch(() => ({}))
      if (!res.ok || !data.code) {
        setError(data.detail ?? data.error ?? "Couldn’t start pairing. Check your connection.")
        setStatus("failed")
        return
      }
      setCode(data.code)
      setExpiresAt(data.expires_at ?? null)
      setStatus("pending")
    } catch {
      setError("Couldn’t start pairing. Check your connection.")
      setStatus("failed")
    }
  }, [])

  // A fresh code on every open; a code from an earlier open may be spent or
  // half-way to expiry.
  React.useEffect(() => {
    if (!active) {
      setStatus("idle"); setCode(null); setExpiresAt(null); setError(null); setCopied(false)
      return
    }
    void start()
  }, [active, start])

  React.useEffect(() => {
    if (status !== "pending" || !code) return
    const t = setInterval(async () => {
      try {
        const res = await apiFetch(`/api/v1/auth/pair/poll?code=${encodeURIComponent(code)}`)
        if (!res.ok) return
        const data = await res.json()
        if (data.status === "consumed") setStatus("consumed")
        else if (data.status === "expired") setStatus("expired")
      } catch { /* network blip — keep polling */ }
    }, 2000)
    return () => clearInterval(t)
  }, [status, code])

  React.useEffect(() => {
    if (status !== "pending" || !expiresAt) { setRemaining(null); return }
    const tick = () => {
      const left = Math.max(0, Math.round((new Date(expiresAt).getTime() - Date.now()) / 1000))
      setRemaining(left)
      if (left === 0) setStatus("expired")
    }
    tick()
    const t = setInterval(tick, 1000)
    return () => clearInterval(t)
  }, [status, expiresAt])

  const command = code && typeof window !== "undefined" ? pairCommand(code, window.location.origin) : ""
  const copy = React.useCallback(() => {
    if (!command) return
    void navigator.clipboard?.writeText(command)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }, [command])

  return { status, command, remainingSec, error, copied, copy, newCode: start, retry: start }
}

/** The command and one line for each state of the code. Draws only; state comes from the caller. */
export function PairingBlock({
  status,
  command,
  remainingSec,
  copied = false,
  error,
  onCopy,
  onNewCode,
  onRetry,
  paired,
  className,
}: {
  status: PairStatus
  command: string
  remainingSec: number | null
  copied?: boolean
  /** Why the start failed (failed state). */
  error?: string | null
  onCopy: () => void
  onNewCode: () => void
  onRetry: () => void
  /** The paired line's words; the setup says whether the model token came too. */
  paired?: React.ReactNode
  className?: string
}) {
  if (status === "failed") {
    return (
      <div className={className} data-slot="cli-pairing" data-state="failed">
        <PageBanner
          tone="bad"
          icon={AlertTriangle}
          action={<Button type="button" variant="outline" size="sm" className="page-banner-action" onClick={onRetry}>Retry</Button>}
        >
          {error || "Couldn’t start pairing. Check your connection."}
        </PageBanner>
      </div>
    )
  }

  return (
    <div className={cn("flex flex-col gap-2", className)} data-slot="cli-pairing" data-state={status}>
      {command ? (
        <div className="flex min-w-0 items-center gap-2 rounded-md border border-border bg-surface-subtle py-1.5 pl-3 pr-1.5">
          <code className="min-w-0 flex-1 select-all break-all font-mono text-label text-foreground" data-testid="pair-command">
            {command}
          </code>
          <Button type="button" variant="outline" size="sm" onClick={onCopy} aria-label="Copy command" disabled={status !== "pending"}>
            {copied ? <Check className="size-3.5 text-success" aria-hidden /> : <Copy className="size-3.5" aria-hidden />}
            {copied ? "Copied" : "Copy"}
          </Button>
        </div>
      ) : (
        <Note role="status">Generating a code…</Note>
      )}

      {command && status === "pending" && (
        <Note aria-live="polite">
          Waiting for the CLI…
          {remainingSec !== null && (
            <>
              {" "}code expires in{" "}
              <span className={cn("font-mono tabular-nums", remainingSec < 60 && "text-chip-warn-fg")}>
                {formatCountdown(remainingSec)}
              </span>
            </>
          )}
        </Note>
      )}

      {status === "consumed" && (
        <Note icon={Check} role="status">
          {paired ?? (
            <>
              <strong className="font-semibold text-foreground">CLI connected.</strong> Its token is listed in Sessions &amp; CLI tokens.{" "}
              <Link href={SESSIONS_HREF} className="font-medium text-primary-hover underline-offset-2 hover:underline">
                Open Sessions
              </Link>
            </>
          )}
        </Note>
      )}

      {status === "expired" && (
        <Note tone="warn">
          <span className="flex flex-wrap items-center justify-between gap-2">
            <span>The code expired.</span>
            <Button type="button" variant="outline" size="sm" onClick={onNewCode}>
              <RefreshCw className="size-3.5" aria-hidden />
              New code
            </Button>
          </span>
        </Note>
      )}
    </div>
  )
}
