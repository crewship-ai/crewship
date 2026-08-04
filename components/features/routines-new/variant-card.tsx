"use client"

// Variant "Karta" — the proposed shape of the REAL routine detail.
//
// The other two variants argue about where the graph and the code sit.
// This one argues about the page around them: what a routine detail
// should show at all, and in what order.
//
// Built entirely from components/ui/detail — the same Appear /
// DetailCard / StatStrip / Pill / EntityChip vocabulary the Inbox uses.
// Nothing new is invented, which is the point: the two surfaces should
// read as one product, and a routine is not so special that it needs
// its own kit.
//
// What it drops from today's detail, and why:
//
//   Preview tab      the graph is on the page now; a tab for a picture
//                    of the thing you are already looking at is a tab
//                    that exists to hide the picture.
//   Advanced tab     three levels of nesting for four unrelated things.
//                    Editor merges into the code panel; Waitpoints
//                    belong to a RUN, not to a definition, so they live
//                    in Activity/Inbox where the run is.
//   Runs-over-time   a 7-day sparkline for a routine with three runs is
//                    an empty chart with a legend.
//   Tabs, entirely   the Inbox has none and is the better surface for
//                    it. A tab is a hiding place: nobody has found the
//                    Schedules tab, which is why 38 routines have zero
//                    schedules between them.
//
// Everything that survived earns its place below, ordered by what an
// operator asks first: what is it → when does it run → is it healthy →
// what does it do → what can it reach → what did it do last.

import * as React from "react"
import Link from "next/link"
import {
  ArrowUpRight,
  Bot,
  Clock,
  Code2,
  Eye,
  GitBranch,
  Globe,
  KeyRound,
  MoreHorizontal,
  Play,
  Puzzle,
  Webhook,
} from "lucide-react"

import { cn } from "@/lib/utils"
import { Appear, DetailCard, EntityChip, Pill, StatStrip } from "@/components/ui/detail"
import { DEPENDENCY_SUMMARY, opacityOf, type Fidelity } from "@/lib/routines-preview/fixtures"
import { CodePane, DefinitionCanvas, RunHistoryCard, type Workbench } from "./shared"

const DEP_ICON = {
  integrations: Puzzle,
  notifications: Clock,
  credentials: KeyRound,
  agents: Bot,
  egress: Globe,
} as const

export function VariantCard({ fidelity, wb }: { fidelity: Fidelity; wb: Workbench }) {
  const [editing, setEditing] = React.useState(false)
  const steps = wb.dsl.steps ?? []
  const opacity = opacityOf(wb.dsl)

  // Everything the reviewer can reach, flattened into one row of chips.
  // Today this is four separate cards; as four cards nobody reads the
  // third one, and the question they answer ("what could this thing do
  // if it misbehaved?") is a single question.
  const reach = DEPENDENCY_SUMMARY.filter((g) => g.kind !== "notifications").flatMap((g) =>
    g.items.map((item) => ({ ...item, kind: g.kind })),
  )

  return (
    <div className="h-full overflow-auto">
      <div className="mx-auto flex max-w-[1100px] flex-col gap-3 p-4">
        {/* ── Identity. The name is the first thing on the page, because
            it is the first thing anyone looks for and today it sits
            below a row of status chrome. ─────────────────────────── */}
        <Appear order={0}>
          <DetailCard>
            <div className="flex flex-col gap-3">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div className="min-w-0">
                  <h1 className="truncate text-lg font-semibold tracking-tight">
                    Měsíční účetní podklady
                  </h1>
                  <div className="mt-0.5 flex flex-wrap items-baseline gap-x-2 font-mono text-[11px] text-muted-foreground">
                    <span>mesicni-ucetni-podklady</span>
                    <span aria-hidden>·</span>
                    <span>v1.0</span>
                    <span aria-hidden>·</span>
                    <span>@kontrolor</span>
                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-1.5">
                  <button
                    type="button"
                    className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-1.5 text-[12px] font-medium text-primary-foreground transition-colors hover:bg-primary/90"
                  >
                    <Play className="h-3.5 w-3.5" />
                    Spustit
                  </button>
                  <button
                    type="button"
                    className="inline-flex items-center gap-1.5 rounded-lg border border-border/60 px-3 py-1.5 text-[12px] font-medium text-muted-foreground transition-colors hover:text-foreground"
                  >
                    <Eye className="h-3.5 w-3.5" />
                    Nanečisto
                  </button>
                  <button
                    type="button"
                    aria-label="Další akce"
                    className="rounded-lg border border-border/60 p-1.5 text-muted-foreground transition-colors hover:text-foreground"
                  >
                    <MoreHorizontal className="h-3.5 w-3.5" />
                  </button>
                </div>
              </div>

              <p className="text-[13px] leading-relaxed text-foreground/85">
                Stáhne bankovní výpis z Gmailu, dohledá doklady, založí je na Drive, zrekonciluje
                součty a nechá člověka schválit.
              </p>

              <div className="flex flex-wrap gap-1.5">
                <Pill tone="success">poslední běh prošel</Pill>
                <Pill tone="default">plán · 1. v měsíci 6:00</Pill>
                <Pill tone={opacity >= 60 ? "warn" : "default"}>
                  {opacity} % kroků je agent
                </Pill>
                <Pill tone="default">{steps.length} kroků</Pill>
              </div>
            </div>
          </DetailCard>
        </Appear>

        {/* ── The four numbers worth a glance. Two columns on a phone,
            four from sm up — same strip the Inbox uses. ─────────── */}
        <Appear order={1}>
          <DetailCard bare>
            <StatStrip
              items={[
                { label: "Spouštěč", value: "plán · webhook", mono: true },
                { label: "Poslední běh", value: "před 3 dny", tone: "success" },
                { label: "Úspěšnost", value: "75 %" },
                { label: "Cena / běh", value: "$1,28 z $5", mono: true },
              ]}
            />
          </DetailCard>
        </Appear>

        {/* ── What it does. The graph IS the answer, so it gets the
            space the old prose list and the Preview tab used between
            them. ───────────────────────────────────────────────── */}
        <Appear order={2}>
          <DetailCard
            title="Co to dělá"
            subtitle={`${steps.length} kroků`}
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
                {editing ? "Zavřít kód" : "Upravit kód"}
              </button>
            }
          >
            <div className="flex h-[52vh] min-h-[380px] flex-col md:flex-row">
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
                <aside className="h-[45%] w-full shrink-0 border-t border-border/60 md:h-auto md:w-[46%] md:border-l md:border-t-0 lg:w-[42%]">
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

        {/* ── Blast radius as one row of chips, not four cards. The
            question is singular — "what could this reach?" — so the
            answer should be too. ──────────────────────────────── */}
        <Appear order={3}>
          <DetailCard title="Na co dosáhne" subtitle="blast radius">
            <div className="flex flex-wrap gap-1.5">
              {reach.map((item) => (
                <EntityChip
                  key={`${item.kind}:${item.name}`}
                  icon={DEP_ICON[item.kind as keyof typeof DEP_ICON]}
                  label={item.name}
                  note={item.detail}
                  tone={item.risk ? "warn" : "default"}
                />
              ))}
            </div>
          </DetailCard>
        </Appear>

        {/* ── Runs and the two things that used to be tabs. Side by
            side on wide, stacked on narrow. ───────────────────── */}
        <Appear order={4}>
          <RunHistoryCard compact />
        </Appear>

        <div className="grid gap-3 md:grid-cols-2">
          <Appear order={5}>
            <DetailCard
              title="Spouštěče"
              subtitle="2"
              icon={Clock}
              footer="Cron i webhooky na jednom místě — dnes jsou v různých tabech."
            >
              <ul className="space-y-1.5 text-[12px]">
                <li className="flex items-center justify-between gap-2">
                  <span className="inline-flex items-center gap-1.5">
                    <Clock className="h-3 w-3 text-muted-foreground" />
                    <span className="font-mono">0 6 1 * *</span>
                  </span>
                  <span className="text-muted-foreground">příště za 3 dny</span>
                </li>
                <li className="flex items-center justify-between gap-2">
                  <span className="inline-flex items-center gap-1.5">
                    <Webhook className="h-3 w-3 text-muted-foreground" />
                    <span className="font-mono">ucetni-podklady-hook</span>
                  </span>
                  <span className="text-muted-foreground">aktivní</span>
                </li>
              </ul>
            </DetailCard>
          </Appear>

          <Appear order={6}>
            <DetailCard
              title="Verze"
              subtitle="7"
              icon={GitBranch}
              footer="Jediné, co z celého Advanced tabu přežilo."
            >
              <ul className="space-y-1.5 text-[12px]">
                <li className="flex items-center justify-between gap-2">
                  <span className="font-mono">v7</span>
                  <span className="text-muted-foreground">aktivní · 2. 8. 2026</span>
                </li>
                <li className="flex items-center justify-between gap-2 text-muted-foreground">
                  <span className="font-mono">v6</span>
                  <span>1. 7. 2026</span>
                </li>
              </ul>
              <Link
                href="#"
                className="mt-2 inline-flex items-center gap-1 text-[11px] text-primary hover:underline"
              >
                Porovnat verze
                <ArrowUpRight className="h-3 w-3" />
              </Link>
            </DetailCard>
          </Appear>
        </div>
      </div>
    </div>
  )
}
