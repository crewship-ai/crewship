"use client"

import { Pill } from "@/components/ui/detail"
import { ListRow } from "@/components/ui/list-row"
import { cn } from "@/lib/utils"

import { ActorAvatar, ActorLabel } from "./actor"
import { canRole, type PreviewInboxItem, type WorkspaceRole } from "./mock-data"
import { ItemDetail } from "./item-detail"
import {
  OUTCOME_LABEL, OUTCOME_TONE, absolute, categoryOf, decisionFor, durationLabel,
  expiresIn, resolverOf, since, subjectOf,
} from "./logic"

// =============================================================================
// The list and the reading pane.
//
// A table variant and a card-stream variant were tried here and dropped: the
// split is the one that fits the job, and keeping the losers around as dead
// code would leave three ways to render an approval in a directory whose whole
// point is that there is one.
//
// The list column sits on the rail's surface rather than a third grey, so the
// page reads as one navigation body and one reading surface.
// =============================================================================

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
