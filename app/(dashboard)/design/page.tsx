"use client"

import { useState } from "react"
import { Search } from "lucide-react"

import { DetailCard } from "@/components/ui/detail"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { cn } from "@/lib/utils"

// =============================================================================
// /design — a live wireframe bench, not a product screen.
//
// It exists because layout arguments cannot be settled in prose. Every variant
// renders with the real kit (components/ui/detail), the real type roles and the
// real icon map, so whatever gets picked here is already implementable — there
// is no translation step between the mock and the screen.
//
// Deliberately unlinked from the sidebar: reachable by typing /design, invisible
// to anyone not looking for it. Delete before this branch merges.
//
// ── the question on the bench right now ──────────────────────────────────────
//
// The overview has 6 cards AND a row of 7 chips, each chip opening a drawer from
// the right. The drawers are not one thing: three hold a list (literally the
// same DetailCell the grid uses, 420px) and four hold an entire tab component
// (760px). That is the width inconsistency — a symptom, not a styling slip.
//
// Of the seven chips:
//   Skills / Tools / Channels   lists. They belong in the grid.
//   Manage skills               the same concept as Skills.
//   Workspace                   the header already has a Files button.
//   Activity                    the header already has a Journal link.
//   Memory                      persona + crew settings — that is Configuration.
//
// So six of the seven carry nothing of their own.
// =============================================================================

type CellSpec = {
  key: keyof typeof CONCEPT_ICON
  title: string
  count: string
  filters: string[]
  rows: [string, string][]
  footer: string
  empty?: string
}

const HOLDS: CellSpec[] = [
  {
    key: "issues", title: "Issues", count: "2", filters: ["All", "Running", "Todo", "Done"],
    rows: [["ENG-2 Rewrite the Harborlight README", "backlog · medium"],
           ["ENG-3 Create a directory tree", "backlog · high"]],
    footer: "Open filtered by casey",
  },
  {
    key: "routines", title: "Routines", count: "0", filters: ["All"], rows: [],
    footer: "Open routines", empty: "Nothing matches this filter.",
  },
  {
    key: "triggers", title: "Triggers", count: "1", filters: ["All", "Automatic", "Manual"],
    rows: [["Manually from chat or CLI", "crewship agent run casey"]],
    footer: "Configure triggers",
  },
  {
    key: "credentials", title: "Credentials", count: "3", filters: ["All", "Active", "Pending"],
    rows: [["CLAUDE_CODE_OAUTH_TOKEN", "anthropic"], ["GH_TOKEN", "github"], ["github-acme", "github"]],
    footer: "Open vault",
  },
]

const CAN_DO: CellSpec[] = [
  {
    key: "skills", title: "Skills", count: "3 / 3", filters: ["All", "Enabled", "Disabled"],
    rows: [["incident-triage", "enabled"], ["pr-review", "enabled"], ["script-runner", "enabled"]],
    footer: "Manage skills",
  },
  {
    key: "tools", title: "Tools", count: "0", filters: ["All"], rows: [],
    footer: "Manage connectors", empty: "No connector bound.",
  },
  {
    key: "channels", title: "Channels", count: "0", filters: ["All", "Active"], rows: [],
    footer: "Manage channels", empty: "Nothing to report to.",
  },
]

const DID: CellSpec[] = [
  {
    key: "runs", title: "Runs", count: "1", filters: ["All", "Errors only", "Running"],
    rows: [["USER run", "completed"]],
    footer: "Open in Journal",
  },
  {
    key: "sessions", title: "Sessions", count: "7", filters: ["All"],
    rows: [["Pipeline pln_cms3qu7ij · step extract", "2 messages"],
           ["Pipeline pln_cms3qu7ij · step extract", "0 messages"]],
    footer: "Open chat",
  },
]

function Cell({ spec }: { spec: CellSpec }) {
  const Icon = CONCEPT_ICON[spec.key]
  return (
    <DetailCard
      bare
      icon={Icon}
      title={spec.title}
      subtitle={spec.count}
      className="flex w-full flex-col"
      action={
        <span className="grid h-6 w-6 place-items-center rounded-md text-muted-foreground-soft">
          <Search className="h-3.5 w-3.5" />
        </span>
      }
    >
      <div className="flex gap-1 border-b border-hairline px-4 py-2">
        {spec.filters.map((f, i) => (
          <span
            key={f}
            className={cn(
              "type-meta shrink-0 rounded-full px-2.5 py-1 font-medium",
              i === 0 ? "bg-primary/20 text-primary" : "text-muted-foreground",
            )}
          >
            {f}
          </span>
        ))}
      </div>
      <div className="min-h-[92px] divide-y divide-border/40">
        {spec.rows.length === 0 ? (
          <p className="type-row px-4 py-6 text-center text-muted-foreground-soft">{spec.empty}</p>
        ) : (
          spec.rows.map(([title, sub], i) => (
            <div key={i} className="flex items-start gap-2.5 px-4 py-2">
              <span className="mt-px grid h-5 w-5 shrink-0 place-items-center rounded-md bg-muted text-muted-foreground">
                <Icon className="h-3 w-3" />
              </span>
              <span className="min-w-0 flex-1">
                <span className="type-row block truncate text-foreground">{title}</span>
                <span className="type-meta block truncate font-mono text-muted-foreground">{sub}</span>
              </span>
            </div>
          ))
        )}
      </div>
      <div className="type-meta mt-auto flex items-center gap-2 border-t border-hairline px-4 py-2 text-muted-foreground-soft">
        <span>{spec.rows.length} items</span>
        <span className="ml-auto text-primary">{spec.footer} ↗</span>
      </div>
    </DetailCard>
  )
}

function RowLabel({ children, note }: { children: string; note: string }) {
  return (
    <div className="mb-2 flex items-baseline gap-2">
      <span className="type-section text-foreground/70">{children}</span>
      <span className="type-meta text-muted-foreground-soft">{note}</span>
    </div>
  )
}

/* ── Variant A — three rows, no chips, no drawer ─────────────────────────── */
function VariantA({ labelled }: { labelled: boolean }) {
  return (
    <div className="space-y-5">
      <div>
        {labelled && <RowLabel note="the work it is carrying">What it holds</RowLabel>}
        <div className="grid gap-3.5 @xl:grid-cols-2 @6xl:grid-cols-4">
          {HOLDS.map((c) => <Cell key={c.key} spec={c} />)}
        </div>
      </div>
      <div>
        {labelled && <RowLabel note="its abilities and where it reports">What it can do</RowLabel>}
        <div className="grid gap-3.5 @xl:grid-cols-2 @6xl:grid-cols-3">
          {CAN_DO.map((c) => <Cell key={c.key} spec={c} />)}
        </div>
      </div>
      <div>
        {labelled && <RowLabel note="on its own, and with you">What it has been up to</RowLabel>}
        <div className="grid gap-3.5 @xl:grid-cols-2">
          {DID.map((c) => <Cell key={c.key} spec={c} />)}
        </div>
      </div>
    </div>
  )
}

/* ── Variant B — one flat grid ───────────────────────────────────────────── */
function VariantB() {
  return (
    <div className="grid gap-3.5 @xl:grid-cols-2 @6xl:grid-cols-3 @9xl:grid-cols-4">
      {[...HOLDS, ...CAN_DO, ...DID].map((c) => <Cell key={c.key} spec={c} />)}
    </div>
  )
}

const VARIANTS = [
  {
    id: "A1",
    title: "A · three rows, labelled",
    blurb:
      "Every card visible, grouped by the question it answers, and the group says so out loud. " +
      "No chip row and no drawer: Manage skills becomes the Skills card's footer link, Workspace " +
      "is the Files button already in the header, Activity is the Journal link already in the " +
      "header, and Memory moves to Configuration, where the memory switch already lives.",
    render: () => <VariantA labelled />,
  },
  {
    id: "A2",
    title: "A · three rows, unlabelled",
    blurb:
      "The same structure with the group headings removed — the grouping carried by the row breaks " +
      "alone. Quieter; costs you the one line that explains why those three sit together.",
    render: () => <VariantA labelled={false} />,
  },
  {
    id: "B",
    title: "B · one flat grid",
    blurb:
      "All nine cards in a single flowing grid. Simplest rule, and the honest downside: Credentials " +
      "ends up beside Runs for no reason but the arithmetic. This is close to what the screen does " +
      "today, with the hidden cards made visible.",
    render: () => <VariantB />,
  },
]

export default function DesignBench() {
  const [variant, setVariant] = useState(VARIANTS[0].id)
  const active = VARIANTS.find((v) => v.id === variant) ?? VARIANTS[0]

  return (
    <div className="@container min-h-screen space-y-6 px-6 py-6 md:px-8 lg:px-12">
      <div className="rounded-xl border border-warn/30 bg-warn/[.06] px-4 py-3">
        <p className="type-row text-warn">
          Wireframe bench — not a product screen. Delete this route before the branch merges.
        </p>
        <p className="type-meta mt-1 text-muted-foreground">
          Renders with the real kit, type roles and icon map, so whatever is chosen here is already
          implementable.
        </p>
      </div>

      <div>
        <h1 className="type-title">Agent overview — where the chips should go</h1>
        <p className="type-row mt-2 max-w-3xl text-muted-foreground">
          The chip row opens a drawer from the right, and the drawers are two different things
          wearing the same clothes: three hold a list — the same card the grid already uses, 420px —
          and four hold an entire tab component, 760px. That is the width inconsistency. It is a
          symptom, not a styling slip.
        </p>
        <p className="type-row mt-2 max-w-3xl text-muted-foreground">
          Six of the seven chips carry nothing of their own: three are lists that belong in the
          grid, <b className="font-medium text-foreground">Manage skills</b> is the same concept as
          Skills, <b className="font-medium text-foreground">Workspace</b> is the header&rsquo;s
          Files button, <b className="font-medium text-foreground">Activity</b> is the header&rsquo;s
          Journal link, and <b className="font-medium text-foreground">Memory</b> opens persona and
          crew settings — which is Configuration, not overview.
        </p>
      </div>

      <div className="flex flex-wrap gap-2">
        {VARIANTS.map((v) => (
          <button
            key={v.id}
            type="button"
            onClick={() => setVariant(v.id)}
            className={cn(
              "type-row rounded-lg border px-3 py-1.5 transition-colors",
              v.id === variant
                ? "border-primary bg-primary/15 text-primary-hover"
                : "border-border text-muted-foreground hover:border-foreground/25 hover:text-foreground",
            )}
          >
            {v.title}
          </button>
        ))}
      </div>

      <p className="type-row max-w-3xl text-muted-foreground">{active.blurb}</p>

      <div className="rounded-xl border border-border/60 bg-background p-4">{active.render()}</div>
    </div>
  )
}
