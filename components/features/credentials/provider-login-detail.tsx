"use client"

/**
 * The sections a credential page grows when the row carries a `login` (PRD
 * provider-logins §6.1, wireframe LoginDetail): the seat, its quota, its
 * validity and refresh, and how it reaches the agent. Everything reads the
 * §10.1 object; the only write here is "Refresh now", which is
 * `crewship credential refresh` behind a button.
 *
 * Kept out of credential-detail-sheet.tsx so that file, already the longest
 * in the folder, does not become the place where two kinds of credential are
 * interleaved. The sheet decides WHERE these cards go; this file decides what
 * they say.
 */

import * as React from "react"
import {
  CreditCard, FileText, FlaskConical, Gauge, LogIn, RefreshCw, ShieldOff, Trash2, UserPlus, Wallet,
} from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Appear, DetailCard, Pill } from "@/components/ui/detail"
import { Spinner } from "@/components/ui/spinner"
import { apiFetch } from "@/lib/api-fetch"
import { formatDate, formatRelativeTime, relTime } from "@/lib/time"
import {
  deriveLoginStatus,
  formatClock,
  formatExpiresIn,
  modeLabel,
  planLabel,
  type LoginCredential,
  type ProviderLogin,
} from "@/lib/credentials/provider-logins"
import { getBrand } from "@/lib/credential-providers/registry"
import { LoginStatusPill, QuotaBar } from "./provider-login-bits"

export interface ProviderLoginActionsProps {
  credential: LoginCredential & { login: ProviderLogin }
  /** The server says it can probe this (provider, type). Hidden for a subscription regardless. */
  testable: boolean
  testing: boolean
  canUpdate: boolean
  canBind: boolean
  canDelete: boolean
  onAssign: () => void
  onTest: () => void
  onRelogin?: () => void
  onRevoke: () => void
}

/** Assign · Test · Re-login · Revoke — the header's action row. */
export function ProviderLoginActions({
  credential, testable, testing, canUpdate, canBind, canDelete, onAssign, onTest, onRelogin, onRevoke,
}: ProviderLoginActionsProps) {
  const { login } = credential
  // A subscription cannot be probed: its traffic is a CONNECT tunnel the
  // sidecar cannot see into, and a "Test" that always passes is a placebo.
  const showTest = testable && canUpdate && login.mode !== "subscription"
  return (
    <div className="flex flex-wrap items-center gap-1.5" data-testid="login-actions">
      {canBind && (
        <Button size="sm" onClick={onAssign}>
          <UserPlus className="mr-1.5 h-3 w-3" />
          Assign to agent
        </Button>
      )}
      {showTest && (
        <Button size="sm" variant="outline" onClick={onTest} disabled={testing}>
          {testing ? <Spinner className="mr-1.5 h-3 w-3" /> : <FlaskConical className="mr-1.5 h-3 w-3" />}
          Test
        </Button>
      )}
      {canUpdate && onRelogin && (
        <Button size="sm" variant="outline" onClick={onRelogin}>
          <LogIn className="mr-1.5 h-3 w-3" />
          Re-login
        </Button>
      )}
      {canDelete && (
        <Button
          size="sm"
          variant="outline"
          onClick={onRevoke}
          className="border-destructive/30 text-destructive hover:bg-destructive/[0.05]"
        >
          <Trash2 className="mr-1.5 h-3 w-3" />
          Revoke
        </Button>
      )}
    </div>
  )
}

export interface ProviderLoginCardsProps {
  workspaceId: string
  credential: LoginCredential & { login: ProviderLogin }
  /** `account_id` from the credential's non-secret fields, when the server stored one. */
  accountId?: string | null
  canUpdate: boolean
  /** The `login` a refresh returned — the sheet keeps it so the pill follows. */
  onLoginChange: (login: ProviderLogin) => void
  /** Starts from the Appear order the sheet is at. */
  appearFrom?: number
}

export function ProviderLoginCards({ workspaceId, credential, accountId, canUpdate, onLoginChange, appearFrom = 2 }: ProviderLoginCardsProps) {
  const { login } = credential
  const [refreshing, setRefreshing] = React.useState(false)
  const brand = getBrand(login.provider)
  const status = deriveLoginStatus(credential)
  const expires = formatExpiresIn(login.expires_at)
  const atLimit = status === "at_limit"

  async function refreshNow() {
    setRefreshing(true)
    try {
      const res = await apiFetch(
        `/api/v1/credentials/${encodeURIComponent(credential.id)}/refresh?workspace_id=${encodeURIComponent(workspaceId)}`,
        { method: "POST" },
      )
      if (res.status === 409) {
        toast.info("A refresh is already running for this seat — one at a time.")
        return
      }
      const data = (await res.json().catch(() => ({}))) as { login?: ProviderLogin; error?: string }
      if (!res.ok) {
        toast.error(typeof data.error === "string" ? data.error : `Couldn't refresh the seat (HTTP ${res.status}).`)
        return
      }
      if (data.login) onLoginChange(data.login)
      toast.success(`${credential.name} refreshed`)
    } catch {
      toast.error("Network error while refreshing the seat.")
    } finally {
      setRefreshing(false)
    }
  }

  return (
    <>
      {/* ── Seat ────────────────────────────────────────────────────── */}
      <Appear order={appearFrom}>
        <DetailCard title="Seat" icon={CreditCard} subtitle={modeLabel(login.mode)}>
          <dl className="grid grid-cols-1 gap-x-6 gap-y-1.5 sm:grid-cols-2">
            <Fact label="Owner">
              {login.owner_email ?? login.owner_user_id ?? <span className="text-muted-foreground-soft">workspace · no owner</span>}
            </Fact>
            <Fact label="Plan">
              {planLabel(login)}
              {login.plan && login.mode === "subscription" && (
                <span className="ml-1.5 text-muted-foreground-soft">(from token claim)</span>
              )}
            </Fact>
            <Fact label="Account">
              {accountId ? <span className="font-mono">{accountId}</span> : <span className="text-muted-foreground-soft">not reported</span>}
            </Fact>
            <Fact label="Billing">
              {login.mode === "subscription" ? "flat-rate · no per-call $" : "metered · per token through the sidecar"}
            </Fact>
          </dl>
        </DetailCard>
      </Appear>

      {/* ── Quota ───────────────────────────────────────────────────── */}
      <Appear order={appearFrom + 1}>
        <DetailCard
          title="Quota"
          icon={Gauge}
          tone={atLimit ? "warn" : "default"}
          footer={
            login.quota
              ? "Read from the provider's 429 responses and rate-limit headers. A seat at its limit is skipped per run, not per request — the CLI reads its login at start."
              : undefined
          }
        >
          {login.quota ? (
            <div className="space-y-2">
              <div className="flex flex-wrap items-center gap-x-6 gap-y-1.5">
                <QuotaBar label="5-hour" pct={login.quota.window_5h_pct} />
                <QuotaBar label="weekly" pct={login.quota.window_weekly_pct} />
              </div>
              {login.quota.resets_at && (
                <p className="type-meta text-muted-foreground">
                  {atLimit ? "At limit — resets" : "Window resets"} {formatClock(login.quota.resets_at)} ({relTime(login.quota.resets_at)})
                </p>
              )}
              {atLimit && (login.pays_for.agents > 0 || login.pays_for.crews > 0) && (
                <p className="type-meta text-warn">
                  What pays with this seat is paused until the window resets. A second {brand.label} seat on the same
                  scope becomes a pool and takes over.
                </p>
              )}
            </div>
          ) : (
            <p className="text-[12px] text-muted-foreground">
              Quota is not readable for this provider. {brand.label} does not expose its windows in a way Crewship
              can read yet; a 429 still parks the seat for the cooldown.
            </p>
          )}
        </DetailCard>
      </Appear>

      {/* ── Validity & refresh ──────────────────────────────────────── */}
      <Appear order={appearFrom + 2}>
        <DetailCard
          title="Validity & refresh"
          icon={RefreshCw}
          tone={status === "needs_relogin" || status === "expired" ? "destructive" : status === "expiring" ? "warn" : "default"}
          action={
            <span className="inline-flex items-center gap-2">
              <LoginStatusPill credential={credential} />
              {login.refresh.supported && canUpdate && (
                <Button size="sm" variant="outline" onClick={refreshNow} disabled={refreshing}>
                  {refreshing ? <Spinner className="mr-1.5 h-3 w-3" /> : <RefreshCw className="mr-1.5 h-3 w-3" />}
                  Refresh now
                </Button>
              )}
            </span>
          }
          footer={
            login.refresh.supported
              ? "Crewship refreshes this seat centrally, one refresh at a time. Containers get a short-lived access token only, so any number of agents can share the seat without breaking each other's login."
              : undefined
          }
        >
          <dl className="grid grid-cols-1 gap-x-6 gap-y-1.5 sm:grid-cols-2">
            <Fact label={login.mode === "subscription" ? "Access token expires" : "Key expires"}>
              {login.expires_at ? (
                <>
                  {expires}
                  <span className="ml-1.5 text-muted-foreground-soft">{formatDate(login.expires_at)}</span>
                </>
              ) : (
                <span className="text-muted-foreground-soft">unknown · never reported</span>
              )}
            </Fact>
            {login.refresh.supported ? (
              <>
                <Fact label="Last refresh">
                  {login.refresh.last_at ? (
                    <>
                      {formatRelativeTime(login.refresh.last_at)}
                      <span className="ml-1.5 text-muted-foreground-soft">· {login.refresh.status}</span>
                    </>
                  ) : (
                    <span className="text-muted-foreground-soft">never · {login.refresh.status}</span>
                  )}
                </Fact>
                <Fact label="Next refresh">
                  {login.refresh.next_at ? (
                    <>
                      {relTime(login.refresh.next_at)}
                      <span className="ml-1.5 text-muted-foreground-soft">(or at the next run start)</span>
                    </>
                  ) : (
                    <span className="text-muted-foreground-soft">not scheduled</span>
                  )}
                </Fact>
                <Fact label="Refresh token">
                  <Pill tone="purple" data-testid="refresh-token-sealed">
                    <ShieldOff className="h-3 w-3" />
                    SEALED
                  </Pill>
                  <span className="ml-1.5 text-muted-foreground-soft">never leaves the server</span>
                </Fact>
              </>
            ) : (
              <Fact label="Refresh">
                <span className="text-warn">
                  no refresh flow — paste a new {login.mode === "subscription" ? "token" : "key"}
                  {login.expires_at ? ` before ${formatDate(login.expires_at)}` : " when the provider retires this one"}
                </span>
              </Fact>
            )}
          </dl>
          {login.refresh.error && (
            <p className="mt-3 rounded-md border border-destructive/30 bg-destructive/[0.04] px-3 py-2 font-mono text-[11px] text-destructive">
              {login.refresh.error}
            </p>
          )}
          {status === "needs_relogin" && (
            <p className="mt-3 type-meta text-destructive">
              The refresh failed repeatedly and the seat left its pool. Re-login to mint a new one; the bindings stay.
            </p>
          )}
        </DetailCard>
      </Appear>

      {/* ── Delivered to the agent as ───────────────────────────────── */}
      <Appear order={appearFrom + 3}>
        <DetailCard
          title="Delivered to the agent as"
          icon={login.delivery.kind === "file" ? FileText : Wallet}
          footer={
            login.mode === "subscription"
              ? "Traffic goes to the provider through the sidecar tunnel — model policy is not enforced in subscription mode."
              : "Requests go through the sidecar, so the key is metered and model policy applies."
          }
        >
          {login.delivery.kind === "file" ? (
            <>
              <p className="font-mono text-[13px] text-foreground/90">
                {login.delivery.target}
                <span className="ml-2 text-muted-foreground-soft">file · 0600 · in the agent&apos;s HOME</span>
              </p>
              <p className="mt-2 text-[12px] text-muted-foreground">
                Rendered with the real access token and a placeholder refresh token; rewritten at every run start
                and after every refresh; removed on revoke.
              </p>
            </>
          ) : (
            <>
              <p className="font-mono text-[13px] text-foreground/90">
                ${login.delivery.target}
                <span className="ml-2 text-muted-foreground-soft">environment variable</span>
              </p>
              <p className="mt-2 text-[12px] text-muted-foreground">
                Set in the container&apos;s environment at run start; removed on revoke.
              </p>
            </>
          )}
        </DetailCard>
      </Appear>
    </>
  )
}

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-baseline gap-2 text-[12px]">
      <dt className="w-[120px] shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 flex-1 text-foreground/90">{children}</dd>
    </div>
  )
}
