"use client"

/**
 * The Vault's Secrets table (Harbor 2): one row per secret — what it is, who
 * holds it, when it was last used, how hard Keeper guards it and whether it
 * works. 50 rows at a time with "Load 50 more", optional grouping by type.
 *
 * Selection is per row and only offered to a role with a bulk action; the
 * page decides what the bulk bar can do and counts only rows still visible.
 *
 * The Folder column of the design is not drawn: folders have no backend yet.
 */

import * as React from "react"
import { ChevronDown } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { StatusPill } from "@/components/ui/status-pill"
import { getBrand, brandColor } from "@/lib/credential-providers/registry"
import { classificationOf, deriveSecretState, type CredentialLike } from "@/lib/credentials/facets"
import { credentialTypeLabel } from "@/lib/credentials/item-types"
import { tierOf } from "@/lib/credentials/tiers"
import type { VaultGroup } from "@/lib/credentials/vault-url-state"
import { formatRelativeTime } from "@/lib/time"
import { cn } from "@/lib/utils"
import { CredentialTierBadge } from "./credential-tier-badge"

export const VAULT_PAGE_SIZE = 50

export interface VaultSecret extends CredentialLike {
  name: string
  type: string
  security_level_label?: string | null
}

/** The service mark on a neutral tile — the one colourful thing in a row. */
export function BrandTile({ provider, size = 28 }: { provider: string; size?: 28 | 40 | 52 }) {
  const brand = getBrand(provider)
  const Icon = brand.Icon
  return (
    <span
      aria-hidden
      className={cn(
        "inline-grid shrink-0 place-items-center rounded-lg border border-border bg-muted",
        size === 28 ? "size-7" : size === 40 ? "size-10" : "size-[52px] rounded-xl",
      )}
    >
      <Icon className={size === 28 ? "size-4" : size === 40 ? "size-5" : "size-7"} style={{ color: brandColor(brand) }} />
    </span>
  )
}

function UsedBy({ c }: { c: VaultSecret }) {
  const ids = c.agent_ids ?? []
  const names = c.agent_names ?? []
  if (names.length === 0) return <span className="text-muted-foreground-soft">No one</span>
  return (
    <span className="inline-flex items-center gap-1" title={names.join(", ")} aria-label={names.join(", ")}>
      {names.slice(0, 3).map((n, i) => (
        <AgentAvatar key={`${ids[i] ?? n}-${i}`} seed={ids[i] ?? n} alt="" className="size-5 shrink-0 rounded-full" />
      ))}
      {names.length > 3 && <span className="font-mono text-micro text-muted-foreground">+{names.length - 3}</span>}
    </span>
  )
}

function Row({
  c,
  now,
  selectable,
  selected,
  onToggle,
  onOpen,
}: {
  c: VaultSecret
  now: number
  selectable: boolean
  selected: boolean
  onToggle: (id: string) => void
  onOpen: (id: string) => void
}) {
  const state = deriveSecretState(c, now)
  const cls = classificationOf(c)
  return (
    <tr
      data-row-id={c.id}
      data-selected={selected || undefined}
      onClick={() => onOpen(c.id)}
      className="cursor-pointer border-b border-border transition-colors last:border-b-0 hover:bg-muted/60 data-[selected=true]:bg-primary/[0.06]"
    >
      {selectable && (
        <td className="w-9 py-2 pl-3 pr-0 align-middle" onClick={(e) => e.stopPropagation()}>
          <Checkbox checked={selected} onCheckedChange={() => onToggle(c.id)} aria-label={`Select ${c.name}`} />
        </td>
      )}
      <td className="min-w-0 py-2 pl-3 pr-2 align-middle">
        <span className="flex min-w-0 items-center gap-2.5">
          <BrandTile provider={c.provider} />
          <span className="grid min-w-0 gap-px leading-tight">
            <button
              type="button"
              onClick={(e) => { e.stopPropagation(); onOpen(c.id) }}
              className="truncate text-left text-control font-medium text-foreground hover:underline focus-visible:outline-2 focus-visible:outline-ring"
            >
              {c.name}
            </button>
            <small className="truncate text-label text-muted-foreground">
              {credentialTypeLabel(c.type)} · {getBrand(c.provider).label}
            </small>
          </span>
        </span>
      </td>
      <td className="hidden py-2 pr-2 align-middle text-label lg:table-cell"><UsedBy c={c} /></td>
      <td className="hidden whitespace-nowrap py-2 pr-2 align-middle font-mono text-label text-muted-foreground md:table-cell">
        {c.last_used_at ? formatRelativeTime(c.last_used_at) : "never"}
      </td>
      <td className="hidden whitespace-nowrap py-2 pr-2 align-middle md:table-cell">
        <span className="inline-flex items-center gap-1">
          <CredentialTierBadge level={tierOf(c)} serverLabel={c.security_level_label} />
          {cls !== "STANDARD" && (
            <span className="rounded border border-border px-1 text-micro font-semibold text-muted-foreground">{cls.toLowerCase()}</span>
          )}
        </span>
      </td>
      <td className="whitespace-nowrap py-2 pl-1 pr-3 text-right align-middle md:text-left">
        <StatusPill tone={state.tone} label={state.label} />
      </td>
    </tr>
  )
}

function Table({
  rows,
  now,
  selectable,
  selectedIds,
  onToggle,
  onToggleAll,
  onOpen,
  caption,
}: {
  rows: VaultSecret[]
  now: number
  selectable: boolean
  selectedIds: ReadonlySet<string>
  onToggle: (id: string) => void
  onToggleAll: (ids: string[], on: boolean) => void
  onOpen: (id: string) => void
  caption: string
}) {
  const all = rows.length > 0 && rows.every((r) => selectedIds.has(r.id))
  const some = !all && rows.some((r) => selectedIds.has(r.id))
  return (
    <div className="overflow-hidden rounded-card border border-border bg-card">
      <table className="w-full table-fixed text-left md:table-auto" aria-label={caption}>
        <thead className="hidden border-b border-border bg-muted/50 text-label text-muted-foreground md:table-header-group">
          <tr className="h-[34px]">
            {selectable && (
              <th className="w-9 pl-3 pr-0 font-normal">
                <Checkbox
                  checked={all ? true : some ? "indeterminate" : false}
                  onCheckedChange={() => onToggleAll(rows.map((r) => r.id), !all)}
                  aria-label={all ? "Clear the selection" : "Select every row shown"}
                />
              </th>
            )}
            <th className="pl-3 pr-2 font-normal">Secret</th>
            <th className="hidden pr-2 font-normal lg:table-cell">Used by</th>
            <th className="pr-2 font-normal">Last used</th>
            <th className="pr-2 font-normal">Tier</th>
            <th className="pr-3 font-normal">State</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((c) => (
            <Row key={c.id} c={c} now={now} selectable={selectable} selected={selectedIds.has(c.id)} onToggle={onToggle} onOpen={onOpen} />
          ))}
        </tbody>
      </table>
    </div>
  )
}

export function SecretsTable({
  rows,
  group,
  onGroupChange,
  selectable,
  selectedIds,
  onToggle,
  onToggleAll,
  onOpen,
  now = Date.now(),
}: {
  rows: VaultSecret[]
  group: VaultGroup
  onGroupChange: (g: VaultGroup) => void
  selectable: boolean
  selectedIds: ReadonlySet<string>
  onToggle: (id: string) => void
  onToggleAll: (ids: string[], on: boolean) => void
  onOpen: (id: string) => void
  now?: number
}) {
  const [limit, setLimit] = React.useState(VAULT_PAGE_SIZE)
  // A new filter is a new list: start from the top again.
  const key = rows.map((r) => r.id).join(",")
  React.useEffect(() => setLimit(VAULT_PAGE_SIZE), [key])

  const groups = React.useMemo(() => {
    if (group !== "type") return null
    const by = new Map<string, VaultSecret[]>()
    for (const r of rows) {
      const k = credentialTypeLabel(r.type)
      by.set(k, [...(by.get(k) ?? []), r])
    }
    return [...by.entries()].sort((a, b) => b[1].length - a[1].length || a[0].localeCompare(b[0]))
  }, [rows, group])

  const shared = { now, selectable, selectedIds, onToggle, onToggleAll, onOpen }

  return (
    <div className="flex flex-col gap-3" data-slot="secrets-table">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-label text-muted-foreground" aria-live="polite">
          {rows.length} secret{rows.length === 1 ? "" : "s"}
        </span>
        <span className="flex-1" />
        <span className="text-label text-muted-foreground" id="vault-group-label">Group</span>
        <div role="radiogroup" aria-labelledby="vault-group-label" className="inline-flex rounded-md border border-border bg-muted/40 p-0.5">
          {(["none", "type"] as const).map((g) => (
            <button
              key={g}
              type="button"
              role="radio"
              aria-checked={group === g}
              onClick={() => onGroupChange(g)}
              className={cn(
                "kit-tap h-6 rounded px-2 text-label transition-colors",
                group === g ? "bg-card font-medium text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground",
              )}
            >
              {g === "none" ? "None" : "Type"}
            </button>
          ))}
        </div>
      </div>

      {groups ? (
        groups.map(([label, items]) => <GroupCard key={label} label={label} items={items} {...shared} />)
      ) : (
        <>
          <Table rows={rows.slice(0, limit)} caption="Secrets" {...shared} />
          <div className="flex items-center gap-3 px-1">
            {rows.length > limit && (
              <Button variant="outline" size="sm" onClick={() => setLimit((n) => n + VAULT_PAGE_SIZE)}>
                <ChevronDown className="size-3.5" aria-hidden />
                Load 50 more
              </Button>
            )}
            <span className="text-label text-muted-foreground">
              Showing {Math.min(limit, rows.length)} of {rows.length}
            </span>
          </div>
        </>
      )}
    </div>
  )
}

function GroupCard({
  label,
  items,
  ...shared
}: {
  label: string
  items: VaultSecret[]
  now: number
  selectable: boolean
  selectedIds: ReadonlySet<string>
  onToggle: (id: string) => void
  onToggleAll: (ids: string[], on: boolean) => void
  onOpen: (id: string) => void
}) {
  const [open, setOpen] = React.useState(false)
  const shown = open ? items : items.slice(0, 5)
  return (
    <section aria-label={`${label} · ${items.length}`} className="flex flex-col gap-1.5">
      <h3 className="px-1 text-label font-semibold text-foreground">
        {label} <span className="font-mono font-normal text-muted-foreground">· {items.length}</span>
      </h3>
      <Table rows={shown} caption={label} {...shared} />
      {items.length > 5 && !open && (
        <button type="button" onClick={() => setOpen(true)} className="kit-tap self-start px-1 text-label text-primary hover:underline">
          Show all {items.length} ›
        </button>
      )}
    </section>
  )
}
