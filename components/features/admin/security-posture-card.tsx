"use client"

import { useCallback, useEffect, useState } from "react"
import { AlertTriangle, Info, RefreshCw, ShieldAlert, ShieldCheck } from "lucide-react"
import { Button } from "@/components/ui/button"
import { SettingsCard } from "@/components/features/settings/shared"
import { apiFetch } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"

// #1379 — read-only view of the instance's env-driven security posture.
//
// These flags are deliberately not settable from the app; they are deploy
// decisions. What was missing is that their STATE was invisible, so "are we
// storing credentials in plaintext? is signup open?" required shell access to
// the box — which the person triaging an incident often doesn't have.
//
// The API returns booleans only, never a secret value, so there is nothing
// here to redact.

/** A derived risk note from the posture endpoint. The raw booleans say what is
 *  set; a warning says why it matters, which is the part that gets acted on. */
interface PostureWarning {
  key: string
  severity: "high" | "medium" | "info" | string
  message: string
}

/** Wire shape of GET /api/v1/admin/security-posture. Booleans and enum-ish
 *  state only — the endpoint never returns a secret value, so there is nothing
 *  here to redact before rendering. */
interface Posture {
  environment: string
  encryption_key_configured: boolean
  plaintext_secrets_allowed: boolean
  private_endpoints_ceiling: boolean
  signup_open: boolean
  oauth_configured: boolean
  email_configured: boolean
  rate_limit_disabled: boolean
  rate_limit_effectively_disabled: boolean
  warnings: PostureWarning[]
}

/** One state line. `bad` drives the colour, so "insecure" never renders in the
 *  same neutral tone as "fine" — the whole value of this card is the glance. */
function StateRow({ label, value, bad }: { label: string; value: string; bad?: boolean }) {
  return (
    <div className="flex items-center justify-between gap-3 py-1.5 border-b border-border/30 last:border-0">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span
        className={cn(
          "text-xs font-mono",
          bad ? "text-destructive font-medium" : "text-foreground/85",
        )}
      >
        {value}
      </span>
    </div>
  )
}

const SEVERITY_STYLE: Record<string, { icon: typeof AlertTriangle; cls: string }> = {
  high: { icon: ShieldAlert, cls: "text-destructive" },
  medium: { icon: AlertTriangle, cls: "text-amber-500" },
  info: { icon: Info, cls: "text-muted-foreground" },
}

/**
 * Read-only card showing how the instance is postured: encryption at rest, the
 * private-egress ceiling, signup policy, rate limiting, and whether email/OAuth
 * are configured.
 *
 * Read-only on purpose. These are env-driven deploy decisions and must not be
 * flippable from the app — the gap this closes is that their STATE was
 * invisible without shell access to the box, which the person triaging an
 * incident often doesn't have. Admin-gated server-side; a non-admin gets an
 * explanation rather than an empty card.
 */
export function SecurityPostureCard() {
  const [posture, setPosture] = useState<Posture | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const res = await apiFetch("/api/v1/admin/security-posture")
      if (!res.ok) {
        setError(
          res.status === 403
            ? "Requires an admin role in this workspace."
            : `Could not load the security posture (HTTP ${res.status}).`,
        )
        return
      }
      setPosture((await res.json()) as Posture)
    } catch {
      setError("Network error loading the security posture.")
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { void load() }, [load])

  // Intent and effect are separate facts: in production the limiter runs even
  // when the disable flag is set, and collapsing the two would either invent an
  // exposure or hide a misconfiguration.
  function rateLimitState(p: Posture): { value: string; bad: boolean } {
    if (p.rate_limit_effectively_disabled) return { value: "DISABLED", bad: true }
    if (p.rate_limit_disabled) return { value: "flag set — IGNORED in production", bad: false }
    return { value: "enabled", bad: false }
  }

  return (
    <SettingsCard
      title="Security posture"
      description="How this instance is configured. Env-driven and read-only — set at deploy, not here."
      actions={
        <Button
          variant="outline"
          size="sm"
          className="h-7 px-2.5 text-xs"
          onClick={() => void load()}
          disabled={loading}
        >
          <RefreshCw className={cn("mr-1.5 h-3 w-3", loading && "animate-spin")} />
          Refresh
        </Button>
      }
      padded
    >
      {error ? (
        <p className="text-xs text-muted-foreground">{error}</p>
      ) : !posture ? (
        <p className="text-xs text-muted-foreground">Loading…</p>
      ) : (
        <div className="space-y-3">
          <StateRow label="Environment" value={posture.environment || "(unset)"} />
          <div>
            <StateRow
              label="Encryption key"
              value={posture.encryption_key_configured ? "configured" : "NOT configured"}
              bad={!posture.encryption_key_configured}
            />
            <StateRow
              label="Plaintext secrets"
              value={posture.plaintext_secrets_allowed ? "ALLOWED (insecure)" : "refused"}
              bad={posture.plaintext_secrets_allowed}
            />
            <StateRow
              label="Private-egress ceiling"
              value={posture.private_endpoints_ceiling ? "open" : "closed"}
            />
            <StateRow
              label="Signup"
              value={posture.signup_open ? "OPEN" : "invite-only"}
              bad={posture.signup_open}
            />
            <StateRow label="Rate limiter" {...rateLimitState(posture)} />
            <StateRow
              label="Email (Resend)"
              value={posture.email_configured ? "configured" : "not configured"}
            />
            <StateRow
              label="OAuth (Google)"
              value={posture.oauth_configured ? "configured" : "not configured"}
            />
          </div>

          {posture.warnings.length === 0 ? (
            <p className="text-xs text-emerald-600 dark:text-emerald-400 flex items-center gap-1.5">
              <ShieldCheck className="h-3.5 w-3.5" />
              Nothing in this instance&apos;s posture stands out.
            </p>
          ) : (
            <div className="space-y-1.5 pt-1">
              {posture.warnings.map((w) => {
                const style = SEVERITY_STYLE[w.severity] ?? SEVERITY_STYLE.info
                const Icon = style.icon
                return (
                  <p key={w.key} className={cn("text-[11px] flex items-start gap-1.5", style.cls)}>
                    <Icon className="h-3.5 w-3.5 shrink-0 mt-px" />
                    <span>{w.message}</span>
                  </p>
                )
              })}
            </div>
          )}
        </div>
      )}
    </SettingsCard>
  )
}
