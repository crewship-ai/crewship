"use client"

// Variant "Card" — the proposed shape of the REAL routine detail.
//
// The other two variants argue about where the graph and the code sit.
// This one argues about the page around them: what a routine detail
// shows at all, in what order, and how it uses the width it is given.
//
// Built from components/ui/detail — the same Appear / DetailCard /
// StatStrip / Pill / EntityChip vocabulary the Inbox uses. Nothing new
// is invented; the two surfaces should read as one product.
//
// Three things this revision fixes over the first pass:
//
//   Width.   The first pass capped the page at 1100px, which wastes
//            most of a monitor. It now uses the dashboard's own
//            approach — plain responsive grids, no cap — so the graph
//            grows into the space instead of a margin doing it.
//   Labels.  "What it does" / "What it touches" read like a tutorial.
//            Someone authoring a routine already knows what a routine
//            is. Definition, Access, Triggers.
//   Schedule.Shown as a sentence, with the cron as a caption. Nobody
//            reads `0 6 1 * *` and thinks "first of the month".
//
// English throughout, matching the rest of the product. The other two
// variants are Czech because they are notes to ourselves about layout;
// this one is a proposal for a shipping surface.

import * as React from "react"
import Link from "next/link"
import {
  ArrowUpRight,
  Bot,
  CalendarClock,
  CheckCircle2,
  Code2,
  Eye,
  GitBranch,
  Globe,
  KeyRound,
  Copy,
  Download,
  MoreHorizontal,
  Pencil,
  Play,
  Puzzle,
  Trash2,
  Type,
  Webhook,
} from "lucide-react"

import { cn } from "@/lib/utils"
import { Appear, DetailCard, EntityChip, Pill, StatStrip } from "@/components/ui/detail"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { DEPENDENCY_SUMMARY, RUN_HISTORY, opacityOf, type Fidelity } from "@/lib/routines-preview/fixtures"
import { CodePane, DefinitionCanvas, RunHistoryCard, type Workbench } from "./shared"

const DEP_ICON = {
  integrations: Puzzle,
  credentials: KeyRound,
  agents: Bot,
  egress: Globe,
} as const

type SideTab = "triggers" | "versions"

export function VariantCard({ fidelity, wb }: { fidelity: Fidelity; wb: Workbench }) {
  const [editing, setEditing] = React.useState(false)
  const [sideTab, setSideTab] = React.useState<SideTab>("triggers")
  const steps = wb.dsl.steps ?? []
  const opacity = opacityOf(wb.dsl)
  const lastRun = RUN_HISTORY[0]

  // Everything the routine can reach, as one row. Today this is four
  // separate cards; the question they answer — "what could this do if
  // it misbehaved?" — is one question, so it gets one answer.
  const reach = DEPENDENCY_SUMMARY.filter((g) => g.kind !== "notifications").flatMap((g) =>
    g.items.map((item) => ({ ...item, kind: g.kind })),
  )

  return (
    <div className="h-full overflow-auto">
      {/* No max-width. The dashboard fills the monitor and reads better
          for it; a routine with a 14-step graph needs the width more
          than the dashboard does. */}
      <div className="flex flex-col gap-4 p-4">
        {/* ── Identity ──────────────────────────────────────────── */}
        <Appear order={0}>
          <DetailCard>
            <div className="flex flex-col gap-3">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div className="min-w-0">
                  <h1 className="truncate text-lg font-semibold tracking-tight">
                    Monthly accounting pack
                  </h1>
                  <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
                    <span className="font-mono">mesicni-ucetni-podklady</span>
                    <span aria-hidden>·</span>
                    <span className="font-mono">v7</span>
                    <span aria-hidden>·</span>
                    {/* The owning AGENT, not the human who typed it.
                        Routines are run by agents, and the agent page
                        already answers the follow-up question — what
                        else does it own, what is it working on. */}
                    <Link
                      href="/crews"
                      className="inline-flex items-center gap-1.5 rounded-full border border-border/60 py-0.5 pl-0.5 pr-2 transition-colors hover:border-border hover:text-foreground"
                    >
                      <AgentAvatar seed="auditor" className="h-4 w-4" alt="" />
                      <span className="font-medium">auditor</span>
                    </Link>
                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-1.5">
                  <button
                    type="button"
                    className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-1.5 text-[12px] font-medium text-primary-foreground transition-colors hover:bg-primary/90"
                  >
                    <Play className="h-3.5 w-3.5" />
                    Run
                  </button>
                  <button
                    type="button"
                    className="inline-flex items-center gap-1.5 rounded-lg border border-border/60 px-3 py-1.5 text-[12px] font-medium text-muted-foreground transition-colors hover:text-foreground"
                  >
                    <Eye className="h-3.5 w-3.5" />
                    Dry run
                  </button>
                  {/* The kebab is where the verbs live. Empty, it is a
                      button that promises actions and delivers a menu of
                      nothing — same shape as the account menu, so the
                      two read as one product. */}
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <button
                        type="button"
                        aria-label="More actions"
                        className="rounded-lg border border-border/60 p-1.5 text-muted-foreground transition-colors hover:text-foreground"
                      >
                        <MoreHorizontal className="h-3.5 w-3.5" />
                      </button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end" className="w-52">
                      <DropdownMenuItem>
                        <Pencil className="h-3.5 w-3.5" />
                        Edit definition
                      </DropdownMenuItem>
                      <DropdownMenuItem>
                        <Type className="h-3.5 w-3.5" />
                        Rename &amp; description
                      </DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem>
                        <Copy className="h-3.5 w-3.5" />
                        Duplicate
                      </DropdownMenuItem>
                      <DropdownMenuItem>
                        <Download className="h-3.5 w-3.5" />
                        Export bundle
                      </DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem variant="destructive">
                        <Trash2 className="h-3.5 w-3.5" />
                        Delete routine
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </div>
              </div>

              <p className="max-w-[80ch] text-[13px] leading-relaxed text-foreground/85">
                Pulls the bank statement out of Gmail, finds every matching invoice, files them on
                Drive, reconciles the totals and parks on a human approval.
              </p>

              <div className="flex flex-wrap gap-1.5">
                <Pill tone="success">healthy</Pill>
                <Pill tone="default">scheduled</Pill>
                <Pill tone={opacity >= 60 ? "warn" : "default"}>{opacity}% agent steps</Pill>
                <Pill tone="default">{steps.length} steps</Pill>
              </div>
            </div>
          </DetailCard>
        </Appear>

        {/* ── The numbers. Two up on a phone, six across a monitor —
            the dashboard's own ratio. ─────────────────────────── */}
        {/* StatStrip renders its own bordered strip — wrapping it in a
            DetailCard would draw a second border around the first. */}
        <Appear order={1}>
          <StatStrip
            items={[
              { label: "Next run", value: "in 3 days" },
              { label: "Last run", value: "3 days ago", tone: "success" },
              { label: "Pass rate", value: "75%" },
              { label: "Avg duration", value: "12 min", mono: true },
            ]}
          />
        </Appear>

        {/* ── Definition + the side rail. The graph takes two thirds
            from xl up and three quarters on an ultrawide; below that
            everything stacks. ──────────────────────────────────── */}
        <div className="grid gap-4 grid-cols-1 xl:grid-cols-3 2xl:grid-cols-4">
          <Appear order={2} className="xl:col-span-2 2xl:col-span-3">
            <DetailCard
              title="Definition"
              subtitle={`${steps.length} steps`}
              bare
              action={
                <button
                  type="button"
                  onClick={() => setEditing((v) => !v)}
                  className={cn(
                    "inline-flex items-center gap-1.5 rounded-md border px-2 py-1 text-[11px] font-medium transition-colors",
                    editing
                      ? "border-primary/40 bg-primary/15 text-primary"
                      : "border-border/60 text-muted-foreground hover:text-foreground",
                  )}
                >
                  <Code2 className="h-3 w-3" />
                  {editing ? "Close code" : "Edit code"}
                </button>
              }
            >
              <div className="flex h-[56vh] min-h-[380px] flex-col md:flex-row">
                <div className="relative min-h-[240px] min-w-0 flex-1">
                  <DefinitionCanvas
                    dsl={wb.dsl}
                    selectedStepId={wb.selected}
                    onStepSelect={wb.onSelect}
                    focusStepId={wb.focus}
                    recenterOnResize
                    viewportRef={wb.viewportRef}
                  />
                </div>
                {editing && (
                  <aside className="h-[45%] w-full shrink-0 border-t border-border/60 md:h-auto md:w-[48%] md:border-l md:border-t-0">
                    <CodePane
                      fidelity={fidelity}
                      onStepAtCaret={wb.onCaret}
                      follow={wb.follow}
                      onFollowChange={wb.setFollow}
                      onApply={wb.setDsl}
                    />
                  </aside>
                )}
              </div>
            </DetailCard>
          </Appear>

          {/* The rail runs newest-fact-first: what just happened, what
              happens next, what it can reach, and the flat facts last.
              The first pass left it half empty because everything of
              substance sat in the wide column. */}
          <div className="flex flex-col gap-4">
            <Appear order={3}>
              <LastRunCard summary={lastRun.summary} />
            </Appear>

            <Appear order={4}>
              <DetailCard
                title={sideTab === "triggers" ? "Triggers" : "Versions"}
                subtitle={sideTab === "triggers" ? "2" : "7"}
                icon={sideTab === "triggers" ? CalendarClock : GitBranch}
                tone="purple"
                action={
                  <div className="flex items-center gap-0.5 rounded-md border border-border/60 p-0.5">
                    {(["triggers", "versions"] as const).map((t) => (
                      <button
                        key={t}
                        type="button"
                        onClick={() => setSideTab(t)}
                        aria-pressed={sideTab === t}
                        className={cn(
                          "rounded px-1.5 py-0.5 text-[10px] font-medium capitalize transition-colors",
                          sideTab === t
                            ? "bg-primary/15 text-primary"
                            : "text-muted-foreground hover:text-foreground",
                        )}
                      >
                        {t}
                      </button>
                    ))}
                  </div>
                }
              >
                {sideTab === "triggers" ? <Triggers /> : <Versions />}
              </DetailCard>
            </Appear>

            <Appear order={5}>
              <DetailCard
                title="Access"
                subtitle="what this can reach"
                tone="warn"
                footer="Amber marks reach a reviewer should look at twice."
              >
                <div className="flex flex-wrap gap-1.5">
                  {reach.map((item) => (
                    <EntityChip
                      key={`${item.kind}:${item.name}`}
                      icon={DEP_ICON[item.kind as keyof typeof DEP_ICON]}
                      label={item.name}
                      tone={item.risk ? "warn" : "default"}
                    />
                  ))}
                </div>
              </DetailCard>
            </Appear>

            <Appear order={6}>
              <Metadata steps={steps.length} />
            </Appear>
          </div>
        </div>

        {/* ── Runs. Full width: the rail already carries triggers,
            versions, access and metadata, and a run row reads better
            long than boxed. ─────────────────────────────────────── */}
        <Appear order={7}>
          <RunHistoryCard pipelineSlug="mesicni-ucetni-podklady" />
        </Appear>
      </div>
    </div>
  )
}

/**
 * The flat facts.
 *
 * Low weight individually, which is why they sit last — but the review
 * asked for them by name, and the reason is sound: paired with the
 * version list they are how you answer "what changed since the run that
 * worked", which is the question you have when something breaks.
 */
function Metadata({ steps }: { steps: number }) {
  return (
    <DetailCard title="Metadata">
      <dl className="grid grid-cols-2 gap-x-4 gap-y-2.5 text-[11px]">
        {[
          ["DSL version", "1.0"],
          ["Visibility", "workspace"],
          ["Hash", "34d7eb485f…"],
          ["Steps", String(steps)],
          ["Created", "23 Jul 2026"],
          ["Updated", "30 Jul 2026"],
        ].map(([k, v]) => (
          <div key={k}>
            <dt className="text-[10px] uppercase tracking-wider text-muted-foreground-soft">{k}</dt>
            <dd className="mt-0.5 truncate font-mono text-foreground/85">{v}</dd>
          </div>
        ))}
      </dl>
    </DetailCard>
  )
}

/**
 * Last run, with the tinted header from today's overview.
 *
 * Kept verbatim because it is the detail's best-judged element: the
 * colour says "this ended well" before a single word is read, and it
 * does it with a 4%-opacity gradient rather than shouting.
 */
function LastRunCard({ summary }: { summary: string }) {
  return (
    <div className="overflow-hidden rounded-xl border border-border/60 bg-card">
      <div className="flex items-center gap-3 border-b border-border/40 bg-gradient-to-r from-success/[0.06] to-transparent px-4 py-3">
        <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-success/20 text-success">
          <CheckCircle2 className="h-4 w-4" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="text-[13px] font-medium">Last run · completed</div>
          <div className="truncate font-mono text-[10px] text-muted-foreground">
            run_cms7notoy0001cd68ffc4
          </div>
        </div>
      </div>
      <div className="space-y-2 px-4 py-3">
        <p className="text-[12px] text-foreground/85">{summary}</p>
        <dl className="grid grid-cols-3 gap-2 text-[11px]">
          {[
            ["started", "3 days ago"],
            ["duration", "14 min"],
            ["cost", "$1.42"],
          ].map(([k, v]) => (
            <div key={k}>
              <dt className="text-[10px] uppercase tracking-wider text-muted-foreground/60">{k}</dt>
              <dd className="tabular-nums text-foreground/85">{v}</dd>
            </div>
          ))}
        </dl>
        <Link
          href="/activity"
          className="inline-flex items-center gap-1 text-[11px] text-primary hover:underline"
        >
          Open full trace
          <ArrowUpRight className="h-3 w-3" />
        </Link>
      </div>
    </div>
  )
}

/**
 * Triggers as sentences.
 *
 * `0 6 1 * *` is a correct answer to a question nobody asked. The
 * schedule people care about is "the 1st of every month at 06:00", so
 * that is the line; the cron stays as a caption for whoever is
 * debugging it. A picker that produces this without anyone typing cron
 * is a bigger change and belongs in its own PR.
 */
function Triggers() {
  return (
    <ul className="space-y-2.5 text-[12px]">
      <li className="flex items-start gap-2">
        <CalendarClock className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        <div className="min-w-0 flex-1">
          <div className="text-foreground/90">1st of every month at 06:00</div>
          <div className="flex flex-wrap items-baseline gap-x-2 text-[10px] text-muted-foreground">
            <span className="font-mono">0 6 1 * *</span>
            <span aria-hidden>·</span>
            <span>Europe/Prague</span>
            <span aria-hidden>·</span>
            <span className="text-info">next in 3 days</span>
          </div>
        </div>
      </li>
      <li className="flex items-start gap-2">
        <Webhook className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        <div className="min-w-0 flex-1">
          <div className="text-foreground/90">On webhook</div>
          <div className="font-mono text-[10px] text-muted-foreground">ucetni-podklady-hook</div>
        </div>
      </li>
    </ul>
  )
}

function Versions() {
  return (
    <ul className="space-y-2 text-[12px]">
      {[
        ["v7", "active · 2 Aug 2026"],
        ["v6", "1 Jul 2026"],
        ["v5", "3 Jun 2026"],
      ].map(([v, when], i) => (
        <li key={v} className="flex items-center justify-between gap-2">
          <span className={cn("font-mono", i > 0 && "text-muted-foreground")}>{v}</span>
          <span className="text-muted-foreground">{when}</span>
        </li>
      ))}
    </ul>
  )
}
