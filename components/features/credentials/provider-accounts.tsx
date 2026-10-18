"use client"

/**
 * The Vault's AI providers space (Harbor 2): the model accounts agents think
 * and pay with. A health strip, then the accounts as cards or as a list (the
 * list is the default from 13 accounts), grouped by provider, owner or
 * account group, 50 at a time. With exactly one provider chosen the space is
 * that provider's page: how it signs in, which runners use it, how agents get
 * it, renewal and the workspace default.
 *
 * Usage windows are drawn only when the server reports them (login.quota,
 * #3442). Recorded tokens and calls come from /paymaster/subscriptions. Spend
 * per account has no backend ($0 for flat-rate by construction) and is not drawn.
 */

import * as React from "react"
import type { LucideIcon } from "lucide-react"
import { AlertTriangle, ChevronDown, CircleDashed, LayoutGrid, List, Plus, Sparkles, UserRound } from "lucide-react"

import { Button } from "@/components/ui/button"
import { getBrand } from "@/lib/credential-providers/registry"
import { loginProvider } from "@/lib/credentials/login-providers"
import {
  ownerLabel,
  paysForLabel,
  planLabel,
  providerRunners,
  type LoginCredential,
  type LoginStatusFilter,
  type ProviderHealthCounts,
} from "@/lib/credentials/provider-logins"
import type { ProvidersGroup, ProvidersLayout } from "@/lib/credentials/vault-url-state"
import { Segmented } from "@/components/ui/segmented"
import { cn } from "@/lib/utils"
import { AccountStateMark, LoginBrandMark, QuotaBar } from "./provider-login-bits"

export const ACCOUNTS_PAGE_SIZE = 50
/** From this many accounts the list is the default look; cards stay one click away. */
export const LIST_FROM = 13

export function providerLabel(provider: string): string {
  return loginProvider(provider)?.label ?? getBrand(provider).label
}

function modeText(c: LoginCredential): string {
  const plan = planLabel(c.login)
  const mode = c.login?.mode === "api_key" ? "API key" : "Subscription"
  return plan && plan !== "—" ? `${mode} · ${plan}` : mode
}

// ── Health ──────────────────────────────────────────────────────────────────

interface Tile {
  key: string
  view?: LoginStatusFilter
  icon: LucideIcon
  label: string
  hint: string
  count: number
  tone: "bad" | "muted" | "ok"
}

const TONE: Record<Tile["tone"], string> = { bad: "text-destructive", muted: "text-muted-foreground", ok: "text-primary" }

export function ProviderHealth({ counts, onOpen }: { counts: ProviderHealthCounts; onOpen: (view: LoginStatusFilter) => void }) {
  const tiles: Tile[] = [
    { key: "accounts", view: "all", icon: Sparkles, label: "Accounts", hint: `${counts.subscriptions} subscription${counts.subscriptions === 1 ? "" : "s"} · ${counts.apiKeys} API key${counts.apiKeys === 1 ? "" : "s"}`, count: counts.accounts, tone: "ok" },
    { key: "signin", view: "attention", icon: AlertTriangle, label: "Need a new sign-in", hint: "Stopped renewing or expired", count: counts.signIn, tone: "bad" },
    { key: "idle", view: "unassigned", icon: CircleDashed, label: "Pay for nothing", hint: "No agent uses them", count: counts.idle, tone: "muted" },
    { key: "people", icon: UserRound, label: "People with an account", hint: "Subscriptions belong to people", count: counts.people, tone: "muted" },
  ]
  return (
    <div role="group" aria-label="AI providers health" className="mb-4 grid grid-cols-2 gap-2 md:grid-cols-4">
      {tiles.map((t) => {
        const Icon = t.icon
        const inner = (
          <>
            <Icon className={cn("size-4", t.count > 0 ? TONE[t.tone] : "text-muted-foreground-soft")} aria-hidden />
            <b className="text-heading font-semibold tabular-nums text-foreground">{t.count}</b>
            <span className="col-span-2 min-w-0 text-label text-foreground/85">
              {t.label}
              <small className="block truncate text-micro text-muted-foreground">{t.hint}</small>
            </span>
          </>
        )
        const cls = "grid min-w-0 grid-cols-[auto_1fr] items-center gap-x-2 gap-y-0.5 rounded-xl border border-border bg-card px-3 py-2.5 text-left"
        return t.view ? (
          <button
            key={t.key}
            type="button"
            data-health={t.key}
            onClick={() => onOpen(t.view!)}
            aria-label={`${t.count} ${t.label.toLowerCase()} — open the list`}
            className={cn(cls, "kit-tap transition-colors hover:border-foreground/25 focus-visible:outline-2 focus-visible:outline-ring")}
          >
            {inner}
          </button>
        ) : (
          <div key={t.key} data-health={t.key} className={cls}>{inner}</div>
        )
      })}
    </div>
  )
}

// ── One account ─────────────────────────────────────────────────────────────

/** Recorded use of one account over the last 30 days (paymaster subscriptions). Never a price. */
export interface AccountTokenUsage {
  tokens: number
  calls: number
}

function compactCount(n: number): string {
  if (n >= 1_000_000) return `${(Math.round(n / 100_000) / 10).toString()}M`
  if (n >= 1_000) return `${(Math.round(n / 100) / 10).toString()}k`
  return String(n)
}

/** Share of a window from which the card warns. */
export const NEAR_LIMIT_PCT = 80

/**
 * The usage row: both windows when the server reports them (with "Near the
 * limit" from 80 %), and the recorded tokens and calls when there are any;
 * with neither, one honest sentence.
 */
export function AccountUsage({ c, usage }: { c: LoginCredential; usage?: AccountTokenUsage }) {
  const q = c.login?.quota
  const used = usage && (usage.tokens > 0 || usage.calls > 0) ? usage : null
  if (!q && !used) return <p className="text-label text-muted-foreground-soft">No usage data yet</p>
  const near = q ? Math.max(q.window_5h_pct ?? 0, q.window_weekly_pct ?? 0) >= NEAR_LIMIT_PCT : false
  return (
    <div className="flex flex-col gap-1">
      {q && (
        <>
          <QuotaBar label="5 hours" pct={q.window_5h_pct} />
          <QuotaBar label="Week" pct={q.window_weekly_pct} />
          {near && <p className="text-label font-medium text-warn">Near the limit</p>}
        </>
      )}
      {used && (
        <p className="text-label text-muted-foreground">
          {compactCount(used.tokens)} tokens · {used.calls} {used.calls === 1 ? "call" : "calls"} · last 30 days
        </p>
      )}
    </div>
  )
}

export function AccountCard({ c, group, usage, onOpen }: { c: LoginCredential; group?: string | null; usage?: AccountTokenUsage; onOpen: (id: string) => void }) {
  const provider = c.login?.provider ?? c.provider
  const pays = paysForLabel(c.login?.pays_for)
  return (
    <button
      type="button"
      data-slot="account-card"
      onClick={() => onOpen(c.id)}
      className="kit-tap flex min-w-0 flex-col gap-3 rounded-card border border-border bg-card p-3.5 text-left transition-colors hover:border-foreground/25 focus-visible:outline-2 focus-visible:outline-ring"
    >
      <span className="flex min-w-0 items-start gap-2.5">
        <LoginBrandMark provider={provider} />
        <span className="grid min-w-0 flex-1 gap-px leading-tight">
          <b className="truncate text-control font-semibold text-foreground">{c.name}</b>
          <small className="truncate text-label text-muted-foreground">{providerLabel(provider)} · {modeText(c)}</small>
        </span>
        <AccountStateMark credential={c} />
      </span>
      <AccountUsage c={c} usage={usage} />
      <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 border-t border-border pt-2.5 text-label">
        <span className="inline-flex min-w-0 items-center gap-1 text-foreground/85">
          <UserRound className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
          <span className="truncate">{ownerLabel(c)}</span>
        </span>
        {group && <span className="truncate text-muted-foreground">· {group}</span>}
        <span className="flex-1" />
        <span className="text-muted-foreground">{pays === "nobody yet" ? "pays for no one" : `pays for ${pays}`}</span>
      </span>
    </button>
  )
}

function AddAccountCard({ onAdd }: { onAdd: () => void }) {
  return (
    <button
      type="button"
      onClick={onAdd}
      className="kit-tap flex min-h-32 flex-col items-center justify-center gap-1 rounded-card border border-dashed border-border bg-transparent p-3.5 text-center text-muted-foreground transition-colors hover:border-foreground/25 hover:text-foreground"
    >
      <Plus className="size-5" aria-hidden />
      <b className="text-control font-semibold text-foreground">Add AI provider</b>
      <small className="text-label">Sign in with a subscription or paste an API key</small>
    </button>
  )
}

// ── The list ────────────────────────────────────────────────────────────────

function AccountsTable({ rows, caption, groupOf, onOpen }: { rows: LoginCredential[]; caption: string; groupOf: (id: string) => string | null; onOpen: (id: string) => void }) {
  return (
    <div className="overflow-hidden rounded-card border border-border bg-card">
      <table className="w-full table-fixed text-left md:table-auto" aria-label={caption}>
        <thead className="hidden border-b border-border bg-muted/50 text-label text-muted-foreground md:table-header-group">
          <tr className="h-[34px]">
            <th className="pl-3 pr-2 font-normal">Account</th>
            <th className="hidden pr-2 font-normal lg:table-cell">Owner</th>
            <th className="pr-2 font-normal">Pays for</th>
            <th className="pr-3 font-normal">State</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((c) => {
            const provider = c.login?.provider ?? c.provider
            const group = groupOf(c.id)
            return (
              <tr key={c.id} data-row-id={c.id} onClick={() => onOpen(c.id)} className="cursor-pointer border-b border-border transition-colors last:border-b-0 hover:bg-muted/60">
                <td className="min-w-0 py-2 pl-3 pr-2 align-middle">
                  <span className="flex min-w-0 items-center gap-2.5">
                    <LoginBrandMark provider={provider} size="sm" />
                    <span className="grid min-w-0 gap-px leading-tight">
                      <button
                        type="button"
                        onClick={(e) => { e.stopPropagation(); onOpen(c.id) }}
                        className="truncate text-left text-control font-medium text-foreground hover:underline focus-visible:outline-2 focus-visible:outline-ring"
                      >
                        {c.name}
                      </button>
                      <small className="truncate text-label text-muted-foreground">
                        {modeText(c)}{group ? ` · ${group}` : ""}
                      </small>
                    </span>
                  </span>
                </td>
                <td className="hidden truncate py-2 pr-2 align-middle text-label text-muted-foreground lg:table-cell">{ownerLabel(c)}</td>
                <td className="hidden py-2 pr-2 align-middle text-label text-muted-foreground md:table-cell">{paysForLabel(c.login?.pays_for) === "nobody yet" ? "No one" : paysForLabel(c.login?.pays_for)}</td>
                <td className="whitespace-nowrap py-2 pl-1 pr-3 text-right align-middle md:text-left"><AccountStateMark credential={c} /></td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

export interface AccountsListProps {
  rows: LoginCredential[]
  /** null = the default for the size: list from 13 accounts (cards on a phone). */
  layout: ProvidersLayout | null
  onLayoutChange: (l: ProvidersLayout) => void
  group: ProvidersGroup
  onGroupChange: (g: ProvidersGroup) => void
  onOpen: (id: string) => void
  /** Absent for a reader who cannot add accounts. */
  onAdd?: () => void
  /** Account group of an account, by credential id. */
  poolOf: (id: string) => string | null
  isPhone?: boolean
  /** Recorded use per credential id; absent = not loaded, accounts say "No usage data yet". */
  usage?: Record<string, AccountTokenUsage>
  /** No look or grouping controls — a list inside another page. */
  fixed?: boolean
}

export function resolveLayout(layout: ProvidersLayout | null, count: number, isPhone = false): ProvidersLayout {
  if (layout) return layout
  return count >= LIST_FROM && !isPhone ? "list" : "cards"
}

export function AccountsList({ rows, layout, onLayoutChange, group, onGroupChange, onOpen, onAdd, poolOf, isPhone, usage, fixed = false }: AccountsListProps) {
  const view = resolveLayout(layout, rows.length, isPhone)
  const [limit, setLimit] = React.useState(ACCOUNTS_PAGE_SIZE)
  const key = rows.map((r) => r.id).join(",")
  React.useEffect(() => setLimit(ACCOUNTS_PAGE_SIZE), [key])

  const groups = React.useMemo(() => {
    if (view !== "list" || group === "none") return null
    const keyOf = (c: LoginCredential) =>
      group === "provider" ? providerLabel(c.login?.provider ?? c.provider) : group === "owner" ? ownerLabel(c) : poolOf(c.id) ?? "No group"
    const by = new Map<string, LoginCredential[]>()
    for (const r of rows) by.set(keyOf(r), [...(by.get(keyOf(r)) ?? []), r])
    return [...by.entries()].sort((a, b) => b[1].length - a[1].length || a[0].localeCompare(b[0]))
  }, [rows, view, group, poolOf])

  const more = rows.length > ACCOUNTS_PAGE_SIZE && !groups ? (
    <div className="flex items-center gap-3 px-1">
      {rows.length > limit && (
        <Button variant="outline" size="sm" onClick={() => setLimit((n) => n + ACCOUNTS_PAGE_SIZE)}>
          <ChevronDown className="size-3.5" aria-hidden /> Load 50 more
        </Button>
      )}
      <span className="text-label text-muted-foreground">Showing {Math.min(limit, rows.length)} of {rows.length}</span>
    </div>
  ) : null

  return (
    <div className="flex flex-col gap-3" data-slot="provider-accounts" data-layout={view}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-label text-muted-foreground" aria-live="polite">{rows.length} account{rows.length === 1 ? "" : "s"}</span>
        <span className="flex-1" />
        {view === "list" && !fixed && (
          <>
            <span className="text-label text-muted-foreground">Group</span>
            <Segmented
              ariaLabel="Group accounts"
              size="xs"
              value={group}
              onChange={onGroupChange}
              options={[{ value: "none", label: "None" }, { value: "provider", label: "Provider" }, { value: "owner", label: "Owner" }, { value: "pool", label: "Group" }]}
            />
          </>
        )}
        {!fixed && (
          <Segmented
            ariaLabel="Show accounts as"
            size="xs"
            value={view}
            onChange={onLayoutChange}
            options={[
              { value: "cards", label: null, icon: LayoutGrid, ariaLabel: "Cards" },
              { value: "list", label: null, icon: List, ariaLabel: "List" },
            ]}
          />
        )}
      </div>

      {view === "cards" ? (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {rows.slice(0, limit).map((c) => <AccountCard key={c.id} c={c} group={poolOf(c.id)} usage={usage?.[c.id]} onOpen={onOpen} />)}
          {onAdd && <AddAccountCard onAdd={onAdd} />}
        </div>
      ) : groups ? (
        groups.map(([label, items]) => <GroupSection key={label} label={label} items={items} poolOf={poolOf} onOpen={onOpen} />)
      ) : (
        <AccountsTable rows={rows.slice(0, limit)} caption="AI provider accounts" groupOf={poolOf} onOpen={onOpen} />
      )}
      {more}
    </div>
  )
}

function GroupSection({ label, items, poolOf, onOpen }: { label: string; items: LoginCredential[]; poolOf: (id: string) => string | null; onOpen: (id: string) => void }) {
  const [open, setOpen] = React.useState(false)
  return (
    <section aria-label={`${label} · ${items.length}`} className="flex flex-col gap-1.5">
      <h3 className="px-1 text-label font-semibold text-foreground">
        {label} <span className="font-normal tabular-nums text-muted-foreground">· {items.length}</span>
      </h3>
      <AccountsTable rows={open ? items : items.slice(0, 5)} caption={label} groupOf={poolOf} onOpen={onOpen} />
      {items.length > 5 && !open && (
        <button type="button" onClick={() => setOpen(true)} className="kit-tap self-start px-1 text-label text-primary hover:underline">
          Show all {items.length} ›
        </button>
      )}
    </section>
  )
}

// ── A provider's page ───────────────────────────────────────────────────────

function signInWith(provider: string): string {
  const p = loginProvider(provider)
  return p?.detail ?? "API key"
}

function deliveredAs(rows: LoginCredential[]): string {
  const targets = [...new Set(rows.map((c) => c.login?.delivery ? (c.login.delivery.kind === "file" ? `file ${c.login.delivery.target}` : `env ${c.login.delivery.target}`) : null).filter(Boolean))]
  return targets.length > 0 ? targets.join(" · ") : "—"
}

function renewal(rows: LoginCredential[]): string {
  if (rows.length === 0) return "—"
  const auto = rows.filter((c) => c.login?.refresh.supported).length
  if (auto === rows.length) return "Renews itself on the server"
  if (auto === 0) return "No automatic renewal; sign in again when it stops working"
  return `${auto} of ${rows.length} renew themselves on the server`
}

export function ProviderHead({
  provider,
  rows,
  workspaceDefault,
  onAdd,
}: {
  provider: string
  rows: LoginCredential[]
  /** The account bound for the whole workspace, if any. undefined = still loading. */
  workspaceDefault: string | null | undefined
  onAdd?: () => void
}) {
  const label = providerLabel(provider)
  const subs = rows.filter((c) => c.login?.mode !== "api_key").length
  const facts: [string, string][] = [
    ["Sign in with", signInWith(provider)],
    ["Runners", providerRunners(provider).join(", ")],
    ["Agents get it as", deliveredAs(rows)],
    ["Renewal", renewal(rows)],
    ["Workspace default", workspaceDefault === undefined ? "…" : workspaceDefault ?? "None"],
  ]
  return (
    <div className="mb-4 flex flex-col gap-3" data-slot="provider-head">
      <header className="flex flex-wrap items-center gap-3">
        <LoginBrandMark provider={provider} />
        <div className="min-w-0 flex-1">
          <h1 className="truncate text-title font-semibold tracking-tight text-foreground">{label}</h1>
          <p className="text-body text-muted-foreground">
            {rows.length} account{rows.length === 1 ? "" : "s"} · {subs} subscription{subs === 1 ? "" : "s"} · {rows.length - subs} API key{rows.length - subs === 1 ? "" : "s"}
          </p>
        </div>
        {onAdd && (
          <Button size="sm" onClick={onAdd}>
            <Plus className="size-3.5" aria-hidden /> Add {label.split(" / ")[0]} account
          </Button>
        )}
      </header>
      <dl className="grid gap-x-6 gap-y-2 rounded-card border border-border bg-card p-4 sm:grid-cols-2 lg:grid-cols-5">
        {facts.map(([k, v]) => (
          <div key={k} className="min-w-0">
            <dt className="text-micro text-muted-foreground">{k}</dt>
            <dd className="break-words text-label text-foreground">{v}</dd>
          </div>
        ))}
      </dl>
    </div>
  )
}
