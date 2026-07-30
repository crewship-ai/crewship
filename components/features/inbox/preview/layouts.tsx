"use client"

import { useState } from "react"
import { ChevronDown, ChevronRight, X } from "lucide-react"

import { Appear, DetailCard, Pill } from "@/components/ui/detail"
import { ListRow } from "@/components/ui/list-row"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

import { ActorAvatar, ActorLabel } from "./actor"
import { canRole, type PreviewInboxItem, type WorkspaceRole } from "./mock-data"
import { DecisionCard, ItemDetail, RunProgress } from "./item-detail"
import {
  OUTCOME_LABEL, OUTCOME_TONE, absolute, bucketOf, categoryOf, decisionFor, durationLabel,
  expiresIn, resolverOf, since, subjectOf,
} from "./logic"
import type { Bucket } from "./types"

// =============================================================================
// Three arrangements of the same data, so the choice can be made by looking
// rather than by arguing. The rail is identical in all of them and none of
// them has a second sub-bar: the page identity is the rail's job here.
//
//   Split  — a mail client, committed to properly: the list column carries the
//            rail's surface so the reading pane is the only thing on the page
//            background, and the seam between them is one hairline.
//   Table  — the /routines catalog: one dense row per item, decision state as
//            a column you can scan down, detail in a drawer over the table.
//   Stream — detail-kit cards grouped by bucket, each carrying its own
//            decision. No reading pane at all; nothing is one click away.
// =============================================================================

const BUCKET_LABEL: Record<Bucket, string> = {
  decisions: "Needs a decision",
  replies: "Agent replies",
  review: "Ready for review",
  routines: "Routine progress",
  other: "Everything else",
}

interface LayoutProps {
  rows: PreviewInboxItem[]
  total: number
  role: WorkspaceRole
  selectedId: string | null
  onSelect: (id: string) => void
}

/* ------------------------------------------------------------------ split */

export function SplitLayout({ rows, total, role, selectedId, onSelect }: LayoutProps) {
  const selected = rows.find((r) => r.id === selectedId) ?? rows[0] ?? null

  return (
    <div className="flex min-w-0 flex-1 overflow-hidden">
      {/* The list column sits on bg-card — the rail's surface — so the page
          reads as one navigation body and one reading surface, instead of
          three columns of three different greys. */}
      <div className="flex w-[400px] shrink-0 flex-col overflow-hidden border-r border-white/[0.06] bg-card">
        <div className="flex shrink-0 items-center gap-2 border-b border-white/[0.06] px-4 py-2">
          <span className="type-section text-foreground/70">Inbox</span>
          <span className="type-meta font-mono text-muted-foreground-soft">
            {rows.length === total ? total : `${rows.length} of ${total}`}
          </span>
          <button type="button" className="type-meta ml-auto text-muted-foreground hover:text-foreground">
            Newest
          </button>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto">
          {rows.length === 0 && (
            <p className="type-row px-4 py-10 text-center text-muted-foreground-soft">Nothing here.</p>
          )}
          <ul>
            {rows.map((item) => (
              <MailRow
                key={item.id}
                item={item}
                role={role}
                selected={selected?.id === item.id}
                onSelect={() => onSelect(item.id)}
              />
            ))}
          </ul>
        </div>
      </div>

      <div className="min-w-0 flex-1 overflow-y-auto bg-background p-4">
        {selected ? (
          <ItemDetail key={selected.id} item={selected} role={role} />
        ) : (
          <p className="type-row px-4 py-10 text-center text-muted-foreground-soft">Pick an item.</p>
        )}
      </div>
    </div>
  )
}

/**
 * One line of title, one line of everything else. The previous pass gave each
 * row a pill row of its own, which is what made ten items look like ten cards.
 */
function MailRow({
  item, role, selected, onSelect,
}: {
  item: PreviewInboxItem
  role: WorkspaceRole
  selected: boolean
  onSelect: () => void
}) {
  const spec = decisionFor(item)
  const blocked = spec != null && !canRole(role, spec.requires)
  const mins = expiresIn(item)
  const subject = subjectOf(item)

  return (
    <ListRow selected={selected} onSelect={onSelect} className="items-start gap-2.5 px-4 py-2">
      <span
        className={cn(
          "mt-2 h-1.5 w-1.5 shrink-0 rounded-full",
          item.state === "unread" ? "bg-primary" : "bg-transparent",
        )}
      />
      <ActorAvatar actor={subject} size={24} />
      <span className="min-w-0 flex-1">
        <span className="flex items-baseline gap-2">
          <span
            className={cn(
              "type-row min-w-0 flex-1 truncate",
              item.state === "unread" ? "font-medium text-foreground" : "text-muted-foreground",
            )}
          >
            {item.title}
          </span>
          <span className="type-meta shrink-0 text-muted-foreground-soft">{since(item.created_at)}</span>
        </span>
        <span className="type-meta flex min-w-0 items-center gap-1.5 text-muted-foreground-soft">
          <span className="truncate">{subject.label}</span>
          <span>·</span>
          <span className="truncate font-mono">{categoryOf(item)}</span>
          {mins != null && mins > 0 && (
            <span className="shrink-0 font-medium text-destructive">· expires in {mins}m</span>
          )}
          {blocked && <span className="shrink-0">· admin decides</span>}
        </span>
      </span>
    </ListRow>
  )
}

/* ------------------------------------------------------------------ table */

export function TableLayout({ rows, total, role, selectedId, onSelect }: LayoutProps) {
  const [open, setOpen] = useState(false)
  const selected = rows.find((r) => r.id === selectedId) ?? null

  return (
    <div className="relative flex min-w-0 flex-1 flex-col overflow-hidden bg-background">
      <div className="flex shrink-0 items-center gap-2 border-b border-hairline px-5 py-2.5">
        <span className="text-body font-medium">Inbox</span>
        <span className="type-meta text-muted-foreground-soft">
          {rows.length === total ? `${total} items` : `${rows.length} of ${total}`}
        </span>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        <table className="w-full">
          <thead className="sticky top-0 z-[1] bg-background">
            <tr className="border-b border-hairline">
              {["From", "Item", "Category", "Waiting", "Decision"].map((h) => (
                <th
                  key={h}
                  className="type-meta px-5 py-2 text-left font-medium uppercase tracking-wide text-muted-foreground-soft"
                >
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((item) => {
              const spec = decisionFor(item)
              const allowed = spec != null && canRole(role, spec.requires)
              const mins = expiresIn(item)
              return (
                <tr
                  key={item.id}
                  data-row
                  onClick={() => { onSelect(item.id); setOpen(true) }}
                  className={cn(
                    "cursor-pointer border-b border-hairline/60 transition-colors hover:bg-white/[0.02]",
                    selectedId === item.id && open && "bg-primary/[0.07]",
                  )}
                >
                  <td className="px-5 py-2.5">
                    <ActorLabel actor={subjectOf(item)} size={24} />
                  </td>
                  <td className="px-5 py-2.5">
                    <div className="flex items-center gap-2">
                      {item.state === "unread" && <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-primary" />}
                      <span className={cn("type-row", item.state === "unread" && "font-medium")}>{item.title}</span>
                    </div>
                  </td>
                  <td className="type-meta px-5 py-2.5 font-mono text-muted-foreground-soft">
                    {categoryOf(item)}
                  </td>
                  <td className="type-meta px-5 py-2.5 font-mono tabular-nums text-muted-foreground-soft">
                    {mins != null && mins > 0
                      ? <span className="text-destructive">expires in {mins}m</span>
                      : since(item.created_at)}
                  </td>
                  <td className="px-5 py-2.5">
                    {spec == null ? (
                      <span className="type-meta text-muted-foreground-soft">—</span>
                    ) : allowed ? (
                      <Pill tone="warn">yours to decide</Pill>
                    ) : (
                      <Pill tone="default">admin decides</Pill>
                    )}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
        {rows.length === 0 && (
          <p className="type-row px-5 py-10 text-center text-muted-foreground-soft">Nothing here.</p>
        )}
      </div>

      {/* Drawer over the table rather than a permanent third column: the table
          is the surface being read, and a pane that is empty most of the time
          is a column of nothing. */}
      {open && selected && (
        <div className="absolute inset-y-0 right-0 z-10 flex w-[560px] flex-col border-l border-white/[0.08] bg-card shadow-2xl">
          <div className="flex shrink-0 items-center gap-2 border-b border-hairline px-4 py-2.5">
            <ActorLabel actor={subjectOf(selected)} size={24} showKind />
            <Button
              size="icon-sm"
              variant="ghost"
              className="ml-auto"
              aria-label="Close"
              onClick={() => setOpen(false)}
            >
              <X className="h-4 w-4" />
            </Button>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto p-4">
            <ItemDetail item={selected} role={role} />
          </div>
        </div>
      )}
    </div>
  )
}

/* ----------------------------------------------------------------- stream */

export function StreamLayout({ rows, role }: LayoutProps) {
  const groups = new Map<Bucket, PreviewInboxItem[]>()
  for (const item of rows) {
    const b = bucketOf(item)
    const list = groups.get(b)
    if (list) list.push(item)
    else groups.set(b, [item])
  }

  return (
    <div className="min-w-0 flex-1 overflow-y-auto bg-background p-5">
      <div className="mx-auto flex max-w-[880px] flex-col gap-6">
        {[...groups.entries()].map(([bucket, items]) => (
          <section key={bucket}>
            <div className="mb-2 flex items-baseline gap-2">
              <h2 className="type-section text-foreground/70">{BUCKET_LABEL[bucket]}</h2>
              <span className="type-meta font-mono text-muted-foreground-soft">{items.length}</span>
            </div>
            <div className="flex flex-col gap-3">
              {items.map((item, i) => (
                <Appear key={item.id} order={i}>
                  <StreamCard item={item} role={role} />
                </Appear>
              ))}
            </div>
          </section>
        ))}
        {rows.length === 0 && (
          <p className="type-row py-10 text-center text-muted-foreground-soft">Nothing here.</p>
        )}
      </div>
    </div>
  )
}

function StreamCard({ item, role }: { item: PreviewInboxItem; role: WorkspaceRole }) {
  const [expanded, setExpanded] = useState(false)
  const spec = decisionFor(item)
  const subject = subjectOf(item)

  return (
    <DetailCard bare className={cn(spec && "border-warn/25")}>
      <div className="flex items-start gap-3 px-4 py-3">
        <ActorAvatar actor={subject} size={32} />
        <div className="min-w-0 flex-1">
          <div className="flex items-baseline gap-2">
            <span className="type-row font-medium">{subject.label}</span>
            {subject.kind !== "agent" && (
              <span className="type-meta uppercase tracking-wide text-muted-foreground-soft">{subject.kind}</span>
            )}
            <span className="type-meta ml-auto shrink-0 font-mono text-muted-foreground-soft">
              {categoryOf(item)} · {since(item.created_at)}
            </span>
          </div>
          <p className="type-row mt-0.5 text-foreground">{item.title}</p>

          <button
            type="button"
            onClick={() => setExpanded((v) => !v)}
            className="type-meta mt-1.5 inline-flex items-center gap-1 text-muted-foreground hover:text-foreground"
          >
            {expanded ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
            {expanded ? "Hide details" : "Details"}
          </button>
        </div>
      </div>

      {spec && (
        <div className="border-t border-hairline px-4 py-3">
          <DecisionCard item={item} role={role} spec={spec} compact />
        </div>
      )}

      {expanded && (
        <div className="flex flex-col gap-3 border-t border-hairline bg-surface-subtle/40 px-4 py-3">
          <RunProgress item={item} />
          {item.body_md && (
            <p className="type-row whitespace-pre-wrap text-muted-foreground">{item.body_md}</p>
          )}
        </div>
      )}
    </DetailCard>
  )
}

/* ---------------------------------------------------------------- archive */

export function ArchiveTable({ rows, total }: { rows: PreviewInboxItem[]; total: number }) {
  return (
    <div className="min-w-0 flex-1 overflow-y-auto bg-background">
      <div className="flex items-center gap-2 border-b border-hairline px-5 py-2.5">
        <span className="text-body font-medium">Archive</span>
        <span className="type-meta text-muted-foreground-soft">
          {rows.length === total ? `${total} resolved items` : `${rows.length} of ${total}`}
        </span>
        <span className="type-meta ml-auto font-mono text-muted-foreground-soft">
          filters on the left · on the server this is SQL facets + a cursor
        </span>
      </div>

      <table className="w-full">
        <thead>
          <tr className="border-b border-hairline">
            {["Subject", "Item", "Outcome", "Decided by", "When", "Took", "Category"].map((h) => (
              <th
                key={h}
                className="type-meta px-5 py-2 text-left font-medium uppercase tracking-wide text-muted-foreground-soft"
              >
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((item) => {
            const action = item.resolved_action ?? ""
            const resolver = resolverOf(item)
            const took = item.resolved_at
              ? Math.round((Date.parse(item.resolved_at) - Date.parse(item.created_at)) / 60_000)
              : null
            return (
              <tr key={item.id} data-row className="border-b border-hairline/60 hover:bg-white/[0.02]">
                <td className="px-5 py-2.5"><ActorLabel actor={subjectOf(item)} size={24} /></td>
                <td className="type-row px-5 py-2.5">{item.title}</td>
                <td className="px-5 py-2.5">
                  <Pill tone={OUTCOME_TONE[action] ?? "default"}>{OUTCOME_LABEL[action] ?? action}</Pill>
                </td>
                <td className="px-5 py-2.5">
                  {resolver
                    ? <ActorLabel actor={resolver} size={20} />
                    : <span className="type-meta text-muted-foreground-soft">no one — it expired</span>}
                </td>
                <td className="type-meta px-5 py-2.5 font-mono text-muted-foreground-soft">
                  {absolute(item.resolved_at)}
                </td>
                <td className="type-meta px-5 py-2.5 font-mono tabular-nums text-muted-foreground-soft">
                  {durationLabel(took)}
                </td>
                <td className="type-meta px-5 py-2.5 font-mono text-muted-foreground-soft">{categoryOf(item)}</td>
              </tr>
            )
          })}
        </tbody>
      </table>

      {rows.length === 0 && (
        <p className="type-row px-5 py-10 text-center text-muted-foreground-soft">Nothing matches this filter.</p>
      )}
    </div>
  )
}
