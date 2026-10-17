"use client"

/**
 * An account group's own page in the Vault (Harbor 2): Accounts · Order ·
 * Settings. Replaces the account groups dialog.
 *
 * A group gathers accounts of one provider and one way of paying, in an order
 * of preference. Every write names the revision it was read at (If-Match), so
 * a change someone else made in the meantime is reported, never overwritten.
 * Groups do not assign accounts or switch between them at run time yet, and
 * the page says so instead of drawing a "then" step that does nothing.
 *
 * Who may see a group's accounts (the design's Access tab) has no backend; it
 * is not drawn.
 */

import * as React from "react"
import { useQueryClient } from "@tanstack/react-query"
import { ArrowDown, ArrowUp, ChevronLeft, Layers, RefreshCw, Trash2 } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { Input } from "@/components/ui/input"
import { PageSaveBar, PageSaveProvider, usePageSave } from "@/components/ui/page-save-bar"
import { TabBar } from "@/components/ui/tab-bar"
import { PageError, PageLoading, PageNotFound } from "@/components/layout/page-state"
import {
  POOL_CONFLICT_MESSAGE,
  PoolRequestError,
  poolKeys,
  poolRequest,
  useProviderPool,
  type PoolMember,
  type ProviderPool,
} from "@/hooks/use-provider-pools"
import { ownerLabel, type LoginCredential } from "@/lib/credentials/provider-logins"
import type { PoolTab } from "@/lib/credentials/vault-url-state"
import { toast } from "@/lib/toast"
import { cn } from "@/lib/utils"
import { AccountsList, providerLabel } from "./provider-accounts"
import { AccountStateMark, LoginBrandMark } from "./provider-login-bits"
import { SettingsCard } from "./secret-settings"

export interface AccountGroupPageProps {
  workspaceId: string
  poolId: string
  tab: PoolTab | null
  onTabChange: (tab: PoolTab) => void
  /** Every AI provider account the page loaded. */
  accounts: LoginCredential[]
  canManage: boolean
  onBack: () => void
  onOpenAccount: (id: string) => void
  onDeleted: () => void
}

const sortMembers = (m: PoolMember[]) => [...m].sort((a, b) => a.priority - b.priority)

export function AccountGroupPage(props: AccountGroupPageProps) {
  const { workspaceId, poolId, canManage, onBack } = props
  const { pool, loading, error, reload } = useProviderPool(workspaceId, poolId, canManage)

  if (loading) return <div className="p-4 md:p-6"><PageLoading variant="cards" /></div>
  if (error instanceof PoolRequestError && error.status === 404) {
    return (
      <div className="p-4 md:p-6">
        <PageNotFound what="account group" description="It was deleted, or the link points to another workspace." back={{ label: "Back to AI providers", onClick: onBack }} />
      </div>
    )
  }
  if (error || !pool) {
    return (
      <div className="p-4 md:p-6">
        <PageError title="Couldn't open this account group" description={error?.message ?? "The group did not load."} onRetry={reload} />
      </div>
    )
  }
  return (
    <PageSaveProvider>
      <GroupBody {...props} pool={pool} reload={reload} />
    </PageSaveProvider>
  )
}

function GroupBody({
  workspaceId, pool, tab, onTabChange, accounts, canManage, onBack, onOpenAccount, onDeleted, reload,
}: AccountGroupPageProps & { pool: ProviderPool; reload: () => void }) {
  const client = useQueryClient()
  const [conflict, setConflict] = React.useState(false)
  const members = React.useMemo(() => sortMembers(pool.members ?? []), [pool.members])
  const byId = React.useMemo(() => new Map(accounts.map((a) => [a.id, a])), [accounts])
  const memberAccounts = members.map((m) => byId.get(m.credential_id)).filter((a): a is LoginCredential => Boolean(a))
  const active: PoolTab = tab ?? "accounts"

  /** PUT with the revision the page read. A 412 is shown on the page; the draft stays. */
  const put = async (body: { name: string; allow_cross_owner: boolean; members: PoolMember[] }) => {
    try {
      await poolRequest(workspaceId, `provider-logins/pools/${encodeURIComponent(pool.id)}`, {
        method: "PUT",
        headers: { "If-Match": `"${pool.revision}"` },
        body: JSON.stringify(body),
      })
    } catch (err) {
      if (err instanceof PoolRequestError && err.status === 412) setConflict(true)
      throw err
    }
    setConflict(false)
    await client.invalidateQueries({ queryKey: poolKeys.all(workspaceId) })
  }

  const reloadLatest = () => {
    setConflict(false)
    reload()
  }

  const modeText = pool.mode === "api_key" ? "API key" : "Subscription"

  return (
    <div className="relative flex h-full flex-col" data-slot="account-group-page">
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-6xl flex-col gap-4 p-4 pb-24 md:p-6 md:pb-24">
          <button type="button" onClick={onBack} className="kit-tap inline-flex w-fit items-center gap-1 text-label text-muted-foreground hover:text-foreground">
            <ChevronLeft className="size-3.5" aria-hidden /> AI providers
          </button>
          <header className="flex flex-wrap items-start gap-3">
            <span aria-hidden className="inline-grid size-10 shrink-0 place-items-center rounded-lg border border-border bg-muted">
              <Layers className="size-5 text-muted-foreground" />
            </span>
            <div className="min-w-0 flex-1">
              <h1 className="truncate text-title font-semibold tracking-tight text-foreground">{pool.name}</h1>
              <p className="text-body text-muted-foreground">
                {providerLabel(pool.provider)} · {modeText} · {members.length} account{members.length === 1 ? "" : "s"}
              </p>
              <p className="mt-1 text-label text-muted-foreground">Groups do not assign accounts or enable automatic switching yet.</p>
            </div>
          </header>

          {conflict && (
            <div role="alert" className="flex flex-wrap items-center gap-3 rounded-card border border-warn/30 bg-chip-warn-bg px-4 py-3">
              <p className="min-w-0 flex-1 text-label text-foreground">{POOL_CONFLICT_MESSAGE}</p>
              <Button size="sm" variant="outline" onClick={reloadLatest}><RefreshCw className="size-3.5" aria-hidden /> Reload</Button>
            </div>
          )}

          <TabBar value={active} onValueChange={(v) => onTabChange(v as PoolTab)} ariaLabel="Account group sections" className="overflow-x-auto">
            <TabBar.Item value="accounts">Accounts</TabBar.Item>
            <TabBar.Item value="order">Order</TabBar.Item>
            <TabBar.Item value="settings">Settings</TabBar.Item>
          </TabBar>

          <div role="tabpanel" aria-label={active === "accounts" ? "Accounts" : active === "order" ? "Order" : "Settings"}>
            {active === "accounts" ? (
              memberAccounts.length === 0 ? (
                <p className="text-label text-muted-foreground">No account in this group is visible to you. Add accounts in Settings.</p>
              ) : (
                <AccountsList
                  rows={memberAccounts}
                  layout="list"
                  onLayoutChange={() => {}}
                  group="none"
                  onGroupChange={() => {}}
                  onOpen={onOpenAccount}
                  poolOf={() => null}
                  fixed
                />
              )
            ) : active === "order" ? (
              <OrderTab key={`${pool.id}:${pool.revision}`} pool={pool} members={members} byId={byId} canManage={canManage} save={put} />
            ) : (
              <SettingsTab
                key={`${pool.id}:${pool.revision}`}
                workspaceId={workspaceId}
                pool={pool}
                members={members}
                accounts={accounts}
                canManage={canManage}
                save={put}
                onDeleted={onDeleted}
                onConflict={() => setConflict(true)}
              />
            )}
          </div>
        </div>
      </div>
      <PageSaveBar />
    </div>
  )
}

function OrderTab({
  pool, members, byId, canManage, save,
}: {
  pool: ProviderPool
  members: PoolMember[]
  byId: Map<string, LoginCredential>
  canManage: boolean
  save: (body: { name: string; allow_cross_owner: boolean; members: PoolMember[] }) => Promise<void>
}) {
  const initial = React.useMemo(() => members.map((m) => m.credential_id), [members])
  const [order, setOrder] = React.useState<string[]>(initial)
  const [saving, setSaving] = React.useState(false)
  const changed = order.some((id, i) => id !== initial[i])

  const move = (i: number, d: -1 | 1) => {
    const j = i + d
    if (j < 0 || j >= order.length) return
    const next = [...order]
    ;[next[i], next[j]] = [next[j], next[i]]
    setOrder(next)
  }

  usePageSave({
    label: "Order",
    count: changed ? 1 : 0,
    saving,
    save: async () => {
      setSaving(true)
      try {
        await save({ name: pool.name, allow_cross_owner: pool.allow_cross_owner, members: order.map((id, i) => ({ credential_id: id, priority: i })) })
        toast.success("Order saved")
      } finally {
        setSaving(false)
      }
    },
    discard: () => setOrder(initial),
  })

  return (
    <SettingsCard title="When an account is at its limit">
      <ol className="flex flex-col" aria-label="Account order">
        {order.map((id, i) => {
          const a = byId.get(id)
          const name = a?.name ?? "Unavailable account"
          return (
            <li key={id} className="flex items-center gap-3 border-t border-border px-4 py-2.5 first:border-t-0">
              <span className="inline-grid size-6 shrink-0 place-items-center rounded-full bg-muted font-mono text-label text-muted-foreground">{i + 1}</span>
              {a && <LoginBrandMark provider={a.login?.provider ?? a.provider} size="sm" />}
              <span className="grid min-w-0 flex-1 leading-tight">
                <b className="truncate text-control font-medium text-foreground">{name}</b>
                <small className="truncate text-label text-muted-foreground">{a ? ownerLabel(a) : id}</small>
              </span>
              {a && <AccountStateMark credential={a} />}
              {canManage && (
                <span className="flex shrink-0 gap-0.5">
                  <Button type="button" size="icon-xs" variant="ghost" aria-label={`Move ${name} up`} disabled={i === 0} onClick={() => move(i, -1)}>
                    <ArrowUp className="size-3.5" />
                  </Button>
                  <Button type="button" size="icon-xs" variant="ghost" aria-label={`Move ${name} down`} disabled={i === order.length - 1} onClick={() => move(i, 1)}>
                    <ArrowDown className="size-3.5" />
                  </Button>
                </span>
              )}
            </li>
          )
        })}
      </ol>
      <p className="border-t border-border px-4 py-3 text-label text-muted-foreground">
        The first account is preferred. Groups do not assign accounts or enable automatic switching yet.
      </p>
    </SettingsCard>
  )
}

function SettingsTab({
  workspaceId, pool, members, accounts, canManage, save, onDeleted, onConflict,
}: {
  workspaceId: string
  pool: ProviderPool
  members: PoolMember[]
  accounts: LoginCredential[]
  canManage: boolean
  save: (body: { name: string; allow_cross_owner: boolean; members: PoolMember[] }) => Promise<void>
  onDeleted: () => void
  onConflict: () => void
}) {
  const client = useQueryClient()
  const baseIds = React.useMemo(() => members.map((m) => m.credential_id), [members])
  const [name, setName] = React.useState(pool.name)
  const [cross, setCross] = React.useState(pool.allow_cross_owner)
  const [ids, setIds] = React.useState<string[]>(baseIds)
  const [saving, setSaving] = React.useState(false)
  const [confirm, setConfirm] = React.useState(false)

  const eligible = accounts.filter((a) => (a.login?.provider ?? a.provider) === pool.provider && a.login?.mode === pool.mode)
  const sameIds = ids.length === baseIds.length && ids.every((id) => baseIds.includes(id))
  const count = (name.trim() !== pool.name ? 1 : 0) + (cross !== pool.allow_cross_owner ? 1 : 0) + (sameIds ? 0 : 1)
  const valid = name.trim().length > 0 && ids.length > 0 && ids.length <= 100

  usePageSave({
    label: "Settings",
    count,
    canSave: valid,
    saving,
    save: async () => {
      setSaving(true)
      try {
        // Kept members keep their place; new ones go last.
        const kept = members.filter((m) => ids.includes(m.credential_id))
        const added = ids.filter((id) => !baseIds.includes(id)).map((id, i) => ({ credential_id: id, priority: kept.length + i }))
        await save({ name: name.trim(), allow_cross_owner: cross, members: [...kept.map((m, i) => ({ credential_id: m.credential_id, priority: i })), ...added] })
        toast.success("Account group saved")
      } finally {
        setSaving(false)
      }
    },
    discard: () => { setName(pool.name); setCross(pool.allow_cross_owner); setIds(baseIds) },
  })

  const remove = async () => {
    try {
      await poolRequest(workspaceId, `provider-logins/pools/${encodeURIComponent(pool.id)}`, { method: "DELETE", headers: { "If-Match": `"${pool.revision}"` } })
    } catch (err) {
      if (err instanceof PoolRequestError && err.status === 412) onConflict()
      toast.error(err instanceof Error ? err.message : "Couldn't delete the group.")
      throw err
    }
    await client.invalidateQueries({ queryKey: poolKeys.all(workspaceId) })
    toast.success(`${pool.name} deleted`)
    onDeleted()
  }

  const ro = !canManage
  return (
    <div className="flex max-w-[860px] flex-col gap-4">
      <SettingsCard title="Details">
        <div className="grid gap-2 px-4 py-3 sm:grid-cols-[200px_minmax(0,1fr)] sm:items-start">
          <label htmlFor="group-name" className="text-control font-medium text-foreground">Name</label>
          <Input id="group-name" value={name} disabled={ro} maxLength={200} onChange={(e) => setName(e.target.value)} className="h-8 text-control" aria-invalid={!name.trim() || undefined} />
        </div>
        <div className="grid gap-2 border-t border-border px-4 py-3 sm:grid-cols-[200px_minmax(0,1fr)] sm:items-start">
          <div>
            <span className="text-control font-medium text-foreground">Different owners</span>
            <p className="text-label text-muted-foreground">Only with the owners&apos; consent; provider terms still apply</p>
          </div>
          <label className="inline-flex items-center gap-2 text-control">
            <Checkbox checked={cross} disabled={ro} onCheckedChange={(v) => setCross(v === true)} aria-label="Allow accounts owned by different people" />
            Allow accounts owned by different people
          </label>
        </div>
        <div className="grid gap-2 border-t border-border px-4 py-3 sm:grid-cols-[200px_minmax(0,1fr)] sm:items-start">
          <div>
            <span className="text-control font-medium text-foreground">Accounts</span>
            <p className="text-label text-muted-foreground">{providerLabel(pool.provider)} · {pool.mode === "api_key" ? "API key" : "Subscription"} only</p>
          </div>
          <ul className="flex flex-col gap-1" aria-label="Accounts in this group">
            {eligible.length === 0 && <li className="text-label text-muted-foreground">No account of this provider and kind is connected.</li>}
            {eligible.map((a) => {
              const on = ids.includes(a.id)
              return (
                <li key={a.id}>
                  <label className={cn("flex items-center gap-2 rounded-md px-1 py-1 text-control", !ro && "cursor-pointer hover:bg-muted/60")}>
                    <Checkbox checked={on} disabled={ro} onCheckedChange={() => setIds(on ? ids.filter((x) => x !== a.id) : [...ids, a.id])} aria-label={`Include ${a.name}`} />
                    <LoginBrandMark provider={a.login?.provider ?? a.provider} size="sm" />
                    <span className="min-w-0 flex-1 truncate">{a.name}</span>
                    <span className="truncate text-label text-muted-foreground">{ownerLabel(a)}</span>
                  </label>
                </li>
              )
            })}
            {ids.length === 0 && <li role="alert" className="text-label text-destructive">Keep at least one account.</li>}
          </ul>
        </div>
      </SettingsCard>
      {canManage && (
        <SettingsCard title="Danger zone" tone="danger">
          <div className="flex flex-wrap items-center gap-3 px-4 py-3">
            <div className="min-w-0 flex-1">
              <b className="text-control font-medium">Delete group</b>
              <p className="text-label text-muted-foreground">Its accounts stay; they only leave the group.</p>
            </div>
            <Button variant="outline" size="sm" className="text-destructive" onClick={() => setConfirm(true)}>
              <Trash2 className="size-3.5" aria-hidden /> Delete…
            </Button>
          </div>
        </SettingsCard>
      )}
      <ConfirmDialog
        open={confirm}
        onOpenChange={setConfirm}
        title={`Delete ${pool.name}?`}
        consequences={[
          { tone: "kept", text: `Its ${members.length} account${members.length === 1 ? "" : "s"} stay in the Vault and keep paying for the same agents.` },
          { tone: "warn", text: "The name stays reserved; a new group needs another name." },
        ]}
        confirmLabel="Delete group"
        destructive
        onConfirm={remove}
      />
    </div>
  )
}
