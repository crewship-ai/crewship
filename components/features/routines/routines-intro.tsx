"use client"

// "How routines work" (Harbor 2 RT-INTRO, routines.js intro()): what a routine
// is, why it is safe to let go, and the three steps from a sentence to a
// running routine. Opened from the top bar (?view=intro) and shown under the
// empty state of a workspace without routines.
//
// "In this workspace this week" (ODCH-RT-08) is shown only with real figures:
// the runs of the last 7 days, the share that finished without help, and the
// month's spend. Left out on purpose: the animated demo run (example data) and
// the template gallery (routine templates exist as an API and CLI, no
// gallery here yet).

import { Hand, Lock, Play, Plus, Workflow } from "lucide-react"
import { Button } from "@/components/ui/button"

const WHY = [
  {
    icon: Play,
    title: "Runs without you",
    text: "On a schedule, when GitHub or another app calls, when an issue starts, or from a chat command. Nobody has to remember.",
  },
  {
    icon: Hand,
    title: "Asks you only when it matters",
    text: "A decision comes with what the agent found and what it wants to do. Until you answer, nothing after that step runs.",
  },
  {
    icon: Lock,
    title: "Safe to let go",
    text: "Changes are tested before they go live. Risky actions need a Manager. Each run has a cost cap, and every run keeps its steps.",
  },
]

const HOW = [
  { title: "Describe", text: "Write what should happen, or ask a crew lead in Chat. You get a draft you can read." },
  { title: "Test", text: "The draft runs once without side effects: no messages, nothing published. You see what it would do." },
  { title: "Publish", text: "Turn it on. Every run gets its own page: each step, what it produced, what it cost and where it stopped." },
]

export interface IntroProof {
  runs: number
  /** Share of the finished runs that completed, or null when none finished. */
  finishedPct: number | null
  /** The month's spend, or null while unknown. */
  spendUsd: number | null
  month: string
}

export function RoutinesIntro({
  onNew,
  compact = false,
  proof,
  onOverview,
}: {
  onNew?: () => void
  compact?: boolean
  proof?: IntroProof | null
  onOverview?: () => void
}) {
  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8 px-4 py-6 md:px-6" data-testid="routines-intro">
      {!compact && (
        <section aria-labelledby="routines-intro-title" className="flex flex-col gap-3">
          <p className="inline-flex items-center gap-1.5 text-label font-medium text-muted-foreground">
            <Workflow className="h-3.5 w-3.5" aria-hidden /> Routines
          </p>
          <h1 id="routines-intro-title" className="text-display font-semibold tracking-tight text-foreground">
            Work that repeats, done by your crew.
          </h1>
          <p className="max-w-[60ch] text-body text-muted-foreground">
            Say what should happen and when. Your agents do it on time, ask you only when it matters, and show you every step.
          </p>
          {onNew && (
            <div>
              <Button type="button" onClick={onNew} className="gap-1.5 coarse:h-[44px]">
                <Plus className="h-3.5 w-3.5" aria-hidden /> New routine
              </Button>
            </div>
          )}
          <p className="text-label text-muted-foreground-soft">
            Or ask a crew lead in Chat: “every Thursday, check competitor prices”.
          </p>
        </section>
      )}

      <section aria-label="Why routines" className="grid grid-cols-1 gap-3 md:grid-cols-3">
        {WHY.map(({ icon: Icon, title, text }) => (
          <div key={title} className="flex flex-col gap-2 rounded-lg border border-hairline bg-card p-4">
            <span className="flex h-8 w-8 items-center justify-center rounded-md bg-foreground/[0.05]">
              <Icon className="h-4 w-4 text-foreground/80" aria-hidden />
            </span>
            <h3 className="text-body font-semibold text-foreground">{title}</h3>
            <p className="text-label text-muted-foreground">{text}</p>
          </div>
        ))}
      </section>

      <section aria-labelledby="routines-intro-how" className="flex flex-col gap-3">
        <h2 id="routines-intro-how" className="text-heading font-semibold text-foreground">
          From a sentence to a routine in three steps
        </h2>
        <ol className="grid grid-cols-1 gap-3 md:grid-cols-3">
          {HOW.map((step, i) => (
            <li key={step.title} className="flex gap-3 rounded-lg border border-hairline bg-card p-4">
              <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-primary/10 font-mono text-label font-semibold text-primary" aria-hidden>
                {i + 1}
              </span>
              <span className="flex flex-col gap-1">
                <b className="text-body font-semibold text-foreground">{step.title}</b>
                <span className="text-label text-muted-foreground">{step.text}</span>
              </span>
            </li>
          ))}
        </ol>
        {!compact && (
          <p className="text-label text-muted-foreground-soft">
            On the command line: <code className="font-mono">crewship routine init</code>.
          </p>
        )}
      </section>

      {proof && proof.runs > 0 && (
        <section aria-label="In this workspace this week" className="flex flex-col gap-3 rounded-lg border border-hairline bg-card p-4">
          <h2 className="text-label font-medium text-muted-foreground">In this workspace this week</h2>
          <div className="flex flex-wrap gap-x-8 gap-y-2">
            <span className="flex flex-col"><b className="text-title font-bold tabular-nums text-foreground">{proof.runs}</b><span className="text-label text-muted-foreground">runs</span></span>
            {proof.finishedPct !== null && (
              <span className="flex flex-col"><b className="text-title font-bold tabular-nums text-foreground">{proof.finishedPct} %</b><span className="text-label text-muted-foreground">finished without help</span></span>
            )}
            {proof.spendUsd !== null && (
              <span className="flex flex-col"><b className="text-title font-bold tabular-nums text-foreground">${proof.spendUsd.toFixed(2)}</b><span className="text-label text-muted-foreground">spent in {proof.month}</span></span>
            )}
          </div>
          {onOverview && (
            <div>
              <Button type="button" variant="ghost" size="sm" className="gap-1 coarse:h-[44px]" onClick={onOverview}>
                Open the overview
              </Button>
            </div>
          )}
        </section>
      )}
    </div>
  )
}
