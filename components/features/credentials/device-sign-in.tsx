"use client"

/**
 * Sign in with a code — the device-code login of PRD provider-logins §5.6
 * (v2) and §10.3.
 *
 * Crewship runs the RFC 8628 flow itself: it asks the server for a code, the
 * server asks the provider, the person opens the verification page on any
 * device and types the code, and the server polls the provider until the
 * login exists — as a credential, parsed into parts, refresh token sealed.
 * The browser never sees a token. What it does see is the code, large and
 * copyable, and the state machine: pending → complete | expired | denied.
 *
 * Polling uses the interval the server names (`interval_s`) — the provider
 * rate-limits the token endpoint and a client that polls faster is told to
 * slow down, which the server would then have to relay. The floor is a prop so
 * a test can run the whole flow in milliseconds with real timers.
 */

import * as React from "react"
import { Check, Copy, ExternalLink, RefreshCw } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { apiFetch } from "@/lib/api-fetch"
import { getBrand } from "@/lib/credential-providers/registry"
import type { ProviderLoginMode } from "@/lib/credentials/item-types"
import { cn } from "@/lib/utils"

/** POST /provider-logins/device — contract §10.3. */
export interface DeviceStart {
  device_id: string
  user_code: string
  verification_url: string
  expires_at: string
  interval_s: number
}

export type DeviceStatus = "pending" | "complete" | "expired" | "denied"

export interface DeviceSignInProps {
  workspaceId: string
  provider: string
  mode: ProviderLoginMode
  /** The login exists on the server — its credential id. */
  onComplete: (credentialId: string) => void
  /** Reported up so the wizard can hold the step while a code is live. */
  onStateChange?: (state: DeviceStatus | "starting" | "error") => void
  /** Overrides the server's `interval_s` — tests pass a few milliseconds.
   *  Absent, the period is the server's, floored at one second. */
  pollIntervalMs?: number
}

type Phase =
  | { kind: "starting" }
  | { kind: "error"; message: string }
  | { kind: "pending"; start: DeviceStart }
  | { kind: "complete"; start: DeviceStart; credentialId: string }
  | { kind: "expired"; start: DeviceStart }
  | { kind: "denied"; start: DeviceStart }

export function DeviceSignIn({ workspaceId, provider, mode, onComplete, onStateChange, pollIntervalMs }: DeviceSignInProps) {
  const [phase, setPhase] = React.useState<Phase>({ kind: "starting" })
  const [attempt, setAttempt] = React.useState(0)
  const [copied, setCopied] = React.useState(false)
  const onCompleteRef = React.useRef(onComplete)
  onCompleteRef.current = onComplete
  const onStateRef = React.useRef(onStateChange)
  onStateRef.current = onStateChange

  const brand = getBrand(provider)
  const ws = encodeURIComponent(workspaceId)

  React.useEffect(() => {
    onStateRef.current?.(phase.kind)
  }, [phase.kind])

  // One flow per attempt. Everything the effect starts — the request, the
  // poll timer — is torn down when the attempt changes or the component
  // leaves, so a code you walked away from is never polled under a new one.
  React.useEffect(() => {
    let cancelled = false
    let timer: ReturnType<typeof setTimeout> | null = null
    setPhase({ kind: "starting" })
    setCopied(false)
    const period = (start: DeviceStart) => pollIntervalMs ?? Math.max(1000, (start.interval_s || 5) * 1000)

    const poll = async (start: DeviceStart) => {
      if (cancelled) return
      try {
        const r = await apiFetch(`/api/v1/provider-logins/device/${encodeURIComponent(start.device_id)}?workspace_id=${ws}`)
        if (cancelled) return
        if (!r.ok) {
          setPhase({ kind: "error", message: `Couldn't check the sign-in (HTTP ${r.status}).` })
          return
        }
        const data = (await r.json().catch(() => ({}))) as { status?: DeviceStatus; credential_id?: string }
        if (cancelled) return
        if (data.status === "complete") {
          if (!data.credential_id) {
            setPhase({ kind: "error", message: "The sign-in finished but the server named no credential." })
            return
          }
          setPhase({ kind: "complete", start, credentialId: data.credential_id })
          onCompleteRef.current(data.credential_id)
          return
        }
        if (data.status === "expired") {
          setPhase({ kind: "expired", start })
          return
        }
        if (data.status === "denied") {
          setPhase({ kind: "denied", start })
          return
        }
        // pending, or an unknown word — keep asking until the code expires.
        if (new Date(start.expires_at).getTime() <= Date.now()) {
          setPhase({ kind: "expired", start })
          return
        }
        timer = setTimeout(() => void poll(start), period(start))
      } catch {
        if (!cancelled) setPhase({ kind: "error", message: "Network error while checking the sign-in." })
      }
    }

    apiFetch(`/api/v1/provider-logins/device?workspace_id=${ws}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ provider, mode }),
    })
      .then(async (r) => {
        if (cancelled) return
        if (!r.ok) {
          const data = await r.json().catch(() => ({}))
          setPhase({
            kind: "error",
            message: typeof data.error === "string" ? data.error : `Couldn't start the sign-in (HTTP ${r.status}).`,
          })
          return
        }
        const start = (await r.json()) as DeviceStart
        if (cancelled) return
        if (!start?.device_id || !start.user_code) {
          setPhase({ kind: "error", message: "The server answered without a code." })
          return
        }
        setPhase({ kind: "pending", start })
        timer = setTimeout(() => void poll(start), period(start))
      })
      .catch(() => {
        if (!cancelled) setPhase({ kind: "error", message: "Network error while starting the sign-in." })
      })

    return () => {
      cancelled = true
      if (timer) clearTimeout(timer)
    }
  }, [attempt, provider, mode, ws, pollIntervalMs])

  const start = "start" in phase ? phase.start : null

  async function copy() {
    if (!start) return
    try {
      await navigator.clipboard.writeText(start.user_code)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      // No clipboard (insecure context, a test) — the code is on screen and selectable.
    }
  }

  return (
    <div data-testid="device-sign-in" data-phase={phase.kind} className="space-y-3">
      {phase.kind === "starting" && (
        <p className="flex items-center gap-2 type-meta text-muted-foreground">
          <Spinner className="h-3.5 w-3.5" />
          Asking {brand.label} for a code…
        </p>
      )}

      {phase.kind === "error" && (
        <div className="space-y-2">
          <p className="type-meta text-destructive" role="alert">{phase.message}</p>
          <Button size="sm" variant="outline" onClick={() => setAttempt((n) => n + 1)}>
            <RefreshCw className="mr-1.5 h-3 w-3" />
            Try again
          </Button>
        </div>
      )}

      {start && phase.kind !== "error" && (
        <div className="rounded-lg border border-border/60 bg-background p-3">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="min-w-0">
              <p className="type-meta text-muted-foreground">Your one-time code</p>
              <p
                className="mt-1 select-all font-mono text-2xl font-semibold tracking-[0.2em] text-foreground"
                aria-label="One-time code"
                data-testid="device-user-code"
              >
                {start.user_code}
              </p>
            </div>
            <div className="flex shrink-0 items-center gap-1.5">
              <Button size="sm" variant="outline" onClick={copy} aria-label="Copy the code">
                {copied ? <Check className="mr-1.5 h-3 w-3 text-success" /> : <Copy className="mr-1.5 h-3 w-3" />}
                {copied ? "Copied" : "Copy"}
              </Button>
              <Button size="sm" asChild>
                <a href={start.verification_url} target="_blank" rel="noreferrer noopener">
                  <ExternalLink className="mr-1.5 h-3 w-3" />
                  Open {hostOf(start.verification_url)}
                </a>
              </Button>
            </div>
          </div>

          <p className="mt-3 type-meta leading-relaxed text-muted-foreground">
            Open{" "}
            <a href={start.verification_url} target="_blank" rel="noreferrer noopener" className="font-mono text-foreground/85 underline underline-offset-2">
              {start.verification_url}
            </a>{" "}
            on any device, enter the code and approve. Crewship is waiting for {brand.label}; nothing to paste back.
          </p>

          <div className={cn("mt-3 flex items-center gap-2 type-meta", toneFor(phase.kind))}>
            {phase.kind === "pending" && (
              <>
                <Spinner className="h-3.5 w-3.5" />
                <span>Waiting for you to approve… the code expires {expiresIn(start.expires_at)}.</span>
              </>
            )}
            {phase.kind === "complete" && (
              <>
                <Check className="h-3.5 w-3.5" />
                <span>Signed in. The login is saved with its refresh token sealed — continue to say who pays with it.</span>
              </>
            )}
            {phase.kind === "expired" && (
              <>
                <span>The code expired before it was used.</span>
                <Button size="sm" variant="outline" className="ml-auto h-7" onClick={() => setAttempt((n) => n + 1)}>
                  <RefreshCw className="mr-1.5 h-3 w-3" />
                  Get a new code
                </Button>
              </>
            )}
            {phase.kind === "denied" && (
              <>
                <span>{brand.label} reported the sign-in was denied.</span>
                <Button size="sm" variant="outline" className="ml-auto h-7" onClick={() => setAttempt((n) => n + 1)}>
                  <RefreshCw className="mr-1.5 h-3 w-3" />
                  Try again
                </Button>
              </>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

function toneFor(kind: Phase["kind"]): string {
  switch (kind) {
    case "complete":
      return "text-success"
    case "expired":
    case "denied":
      return "text-warn"
    default:
      return "text-muted-foreground"
  }
}

function hostOf(url: string): string {
  try {
    return new URL(url).host
  } catch {
    return url
  }
}

function expiresIn(iso: string): string {
  const diff = new Date(iso).getTime() - Date.now()
  if (!Number.isFinite(diff) || diff <= 0) return "now"
  const mins = Math.round(diff / 60_000)
  return mins < 1 ? "in under a minute" : `in ${mins} min`
}
