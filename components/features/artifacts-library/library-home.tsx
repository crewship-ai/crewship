"use client"

import { useState } from "react"
import { AlertTriangle, ChevronRight, Clock, LayoutGrid, Lock, Plus, RotateCcw, Table2 } from "lucide-react"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Segmented } from "@/components/ui/segmented"
import { StatusPill } from "@/components/ui/status-pill"
import { SubBarPrimary } from "@/components/layout/sub-bar"
import { formatRelativeShort } from "@/lib/time"
import { cn } from "@/lib/utils"
import type { ArtifactKindKey } from "@/components/features/chat/artifact-context/new-artifact/artifact-kinds"
import type { AttentionEntry, AttentionReason } from "./attention"
import { versioned } from "./library-filter"
import type { LibraryItem } from "./library-items"
import { KIND_ORDER, KINDS, kindOf, uiKindCounts, type ArtifactKind } from "./kinds"
import { KindBadge, KindTile } from "./kind-tile"
import { KindThumb } from "./kind-thumb"

/** Library Docs uses human collaborative creation, distinct from the Word Craft flow and agent artifact kinds. */
export type LibraryCreationFlow = ArtifactKindKey
export const CREATE_FLOW: Record<ArtifactKind, readonly LibraryCreationFlow[]> = {
  docs: ["docs"], dashboard: ["dashboard", "page"], slides: ["slides"], design: ["design"], app: ["project"], word: ["word"], sheet: ["sheet"],
}

export function creatableUiKinds(creatable: readonly ArtifactKindKey[], docsAvailable = false): { kind: ArtifactKind; flow: LibraryCreationFlow }[] {
  return KIND_ORDER.flatMap(kind => {
    const flow = CREATE_FLOW[kind].find(key => key === "docs" ? docsAvailable : creatable.includes(key))
    return flow ? [{ kind, flow }] : []
  })
}

/** New artifact: one popover grouped Artifact and Craft, each entry opening the existing New artifact dialog on its flow. */
export function NewArtifactMenu({ creatable, docsAvailable = false, onNew }: { creatable: readonly ArtifactKindKey[]; docsAvailable?: boolean; onNew: (flow: LibraryCreationFlow) => void }) {
  const [open, setOpen] = useState(false)
  const entries = creatableUiKinds(creatable, docsAvailable)
  if (!entries.length) return null
  const group = (family: "Artifact" | "Craft", caption: string) => {
    const list = entries.filter(entry => KINDS[entry.kind].family === family)
    return list.length > 0 && <section aria-label={caption} className="grid gap-1">
      <h3 className="px-2 pb-0.5 pt-1.5 text-xs font-semibold text-muted-foreground">{caption}</h3>
      <div className="grid gap-1 sm:grid-cols-2">
        {list.map(({ kind, flow }) => <button key={kind} type="button" aria-label={`New ${KINDS[kind].label}`}
          onClick={() => { setOpen(false); onNew(flow) }}
          className="flex items-center gap-2.5 rounded-lg p-2 text-left transition-colors hover:bg-accent coarse:min-h-[44px]">
          <KindTile kind={kind} />
          <span className="grid min-w-0 gap-0.5">
            <b className="text-sm font-semibold">{KINDS[kind].label}</b>
            <small className="text-xs text-muted-foreground">{KINDS[kind].description}</small>
          </span>
        </button>)}
      </div>
    </section>
  }
  return <Popover open={open} onOpenChange={setOpen}>
    <PopoverTrigger asChild>
      <SubBarPrimary icon={Plus} aria-label="New artifact">New artifact</SubBarPrimary>
    </PopoverTrigger>
    <PopoverContent align="end" className="w-[min(560px,calc(100vw-2rem))] p-2" aria-label="New artifact">
      {group("Artifact", "Artifacts")}
      {group("Craft", "Craft · one editor at a time")}
    </PopoverContent>
  </Popover>
}

const ATTENTION_ICON: Record<AttentionReason, { icon: typeof Clock; tone: string }> = {
  draft: { icon: RotateCcw, tone: "bg-warn/12 text-warn" },
  failed: { icon: AlertTriangle, tone: "bg-warn/12 text-warn" },
  stale: { icon: Clock, tone: "bg-warn/12 text-warn" },
}

const meta = (item: LibraryItem) => [item.ownerLabel, item.updatedAt ? formatRelativeShort(item.updatedAt.toISOString()) : null].filter(Boolean).join(" · ")

function StateChips({ item }: { item: LibraryItem }) {
  return <>
    {versioned(item) && (item.sharedVersion
      ? <StatusPill tone="success" label={`Shared v${item.sharedVersion}`} />
      : <StatusPill tone="muted" label="Not shared" />)}
    {item.private && <StatusPill tone="muted" label="Private" icon={<Lock aria-hidden className="size-2.5" />} />}
  </>
}

function Card({ item, onOpen, peeked }: { item: LibraryItem; onOpen: () => void; peeked?: boolean }) {
  const resolved = kindOf(item)
  return <li className="min-w-0">
    <button type="button" aria-label={`Open ${item.name}`} onClick={onOpen} data-library-item={item.slug} aria-current={peeked || undefined}
      className={cn("grid h-full w-full min-w-0 content-start gap-2 rounded-xl border bg-card p-2 pb-2.5 text-left transition-[border-color,box-shadow] hover:border-foreground/20 hover:shadow-md focus-visible:outline-2 focus-visible:outline-primary",
        peeked && "border-primary/60 ring-1 ring-primary/40")}>
      <KindThumb kind={resolved.kind} mode={resolved.mode} empty={item.empty} className="h-[110px]" />
      <span className="flex min-w-0 items-center gap-2 px-0.5">
        <KindTile kind={resolved.kind} size="sm" />
        <h3 className="truncate text-sm font-semibold">{item.name}</h3>
      </span>
      <span className="flex min-w-0 items-center gap-2 px-0.5 text-xs text-muted-foreground">
        <KindBadge resolved={resolved} />
        <span className="truncate">{meta(item)}</span>
      </span>
    </button>
  </li>
}

function Table({ items, onOpen, peeked }: { items: LibraryItem[]; onOpen: (slug: string) => void; peeked?: string | null }) {
  return <div className="overflow-x-auto rounded-card border bg-card">
    <table className="w-full text-sm">
      <thead className="bg-muted/50 text-left text-xs text-muted-foreground">
        <tr><th className="px-3 py-2 font-medium">Name</th><th className="px-3 py-2 font-medium">Kind</th><th className="px-3 py-2 font-medium">Version</th><th className="px-3 py-2 font-medium">Status</th><th className="px-3 py-2 font-medium">Owner</th><th className="px-3 py-2 font-medium">Updated</th></tr>
      </thead>
      <tbody>
        {items.map(item => {
          const resolved = kindOf(item)
          return <tr key={item.slug} className={cn("border-t", peeked === item.slug && "bg-accent")}>
            <td className="max-w-[320px] px-3 py-2">
              <button type="button" data-library-item={item.slug} aria-label={`Open ${item.name}`} onClick={() => onOpen(item.slug)}
                className="flex min-w-0 items-center gap-2 font-medium hover:underline">
                <KindTile kind={resolved.kind} size="sm" /><span className="truncate">{item.name}</span>
              </button>
            </td>
            <td className="px-3 py-2"><KindBadge resolved={resolved} /></td>
            <td className="px-3 py-2 font-mono text-xs">{item.sharedVersion ? `v${item.sharedVersion}` : "—"}</td>
            <td className="px-3 py-2"><span className="flex flex-wrap gap-1"><StateChips item={item} /></span></td>
            <td className="max-w-[180px] truncate px-3 py-2 text-xs text-muted-foreground">{item.ownerLabel ?? "—"}</td>
            <td className="whitespace-nowrap px-3 py-2 text-xs text-muted-foreground">{item.updatedAt ? formatRelativeShort(item.updatedAt.toISOString()) : "—"}</td>
          </tr>
        })}
      </tbody>
    </table>
  </div>
}

type HomeLayout = "grid" | "table"

/**
 * Home (Harbor 2 v87): Needs attention (the fixed rule in attention.ts), then
 * every artifact in view with kind chips and a grid or table. What can be made
 * lives in the New artifact popover in the bar.
 */
export function LibraryHome({ items, attention, title, onOpen, peeked }: {
  items: LibraryItem[]
  attention: AttentionEntry[]
  /** The folder in view, or All artifacts. */
  title: string
  onOpen: (slug: string) => void
  peeked?: string | null
}) {
  const [kind, setKind] = useState<ArtifactKind | "all">("all")
  const [layout, setLayout] = useState<HomeLayout>("grid")
  const counts = uiKindCounts(items)
  const shown = kind === "all" ? items : items.filter(item => kindOf(item).kind === kind)
  const chip = (value: ArtifactKind | "all", label: React.ReactNode, count: number) =>
    <button key={value} type="button" aria-pressed={kind === value} onClick={() => setKind(value)}
      className={cn("inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-xs font-medium transition-colors coarse:min-h-[44px]",
        kind === value ? "border-primary/40 bg-primary/12 text-primary-hover" : "bg-card hover:bg-accent")}>
      {label}<span className="font-mono text-muted-foreground">{count}</span>
    </button>
  return <div className="grid content-start gap-6">
    <h1 className="sr-only">Artifacts</h1>
    {attention.length > 0 && <section aria-labelledby="attention-heading" className="grid gap-2.5">
      <div>
        <h2 id="attention-heading" className="flex items-baseline gap-2 text-base font-semibold">Needs attention <span className="font-mono text-sm font-normal text-muted-foreground">{attention.length}</span></h2>
        <p className="text-xs text-muted-foreground">Your recovered drafts, failed refreshes and stale data you own. Not “unshared”.</p>
      </div>
      <ul className="grid grid-cols-[repeat(auto-fill,minmax(min(100%,250px),1fr))] gap-2.5">
        {attention.map(entry => {
          const look = ATTENTION_ICON[entry.reason]
          const Icon = look.icon
          return <li key={entry.item.slug} className="min-w-0">
            <button type="button" aria-label={`${entry.title}: ${entry.item.name}`} onClick={() => onOpen(entry.item.slug)} data-attention={entry.reason}
              className="flex w-full min-w-0 items-center gap-3 rounded-card border bg-card px-3 py-2.5 text-left transition-colors hover:border-foreground/20 coarse:min-h-[44px]">
              <span aria-hidden className={cn("grid size-8 shrink-0 place-items-center rounded-md", look.tone)}><Icon className="size-4" /></span>
              <span className="grid min-w-0 flex-1">
                <b className="text-sm font-semibold">{entry.title}</b>
                <small className="truncate text-xs text-muted-foreground">{entry.detail}</small>
              </span>
              <ChevronRight aria-hidden className="size-3.5 shrink-0 text-muted-foreground" />
            </button>
          </li>
        })}
      </ul>
    </section>}
    <section aria-labelledby="home-list-heading" className="grid gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <h2 id="home-list-heading" className="flex items-baseline gap-2 text-base font-semibold">{title} <span className="font-mono text-sm font-normal text-muted-foreground">{items.length}</span></h2>
        <Segmented<HomeLayout> ariaLabel="View" value={layout} onChange={setLayout} className="ml-auto" options={[
          { value: "grid", label: "Grid", icon: LayoutGrid, ariaLabel: "Grid" },
          { value: "table", label: "Table", icon: Table2, ariaLabel: "Table" },
        ]} />
      </div>
      {counts.length > 1 && <div role="group" aria-label="Kind" className="flex flex-wrap gap-1.5">
        {chip("all", "All", items.length)}
        {counts.map(([value, count]) => chip(value, <><KindTile kind={value} size="sm" className="size-4 border-0 bg-transparent" />{KINDS[value].plural}</>, count))}
      </div>}
      {layout === "table"
        ? <Table items={shown} onOpen={onOpen} peeked={peeked} />
        : <ul aria-label="Artifacts" className="grid grid-cols-[repeat(auto-fill,minmax(min(100%,220px),1fr))] gap-3.5">
          {shown.map(item => <Card key={item.slug} item={item} onOpen={() => onOpen(item.slug)} peeked={peeked === item.slug} />)}
        </ul>}
    </section>
  </div>
}
