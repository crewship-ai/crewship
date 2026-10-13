"use client"

import { useCallback, useEffect, useState } from "react"
import { Check, Copy, RefreshCw, SquareTerminal } from "lucide-react"

import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import {
  Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle,
} from "@/components/ui/dialog"

type PairStatus = "idle" | "starting" | "pending" | "consumed" | "expired" | "failed"

/**
 * The command a user pastes on the machine where their CLI lives — code AND
 * server. Without --server the CLI defaults to http://localhost:8080, which
 * only works for someone running the server on that same machine; the
 * browser already knows where the server is, so it goes into the snippet.
 * The local default is left out so a developer's snippet stays short.
 *
 * Same rule as the onboarding pairing step.
 */
export function pairCommand(code: string, origin: string): string {
  const isLocalDefault = origin === "http://localhost:8080" || origin === "http://127.0.0.1:8080"
  return `crewship login --pair --code=${code}${isLocalDefault ? "" : ` --server=${origin}`}`
}

/**
 * Connect CLI — signs a terminal in as you with a one-time device code
 * (POST /api/v1/auth/pair/start → `crewship login --pair` → poll).
 *
 * Onboarding was the only place this flow had a screen, so after setup the
 * way to sign a second laptop in was to mint a CLI token by hand and paste
 * the secret into a config file. The code is single-use and expires in ten
 * minutes; the token the CLI receives shows up under Sessions & CLI tokens,
 * where it can be revoked like any other.
 */
export function ConnectCliDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const [status, setStatus] = useState<PairStatus>("idle")
  const [code, setCode] = useState<string | null>(null)
  const [expiresAt, setExpiresAt] = useState<string | null>(null)
  const [remaining, setRemaining] = useState<number | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)

  const start = useCallback(async () => {
    setError(null)
    setStatus("starting")
    try {
      const res = await apiFetch("/api/v1/auth/pair/start", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({}),
      })
      const data = await res.json().catch(() => ({}))
      if (!res.ok || !data.code) {
        setError(data.detail ?? data.error ?? "Could not start pairing")
        setStatus("failed")
        return
      }
      setCode(data.code)
      setExpiresAt(data.expires_at ?? null)
      setStatus("pending")
    } catch {
      setError("Network error starting pairing")
      setStatus("failed")
    }
  }, [])

  // A fresh code on every open; a code from an earlier open may be spent or
  // half-way to expiry.
  useEffect(() => {
    if (!open) {
      setStatus("idle"); setCode(null); setExpiresAt(null); setError(null); setCopied(false)
      return
    }
    void start()
  }, [open, start])

  useEffect(() => {
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

  useEffect(() => {
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

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <SquareTerminal className="h-4 w-4" aria-hidden />
            Connect CLI
          </DialogTitle>
          <DialogDescription>
            Run this on the machine where the crewship CLI is installed. The code works once and expires in 10 minutes.
          </DialogDescription>
        </DialogHeader>

        {status === "consumed" ? (
          <div className="flex items-center gap-2 rounded-md border border-success/30 bg-success/10 px-3 py-3 text-sm text-success" role="status">
            <Check className="h-4 w-4" aria-hidden />
            CLI connected. Its token is listed under Sessions &amp; CLI tokens.
          </div>
        ) : status === "expired" || status === "failed" ? (
          <div className="space-y-3">
            <p className="text-sm text-muted-foreground" role="status">
              {status === "expired" ? "This code expired." : error}
            </p>
            <Button size="sm" variant="outline" className="gap-1.5" onClick={() => void start()}>
              <RefreshCw className="h-3.5 w-3.5" aria-hidden />
              Get a new code
            </Button>
          </div>
        ) : (
          <div className="space-y-3">
            <div className="flex items-center gap-2 rounded-md border bg-surface-subtle px-3 py-2.5">
              <code className="min-w-0 flex-1 break-all font-mono text-xs" data-testid="pair-command">
                {command || "Generating code…"}
              </code>
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label="Copy command"
                disabled={!command}
                onClick={() => {
                  void navigator.clipboard?.writeText(command)
                  setCopied(true)
                  setTimeout(() => setCopied(false), 1500)
                }}
              >
                {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
              </Button>
            </div>
            <p className="text-xs text-muted-foreground" aria-live="polite">
              {remaining !== null
                ? `Waiting for the CLI… code expires in ${Math.floor(remaining / 60)}:${String(remaining % 60).padStart(2, "0")}`
                : "Waiting for the CLI…"}
            </p>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}
