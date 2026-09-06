"use client"

/**
 * The Providers tab of /credentials (PRD provider-logins §6.1).
 *
 * A provider login is a seat that pays for a model — it has an owner, a plan,
 * an expiry and, where the provider exposes them, two quota windows — and the
 * Overview cannot show any of that: "CLAUDE_CODE_OAUTH_TOKEN · ai cli · L1" is
 * all a secret row has room for. This pane is one card per seat with exactly
 * those facts, under four tiles that answer what an operator asks on arrival:
 * how many seats, how many are paused, how many expire this month, how many
 * pay for nobody.
 *
 * Every figure is read from the row's `login` object, and a fact the server
 * does not send is drawn as unknown — "quota not readable for this provider",
 * "Expires —" — never as a number.
 */

import * as React from "react"
import { ChevronRight, CreditCard, Plus, UserPlus } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Appear } from "@/components/ui/detail"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { KpiCard } from "@/components/features/dashboard/kpi-card"
import { EmptyState } from "@/components/layout/empty-state"
import {
  formatClock,
  formatExpiresIn,
  isUnassigned,
  loginExpiresAt,
  loginSubtitle,
  loginTotals,
  paysForLabel,
  planLabel,
  type LoginCredential,
  type LoginStatusFilter,
} from "@/lib/credentials/provider-logins"
import { LoginBrandMark, LoginStatusPill, QuotaSummary } from "./provider-login-bits"

export interface ProviderLoginsPanelProps {
  /** Every seat in the workspace — the tiles count the whole tab. */
  logins: LoginCredential[]
  /** What the rail's filters leave — the list. */
  visible: LoginCredential[]
  onSelect: (id: string) => void
  /** Sets the rail's status facet — the tiles' click-through. */
  onSelectStatus: (status: LoginStatusFilter) => void
  /** Opens the wizard on the Provider login shape. Absent for a reader who cannot create. */
  onAdd?: () => void
  /** Opens the assign flow for one seat. Absent for a reader who cannot bind. */
  onAssign?: (id: string) => void
}

export function ProviderLoginsPanel({ logins, visible, onSelect, onSelectStatus, onAdd, onAssign }: ProviderLoginsPanelProps) {
  const totals = React.useMemo(() => loginTotals(logins), [logins])

  if (logins.length === 0) {
    return (
      <EmptyState
        icon={CreditCard}
        title="No provider logins yet"
        description="A provider login is a seat that pays for a model — a Claude Max setup-token, a ChatGPT plan, a metered API key. It has an owner, a plan and an expiry, and Crewship keeps it valid."
      >
        {onAdd && (
          <div className="mt-4 flex items-center justify-center">
            <Button onClick={onAdd}>
              <Plus className="mr-2 h-4 w-4" />
              Add provider login
            </Button>
          </div>
        )}
      </EmptyState>
    )
  }

  return (
    <div className="mx-auto flex max-w-[1800px] flex-col gap-4">
      <Appear order={0}>
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div>
            <h1 className="text-lg font-semibold tracking-tight">Providers</h1>
            <p className="text-xs text-muted-foreground">
              {totals.seats} provider {totals.seats === 1 ? "login" : "logins"} in this workspace
            </p>
          </div>
        </div>
      </Appear>

      <Appear order={1}>
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <KpiCard
            label="Seats"
            value={totals.seats}
            subtitle={`${totals.subscription} subscription · ${totals.apiKey} API ${totals.apiKey === 1 ? "key" : "keys"}`}
            onClick={() => onSelectStatus("all")}
          />
          <KpiCard
            label="At limit"
            value={totals.atLimit}
            valueColor={totals.atLimit > 0 ? "rgb(251, 191, 36)" : undefined}
            subtitle={
              totals.atLimit === 0
                ? "no window is exhausted"
                : [
                    totals.nextReset ? `resets ${formatClock(totals.nextReset)}` : null,
                    `${totals.agentsWaiting} ${totals.agentsWaiting === 1 ? "agent" : "agents"} waiting`,
                  ]
                    .filter(Boolean)
                    .join(" · ")
            }
            onClick={totals.atLimit > 0 ? () => onSelectStatus("at_limit") : undefined}
          />
          <KpiCard
            label="Expiring ≤ 30 d"
            value={totals.expiring}
            valueColor={totals.expiring > 0 ? "rgb(251, 191, 36)" : undefined}
            subtitle={
              totals.expiring === 0
                ? "nothing expires this month"
                : `auto-refresh handles ${totals.expiringAutoRefreshed} of ${totals.expiring}`
            }
            onClick={totals.expiring > 0 ? () => onSelectStatus("expiring") : undefined}
          />
          <KpiCard
            label="Unassigned"
            value={totals.unassigned}
            subtitle={totals.unassigned === 0 ? "every seat pays for someone" : "no agent pays with it yet"}
            onClick={totals.unassigned > 0 ? () => onSelectStatus("unassigned") : undefined}
          />
        </div>
      </Appear>

      <Appear order={2}>
        <DashboardCard
          title="Provider logins"
          icon={CreditCard}
          hint={visible.length === logins.length ? `${logins.length}` : `${visible.length} of ${logins.length}`}
          action={
            onAdd ? (
              <button type="button" onClick={onAdd} className="text-primary hover:underline">
                + Add provider login
              </button>
            ) : undefined
          }
        >
          {visible.length === 0 ? (
            <p className="py-6 text-center text-[11px] text-muted-foreground-soft">Nothing matches these filters.</p>
          ) : (
            <ul role="list" aria-label="Provider logins" className="flex flex-col divide-y divide-border/40">
              {visible.map((c) => (
                <LoginRow key={c.id} credential={c} onSelect={() => onSelect(c.id)} onAssign={onAssign ? () => onAssign(c.id) : undefined} />
              ))}
            </ul>
          )}
        </DashboardCard>
      </Appear>

      <Appear order={3}>
        <p className="type-meta text-muted-foreground-soft">
          Secrets (GitHub, AWS, SMTP, …) stay under Overview. A provider login is a seat that pays for a
          model — it has an owner, a plan and an expiry, and Crewship keeps it valid.
        </p>
      </Appear>
    </div>
  )
}

function LoginRow({
  credential, onSelect, onAssign,
}: {
  credential: LoginCredential
  onSelect: () => void
  onAssign?: () => void
}) {
  const login = credential.login ?? null
  const unassigned = isUnassigned(credential)
  const expires = formatExpiresIn(loginExpiresAt(credential))
  return (
    <li className="group flex flex-wrap items-center gap-x-4 gap-y-2 py-3 first:pt-1 last:pb-1">
      {/* The row is one button — the whole card opens the seat — with Assign
          as a second, separate control beside it so a click on it does not
          also navigate. */}
      <button
        type="button"
        onClick={onSelect}
        aria-label={`Open ${credential.name}`}
        className="flex min-w-0 flex-1 flex-wrap items-center gap-x-4 gap-y-2 rounded-md text-left transition-colors hover:bg-white/[0.02]"
      >
        <span className="flex min-w-0 flex-1 basis-[240px] items-center gap-3">
          <LoginBrandMark provider={login?.provider ?? credential.provider} />
          <span className="min-w-0">
            <span className="block truncate text-[13px] font-medium text-foreground">{credential.name}</span>
            <span className="block truncate type-meta text-muted-foreground">{loginSubtitle(credential)}</span>
          </span>
        </span>

        <span className="grid shrink-0 grid-cols-3 gap-4">
          <Kv label="Plan" value={planLabel(login)} />
          <Kv
            label="Expires"
            value={
              login?.refresh.supported && login.expires_at
                ? `${expires} · auto`
                : expires
            }
          />
          <Kv label="Pays for" value={paysForLabel(login?.pays_for)} />
        </span>

        <span className="w-[200px] shrink-0">
          {login ? <QuotaSummary login={login} /> : null}
        </span>
      </button>

      <span className="flex shrink-0 items-center gap-2">
        {unassigned && onAssign && (
          <Button size="sm" variant="outline" onClick={onAssign} className="h-7">
            <UserPlus className="mr-1.5 h-3 w-3" />
            Assign
          </Button>
        )}
        <LoginStatusPill credential={credential} />
        <ChevronRight className="h-3.5 w-3.5 text-muted-foreground-soft" aria-hidden="true" />
      </span>
    </li>
  )
}

function Kv({ label, value }: { label: string; value: string }) {
  return (
    <span className="flex min-w-[96px] flex-col gap-0.5">
      <span className="type-meta text-muted-foreground-soft">{label}</span>
      <span className="truncate text-[12px] font-medium text-foreground/90">{value}</span>
    </span>
  )
}
