"use client"

// Building blocks shared by the three /routines-new design variants.
//
// The rule for this whole preview: reuse the REAL surfaces wherever one
// exists. The canvas is Activity's own TraceCanvas fed a definition-mode
// run, and the code pane is the same CodeMirror the file editor and the
// routine Editor tab use. A preview drawn with lookalike components can
// flatter a design that would not survive contact with the real
// renderer; this one cannot.

import * as React from "react"
import Link from "next/link"
import {
  AlertTriangle,
  ArrowUpRight,
  Bell,
  Bot,
  CheckCircle2,
  Clock,
  Globe,
  KeyRound,
  PauseCircle,
  Puzzle,
  Crosshair,
  ShieldAlert,
  XCircle,
  type LucideIcon,
} from "lucide-react"

import { cn } from "@/lib/utils"
import { Progress } from "@/components/ui/progress"
import { FileEditor } from "@/components/features/files/file-editor"
import { TraceCanvas } from "@/components/features/activity/trace-canvas"
import { stepIdAtLine, stepLineRanges } from "@/lib/routine-dsl-lines"
import { convertDsl, parseDsl, toYaml, type DslFormat } from "@/lib/routine-dsl-format"
import { routineDslExtensions } from "@/lib/routine-dsl-editor-extensions"
import type { HeatmapBucket } from "@/lib/trace/percentile-heatmap"
import type { PipelineDSL, TraceStep } from "@/lib/trace/types"
import {
  DEPENDENCY_SUMMARY,
  RUN_HISTORY,
  definitionRun,
  dslSource,
  opacityOf,
  type DependencyKind,
  type Fidelity,
  type PreviewRun,
} from "@/lib/routines-preview/fixtures"

/* ------------------------------------------------------------------ *
 *  Canvas — Activity's renderer in definition mode                    *
 * ------------------------------------------------------------------ */

// TraceCanvas takes these for live runs. A definition has no metrics,
// no heatmap and no waitpoint tokens, so they are permanently empty.
// Module-level constants keep the identity stable across renders —
// fresh Maps would retrigger the canvas's fitView effect every paint.
const NO_TOKENS: ReadonlyMap<string, string> = new Map<string, string>()
const NO_BUCKETS: ReadonlyMap<string, HeatmapBucket> = new Map<string, HeatmapBucket>()
const NO_METRICS: ReadonlyMap<string, { durationMs: number; costUsd: number }> = new Map<
  string,
  { durationMs: number; costUsd: number }
>()

interface DefinitionCanvasProps {
  dsl: PipelineDSL
  selectedStepId?: string | null
  onStepSelect?: (id: string | null) => void
  /** Node to bring into view without a click — driven by the caret. */
  focusStepId?: string | null
  className?: string
}

/**
 * The routine's shape, drawn by the same code that draws live runs.
 *
 * `definitionRun()` supplies a run with no outputs and no current step,
 * so every node lands on `pending` — the canvas shows structure without
 * ever implying an outcome.
 */
export function DefinitionCanvas({
  dsl,
  selectedStepId = null,
  onStepSelect,
  focusStepId = null,
  className,
}: DefinitionCanvasProps) {
  // One run object per mounted canvas. React Flow keys node state off
  // run.id, and a new object each render would reset the graph.
  const run = React.useMemo(() => definitionRun(), [])
  const handleSelect = React.useCallback(
    (id: string | null) => onStepSelect?.(id),
    [onStepSelect],
  )

  return (
    <div className={cn("relative h-full w-full", className)}>
      <TraceCanvas
        run={run}
        dsl={dsl}
        selectedStepId={selectedStepId}
        onStepSelect={handleSelect}
        workspaceId=""
        waitpointTokensByStepId={NO_TOKENS}
        heatmapBuckets={NO_BUCKETS}
        stepMetrics={NO_METRICS}
        initialFocus="start"
        centerOnSelect
        focusStepId={focusStepId}
      />
      <div className="pointer-events-none absolute left-3 top-3 rounded-md border border-border/60 bg-card/85 px-2 py-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground backdrop-blur">
        Definice · nikoli běh
      </div>
    </div>
  )
}

/* ------------------------------------------------------------------ *
 *  Code pane                                                          *
 * ------------------------------------------------------------------ */

interface CodePaneProps {
  fidelity: Fidelity
  /** Shown under the editor; the variants word this differently. */
  footnote?: string
  /**
   * Fires with the step the caret is inside, or null between steps.
   * Already deduped — it only fires when the STEP changes, not on every
   * caret move, so the caller can drive a viewport with it directly.
   */
  onStepAtCaret?: (stepId: string | null) => void
  /** Renders the follow toggle. Omit for panes with nothing to follow. */
  follow?: boolean
  onFollowChange?: (next: boolean) => void
  /**
   * Receives the parsed definition when a save validates.
   *
   * Without it the pane is read-only and says so: it will not claim a
   * redraw nobody performed.
   */
  onApply?: (dsl: PipelineDSL) => void
}

/**
 * The right half of the split: the definition as JSON, editable.
 *
 * Save is deliberately inert in the preview — it reports what WOULD
 * happen (parse → doctor → save → canvas redraw) instead of pretending
 * to persist. A preview that silently swallows a save is worse than one
 * that admits it is a preview.
 */
export function CodePane({
  fidelity,
  footnote,
  onStepAtCaret,
  follow,
  onFollowChange,
  onApply,
}: CodePaneProps) {
  // YAML by default. The win is not fewer braces — it is `prompt: |`:
  // the production routine's 600-character prompts are one JSON line of
  // \n escapes, which nobody can read, let alone review. Same split the
  // CLI already makes (internal/pipeline/parse_yaml.go, #1423): humans
  // author YAML, the server stores canonical JSON.
  const [format, setFormat] = React.useState<DslFormat>("yaml")
  const source = React.useMemo(() => {
    const json = dslSource(fidelity)
    if (format === "json") return json
    const converted = convertDsl(json, "json", "yaml")
    return converted.ok ? converted.text : json
  }, [fidelity, format])

  // Line spans are a property of the source, so they are computed once
  // per definition rather than on every keystroke. Works in both
  // formats — the mapper runs on the YAML AST, and YAML 1.2 parses JSON.
  const ranges = React.useMemo(() => stepLineRanges(source), [source])

  // Rebuilt only when the format changes. FileEditor recreates its
  // EditorState when this identity changes, so an unmemoized array
  // would blow the buffer away on every render.
  const extraExtensions = React.useMemo(() => routineDslExtensions(format), [format])
  const [dirty, setDirty] = React.useState(false)
  const [saved, setSaved] = React.useState(false)

  // The confirmation is transient, so it needs a timer — but the
  // timer must outlive neither the next save nor the component.
  // Returning a cleanup from useCallback does nothing: both callers
  // (onClick, FileEditor.onSave) discard the return value, so every
  // save would leak a timer that can setSaved after unmount.
  const savedTimer = React.useRef<ReturnType<typeof setTimeout> | null>(null)
  React.useEffect(
    () => () => {
      if (savedTimer.current) clearTimeout(savedTimer.current)
    },
    [],
  )

  const [error, setError] = React.useState<string | null>(null)

  // Reads the live document and hands it to onSave. The button and
  // Cmd+S go through the same path — a button that flipped state
  // without reading the buffer is how the pane came to report a save
  // that never happened.
  const saveRef = React.useRef<(() => void) | null>(null)

  const handleSave = React.useCallback(
    (content: string) => {
      const result = parseDsl(content, format)
      if (!result.ok) {
        setError(result.line ? `${result.message} (řádek ${result.line})` : result.message)
        setSaved(false)
        return
      }
      const parsed: unknown = result.value
      // Same shape check the routine Editor tab applies: an object with
      // a name and a steps array. Anything else is valid JSON and not a
      // routine, and drawing it would produce an empty canvas rather
      // than an error.
      const obj = parsed as { steps?: unknown } | null
      if (!obj || typeof obj !== "object" || !Array.isArray(obj.steps)) {
        setError("definice musí být objekt s polem `steps`")
        setSaved(false)
        return
      }
      setError(null)
      onApply?.({ steps: obj.steps as PipelineDSL["steps"] })
      setSaved(true)
      setDirty(false)
      if (savedTimer.current) clearTimeout(savedTimer.current)
      savedTimer.current = setTimeout(() => setSaved(false), 2400)
    },
    [onApply, format],
  )

  // Dedupe here rather than in the caller: the caret fires on every
  // arrow key, but only a change of STEP is news. Without this the
  // canvas would be asked to re-centre on the node it is already
  // centred on, dozens of times a second while someone types.
  const lastStepRef = React.useRef<string | null>(null)
  const handleCursorLine = React.useCallback(
    (line: number) => {
      const id = stepIdAtLine(ranges, line)
      if (id === lastStepRef.current) return
      lastStepRef.current = id
      onStepAtCaret?.(id)
    },
    [ranges, onStepAtCaret],
  )

  return (
    <div className="flex h-full flex-col overflow-hidden bg-card/30">
      <div className="flex shrink-0 items-center justify-between gap-2 border-b border-border/60 px-3 py-2">
        <div className="flex items-center gap-2">
          <span className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
            Definice
          </span>
          <div className="flex items-center gap-0.5 rounded-md border border-border/60 p-0.5">
            {(["yaml", "json"] as const).map((f) => (
              <button
                key={f}
                type="button"
                onClick={() => setFormat(f)}
                aria-pressed={format === f}
                className={cn(
                  "rounded px-1.5 py-0.5 font-mono text-[10px] uppercase transition-colors",
                  format === f
                    ? "bg-primary/15 text-primary"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                {f}
              </button>
            ))}
          </div>
        </div>
        <div className="flex items-center gap-2 text-[11px]">
          {error ? (
            <span className="inline-flex items-center gap-1 text-destructive" role="alert">
              <AlertTriangle className="h-3 w-3" />
              syntax: {error}
            </span>
          ) : (
            <span className="inline-flex items-center gap-1 text-success">
              <CheckCircle2 className="h-3 w-3" />
              syntax ok
            </span>
          )}
          {onFollowChange && (
            <button
              type="button"
              onClick={() => onFollowChange(!follow)}
              aria-pressed={follow}
              title="Kurzor v kódu vybírá a vystředí odpovídající krok v grafu"
              className={cn(
                "inline-flex items-center gap-1 rounded-md border px-1.5 py-1 text-[11px] font-medium transition-colors",
                follow
                  ? "border-primary/40 bg-primary/15 text-primary"
                  : "border-border/60 text-muted-foreground hover:text-foreground",
              )}
            >
              <Crosshair className="h-3 w-3" />
              Sledovat pohyb
            </button>
          )}
          <button
            type="button"
            onClick={() => saveRef.current?.()}
            className={cn(
              "rounded-md px-2 py-1 text-[11px] font-medium transition-colors",
              dirty
                ? "bg-primary text-primary-foreground hover:bg-primary/90"
                : "border border-border/60 text-muted-foreground hover:text-foreground",
            )}
          >
            {saved ? "Uloženo · graf překreslen" : "Uložit"}
          </button>
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-auto">
        <FileEditor
          code={source}
          language={format}
          onSave={handleSave}
          onDirtyChange={setDirty}
          onCursorLine={handleCursorLine}
          saveRef={saveRef}
          extraExtensions={extraExtensions}
        />
      </div>
      <p className="shrink-0 border-t border-border/60 px-3 py-2 text-[11px] text-muted-foreground">
        {footnote ??
          (onApply
            ? "Editace jen kódem. Graf je odvozený pohled — po uložení se překreslí z uloženého DSL."
            : "Editace jen kódem. Tento panel je jen ke čtení — uložení se nikam nepropíše.")}
        {format === "yaml" && (
          <>
            {" "}
            <span className="text-warn">
              Ukládá se kanonický JSON, takže komentáře v YAMLu uložení nepřežijí.
            </span>
          </>
        )}
      </p>
    </div>
  )
}

/* ------------------------------------------------------------------ *
 *  Dependency summary                                                 *
 * ------------------------------------------------------------------ */

const DEP_ICON: Record<DependencyKind, LucideIcon> = {
  integrations: Puzzle,
  notifications: Bell,
  credentials: KeyRound,
  agents: Bot,
  egress: Globe,
}

/**
 * "Na čem to visí" — the reviewer's checklist, one group per question.
 *
 * `columns` lets a variant lay this out as a row under a wide canvas or
 * as a stack in a narrow rail without forking the component.
 */
export function DependencySummary({ columns = 2 }: { columns?: 1 | 2 | 3 }) {
  return (
    <div
      className={cn(
        "grid gap-3",
        columns === 1 && "grid-cols-1",
        columns === 2 && "grid-cols-1 md:grid-cols-2",
        columns === 3 && "grid-cols-1 md:grid-cols-3",
      )}
    >
      {DEPENDENCY_SUMMARY.map((group) => {
        const Icon = DEP_ICON[group.kind]
        return (
          <section
            key={group.kind}
            className="rounded-xl border border-border/60 bg-card p-3"
          >
            <header className="mb-2 flex items-start gap-2">
              <span className="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground">
                <Icon className="h-3.5 w-3.5" />
              </span>
              <div className="min-w-0">
                <h4 className="text-[12px] font-semibold">{group.title}</h4>
                <p className="text-[11px] leading-snug text-muted-foreground">
                  {group.question}
                </p>
              </div>
            </header>
            <ul className="space-y-1">
              {group.items.map((item) => (
                <li
                  key={item.name}
                  className="flex items-baseline gap-2 text-[11px]"
                >
                  <span
                    className={cn(
                      "shrink-0 rounded border px-1.5 py-0.5 font-medium",
                      item.risk
                        ? "border-warn/35 text-warn"
                        : "border-border/60 text-foreground/85",
                    )}
                  >
                    {item.name}
                  </span>
                  <span className="min-w-0 text-muted-foreground">{item.detail}</span>
                </li>
              ))}
            </ul>
          </section>
        )
      })}
    </div>
  )
}

/* ------------------------------------------------------------------ *
 *  Run history                                                        *
 * ------------------------------------------------------------------ */

const RUN_STATUS: Record<PreviewRun["status"], { Icon: LucideIcon; tint: string; label: string }> = {
  completed: { Icon: CheckCircle2, tint: "text-success", label: "hotovo" },
  failed: { Icon: XCircle, tint: "text-destructive", label: "spadlo" },
  waiting: { Icon: PauseCircle, tint: "text-warn", label: "čeká na schválení" },
}

function formatDuration(ms: number): string {
  const mins = Math.round(ms / 60_000)
  if (mins < 60) return `${mins} min`
  const hrs = Math.floor(mins / 60)
  return `${hrs} h ${mins % 60} min`
}

// Locale AND time zone are pinned. This is a client component that
// Next prerenders on the server, so an unpinned zone lets the server
// string and the browser string disagree — a hydration mismatch on a
// page whose only job is to look right.
function formatWhen(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime())
    ? "—"
    : d.toLocaleDateString("cs-CZ", {
        day: "numeric",
        month: "short",
        year: "numeric",
        timeZone: "UTC",
      })
}

/**
 * Historical runs, each row a link into /activity for that run.
 *
 * The row carries a human summary ("18 dokladů nahráno · čeká na
 * schválení") next to the id, because a column of run ids and green
 * ticks tells an operator nothing about what the month actually did.
 */
export function RunHistoryCard({ compact = false }: { compact?: boolean }) {
  const rows = compact ? RUN_HISTORY.slice(0, 3) : RUN_HISTORY
  return (
    <div className="overflow-hidden rounded-xl border border-border/60 bg-card">
      <header className="flex items-center justify-between border-b border-border/60 px-3 py-2">
        <div className="flex items-center gap-2">
          <Clock className="h-3.5 w-3.5 text-muted-foreground" />
          <h4 className="text-[12px] font-semibold">Historie běhů</h4>
        </div>
        <span className="text-[11px] text-muted-foreground">{RUN_HISTORY.length} celkem</span>
      </header>
      <ul className="divide-y divide-border/40">
        {rows.map((run) => {
          const s = RUN_STATUS[run.status]
          return (
            <li key={run.id}>
              <Link
                href={`/activity?run=${encodeURIComponent(run.id)}`}
                className="group grid grid-cols-[auto_1fr_auto] items-center gap-3 px-3 py-2.5 transition-colors hover:bg-white/[0.025]"
              >
                <s.Icon className={cn("h-4 w-4 shrink-0", s.tint)} />
                <div className="min-w-0">
                  <div className="flex items-baseline gap-2">
                    <span className="truncate text-[12px] font-medium">{run.summary}</span>
                    <span className="shrink-0 text-[10px] uppercase tracking-wide text-muted-foreground">
                      {run.trigger === "schedule" ? "plán" : "ručně"}
                    </span>
                  </div>
                  <div className="truncate font-mono text-[10px] text-muted-foreground">
                    {run.id}
                  </div>
                </div>
                <div className="text-right">
                  <div className="text-[11px] tabular-nums text-foreground/85">
                    {formatWhen(run.started_at)}
                  </div>
                  <div className="text-[10px] tabular-nums text-muted-foreground">
                    {formatDuration(run.duration_ms)} · ${run.cost_usd.toFixed(2)}
                  </div>
                </div>
              </Link>
            </li>
          )
        })}
      </ul>
      <footer className="border-t border-border/60 px-3 py-2">
        <Link
          href="/activity"
          className="inline-flex items-center gap-1 text-[11px] text-primary hover:underline"
        >
          Otevřít celou stopu v Activity
          <ArrowUpRight className="h-3 w-3" />
        </Link>
      </footer>
    </div>
  )
}

/* ------------------------------------------------------------------ *
 *  Opacity meter — the product argument, as a number                  *
 * ------------------------------------------------------------------ */

/**
 * How much of the routine is an agent black box.
 *
 * Deliberately blunt: this is the number that decides whether an
 * operator can predict what a routine will do, or can only audit what
 * it did.
 */
export function OpacityMeter({ dsl }: { dsl: PipelineDSL }) {
  const pct = opacityOf(dsl)
  const steps = dsl.steps ?? []
  const opaque = steps.filter((s) => s.type === "agent_run").length
  const tone = pct >= 60 ? "text-warn" : pct >= 30 ? "text-notice" : "text-success"

  return (
    <div className="flex items-center gap-3 rounded-lg border border-border/60 bg-card px-3 py-2">
      <div className="flex items-center gap-1.5">
        {pct >= 60 ? (
          <AlertTriangle className={cn("h-3.5 w-3.5", tone)} />
        ) : (
          <ShieldAlert className={cn("h-3.5 w-3.5", tone)} />
        )}
        <span className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
          Neprůhlednost
        </span>
      </div>
      <div className="flex items-baseline gap-1.5">
        <span className={cn("text-lg font-semibold tabular-nums leading-none", tone)}>{pct}%</span>
        <span className="text-[11px] text-muted-foreground">
          {opaque} z {steps.length} kroků je agent
        </span>
      </div>
      <Progress
        value={pct}
        aria-label={`Neprůhlednost ${pct} procent — ${opaque} z ${steps.length} kroků je agent`}
        className="h-1.5 min-w-[60px] flex-1 bg-muted"
        indicatorClassName={cn(
          pct >= 60 ? "bg-warn" : pct >= 30 ? "bg-notice" : "bg-success",
        )}
      />
    </div>
  )
}

/* ------------------------------------------------------------------ *
 *  Step inspector — variant C's right rail                            *
 * ------------------------------------------------------------------ */

/**
 * One step, as YAML.
 *
 * The editor authors YAML, so the read-only fragment shows YAML too —
 * two syntaxes on one screen means the reader holds both in their head
 * to compare the panel against the buffer. A multiline prompt is the
 * case that makes the difference obvious: `prompt: |` and real line
 * breaks, rather than one long line of \n escapes.
 */
function stepFragment(step: TraceStep): string {
  return toYaml(step)
}

/** Prose describing what a step kind guarantees the operator. */
const KIND_CONTRACT: Record<string, string> = {
  agent_run: "Agent rozhoduje. Výsledek nelze předpovědět z definice — jen auditovat po běhu.",
  http: "Deterministické. Jedno volání, známý endpoint, known-shape odpověď.",
  script: "Deterministické. Spustí soubor z repozitáře receptu.",
  transform: "Deterministické. Čistá funkce nad výstupem předchozího kroku.",
  foreach: "Smyčka. Tělo se spustí jednou za položku — tady běh tráví většinu času.",
  notify: "Zapíše kartu do inboxu a pošle ji kanály dle kategorie.",
  wait: "Zaparkuje běh, dokud člověk nerozhodne.",
  query: "Přečte datastore. Deterministické.",
  call_pipeline: "Zavolá jiný recept jako podproces.",
  code: "Spustí inline kód v sandboxu.",
}

export function StepInspector({
  dsl,
  fidelity,
  stepId,
}: {
  dsl: PipelineDSL
  // Threaded, never re-derived. The empty-selection branch prints the
  // whole definition, and printing a different fidelity than the one
  // the canvas is drawing would have the two halves contradict each
  // other — the exact failure this design exists to prevent.
  fidelity: Fidelity
  stepId: string | null
}) {
  const step = (dsl.steps ?? []).find((s) => s.id === stepId) ?? null

  if (!step) {
    return (
      <div className="flex h-full flex-col">
        <header className="shrink-0 border-b border-border/60 px-3 py-2">
          <h4 className="text-[12px] font-semibold">Celá definice</h4>
          <p className="text-[11px] text-muted-foreground">
            Klikni na krok v grafu a uvidíš jen jeho fragment.
          </p>
        </header>
        <div className="min-h-0 flex-1">
          <CodePane fidelity={fidelity} footnote="Nic není vybráno — editujeme celý recept." />
        </div>
      </div>
    )
  }

  const contract = KIND_CONTRACT[step.type] ?? ""
  const dependsOn = step.needs ?? []
  const feeds = (dsl.steps ?? []).filter((s) => (s.needs ?? []).includes(step.id))

  return (
    <div className="flex h-full flex-col overflow-auto">
      <header className="shrink-0 border-b border-border/60 px-3 py-2.5">
        <div className="flex items-center gap-2">
          <h4 className="font-mono text-[13px] font-semibold">{step.id}</h4>
          <span className="rounded border border-border/60 px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
            {step.type}
          </span>
        </div>
        {contract && (
          <p className="mt-1.5 text-[11px] leading-snug text-muted-foreground">{contract}</p>
        )}
      </header>

      <div className="space-y-3 p-3">
        <div className="grid grid-cols-2 gap-2 text-[11px]">
          <div className="rounded-lg border border-border/60 bg-card p-2">
            <div className="mb-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
              Čeká na
            </div>
            {dependsOn.length === 0 ? (
              <span className="text-muted-foreground">nic — startuje hned</span>
            ) : (
              <div className="flex flex-wrap gap-1">
                {dependsOn.map((n) => (
                  <span key={n} className="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px]">
                    {n}
                  </span>
                ))}
              </div>
            )}
          </div>
          <div className="rounded-lg border border-border/60 bg-card p-2">
            <div className="mb-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
              Odblokuje
            </div>
            {feeds.length === 0 ? (
              <span className="text-muted-foreground">nic — konec větve</span>
            ) : (
              <div className="flex flex-wrap gap-1">
                {feeds.map((s) => (
                  <span key={s.id} className="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px]">
                    {s.id}
                  </span>
                ))}
              </div>
            )}
          </div>
        </div>

        <div className="overflow-hidden rounded-lg border border-border/60 bg-card">
          <div className="border-b border-border/60 px-2.5 py-1.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
            Fragment DSL · YAML
          </div>
          <pre
            data-testid="step-fragment"
            className="overflow-x-auto p-2.5 font-mono text-[10.5px] leading-relaxed text-foreground/85"
          >
            {stepFragment(step)}
          </pre>
        </div>
      </div>
    </div>
  )
}
