"use client"

import { useEffect, useMemo, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import {
  Activity,
  Check,
  Download,
  ExternalLink,
  FileText,
  Inbox,
  Loader2,
  ScrollText,
  Send,
  Workflow,
  X,
} from "lucide-react"
import Link from "next/link"
import { cn } from "@/lib/utils"
import { panel } from "@/lib/motion"
import { formatDurationDecimal } from "@/lib/time"
import { TabBar } from "@/components/ui/tab-bar"
import { Spinner } from "@/components/ui/spinner"
import { Skeleton } from "@/components/ui/skeleton"
import type { SubSpan, SubSpanStatus, TraceStep } from "@/lib/trace/types"
import {
  resolveStepInput,
  type ResolvedInputEntry,
} from "@/lib/trace/resolve-step-input"
import {
  collectStepFiles,
  type StepFile,
  type StepFileTouch,
} from "@/lib/trace/collect-step-files"
import { JSONViewer } from "./json-viewer"
import { OutputView } from "./output-view"
import { SubSpanWaterfall } from "./sub-span-waterfall"
import { SubSpanIcon, SUB_SPAN_STATUS_COLOR } from "./sub-span-visual"
import { useAgentFile } from "@/hooks/use-agent-file"
import { toast } from "sonner"

// TraceSidePanel — right-side panel that opens when a step node is
// selected on the canvas. Tabs: Actions / Input / Output / Logs / Files.
//
// Each tab renders REAL run data:
//   - Actions : the agent's sub-spans as a waterfall (SubSpanWaterfall)
//   - Input   : step DSL inputs with {{ … }} refs resolved against the
//               run's step_outputs + inputs (resolveStepInput)
//   - Output  : the step's output through the shared OutputView
//   - Logs    : the sub-spans as a compact "what it did" activity log,
//               plus the run error when this step failed
//   - Files   : the files the step touched — sub-span artifact paths +
//               output-inferred refs — fetched + rendered inline
//
// Why a panel over a modal: n8n's pattern. A persistent right panel lets
// the user click step → step → step and watch it hot-swap without
// losing canvas focus.

export type SidePanelTab = "actions" | "input" | "output" | "logs" | "files"

// Context links surfaced under the panel — the agent session the run
// was authored in + the routine it belongs to. Both optional: an
// older run may lack a chat_id, and the routine slug is always known.
export interface TraceContextLinks {
  // /chat/{agentSlug}?session={chatId} when both are known.
  agentSlug?: string
  chatId?: string
  routineSlug?: string
  routineName?: string
}

interface TraceSidePanelProps {
  open: boolean
  step: TraceStep | null
  // Output for the selected step, parsed from the run's
  // step_outputs map. `undefined` = step hasn't completed yet.
  output?: unknown
  // The full run.step_outputs map + run.inputs — used to resolve the
  // selected step's `{{ steps.X.output }}` / `{{ inputs.Y }}` refs for
  // the Input tab. Read-only; never persisted client-side.
  stepOutputs?: Record<string, unknown> | null
  runInputs?: Record<string, unknown> | null
  // Run-level error, surfaced under Logs when this step is the one
  // marked `failed_at_step`.
  errorMessage?: string
  isFailedStep?: boolean
  // Agent-internal tool calls for the selected step (mapped from
  // run.sub_spans[step.id]) — drives the Actions waterfall + Logs tab +
  // the Files tab's artifact list.
  subSpans?: SubSpan[]
  // Resolved agent id (step.agent_slug → id, falling back to the run's
  // invoking agent) + workspace — needed to download the files the step
  // touched from `/api/v1/agents/{id}/files/download`.
  agentId?: string | null
  workspaceId?: string | null
  // Context links (session + routine).
  context?: TraceContextLinks
  onClose: () => void
}

const TABS: ReadonlyArray<{ id: SidePanelTab; label: string; Icon: typeof Send }> = [
  { id: "actions", label: "Actions", Icon: Activity },
  { id: "input", label: "Input", Icon: Send },
  { id: "output", label: "Output", Icon: Inbox },
  { id: "logs", label: "Logs", Icon: ScrollText },
  { id: "files", label: "Files", Icon: FileText },
]

export function TraceSidePanel({
  open,
  step,
  output,
  stepOutputs,
  runInputs,
  errorMessage,
  isFailedStep,
  subSpans,
  agentId,
  workspaceId,
  context,
  onClose,
}: TraceSidePanelProps) {
  const spans = useMemo(() => subSpans ?? [], [subSpans])
  const hasActions = spans.length > 0
  const [tab, setTab] = useState<SidePanelTab>("output")

  // When a step is (re)selected, jump to the Actions waterfall if it
  // has any — that's the drill-down the user came for. Falls back to
  // Output for steps with no recorded agent activity. Keyed on step.id
  // so re-renders from polling don't yank the user off their tab.
  useEffect(() => {
    setTab(hasActions ? "actions" : "output")
    // Reset ONLY when the selected step changes. Keying on hasActions too
    // meant a poll that flips it false→true (sub-spans arriving late) would
    // yank the user back to Actions after they'd switched tabs.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [step?.id])

  const copyArtifact = (path: string) => {
    const cb = typeof navigator !== "undefined" ? navigator.clipboard : undefined
    if (!cb) {
      toast.error("Clipboard unavailable")
      return
    }
    cb.writeText(path)
      .then(() => toast.success(`Copied ${path}`))
      .catch((err) => toast.error(err instanceof Error ? err.message : "Copy failed"))
  }

  return (
    <AnimatePresence>
      {open && step && (
        <motion.aside
          role="complementary"
          aria-label="Step detail"
          initial={panel.side.initial}
          animate={panel.side.animate}
          exit={panel.side.exit}
          transition={panel.side.transition}
          className="flex h-full w-full flex-col border-l border-white/[0.06] bg-card"
        >
          {/* Header */}
          <div className="flex shrink-0 items-center gap-2 border-b border-white/[0.06] px-3 py-2">
            <button
              type="button"
              onClick={onClose}
              aria-label="Close detail"
              className="rounded p-1 text-muted-foreground/50 hover:text-foreground"
            >
              <X className="h-3.5 w-3.5" />
            </button>
            <div className="min-w-0 flex-1">
              <div className="truncate font-mono text-xs">{step.id}</div>
              <div className="text-[10px] uppercase tracking-wider text-muted-foreground/60">
                {step.type}
              </div>
            </div>
          </div>

          {/* Tab strip */}
          <TabBar
            value={tab}
            onValueChange={(v) => setTab(v as SidePanelTab)}
            layoutId="trace-side-panel-tabs"
            ariaLabel="Step detail view"
            className="shrink-0 px-1"
          >
            {TABS.map(({ id, label, Icon }) => (
              <TabBar.Item key={id} value={id} className="text-[11px]">
                <span className="inline-flex items-center gap-1">
                  <Icon className="h-3 w-3" />
                  {label}
                </span>
              </TabBar.Item>
            ))}
          </TabBar>

          {/* Body */}
          <div className="min-h-0 flex-1 overflow-y-auto p-3">
            {tab === "actions" && (
              <SubSpanWaterfall spans={spans} onOpenArtifact={copyArtifact} />
            )}
            {tab === "input" && (
              <InputView step={step} stepOutputs={stepOutputs} runInputs={runInputs} />
            )}
            {tab === "output" && (
              <OutputView
                value={output}
                emptyLabel="This step produced no output yet."
              />
            )}
            {tab === "logs" && (
              <LogsView
                spans={spans}
                errorMessage={isFailedStep ? errorMessage : undefined}
              />
            )}
            {tab === "files" && (
              <FilesView
                step={step}
                output={output}
                spans={spans}
                agentId={agentId}
                workspaceId={workspaceId}
              />
            )}
          </div>

          {/* Context — links back to the session the agent authored this
            * in + the routine it belongs to. Always-visible footer so
            * it's reachable from any tab. */}
          {context && <ContextLinks step={step} context={context} />}
        </motion.aside>
      )}
    </AnimatePresence>
  )
}

// ContextLinks — the "where did this come from" footer. Session link
// resolves to /chat/{agentSlug}?session={chatId} (the agent + session
// that produced this run) when both are known; routine link goes to
// the routine the run belongs to. Either may be absent.
function ContextLinks({
  step,
  context,
}: {
  step: TraceStep
  context: TraceContextLinks
}) {
  // Prefer the per-step agent slug (the agent step that owns the
  // selected span); fall back to the run-level agent.
  const agentSlug = step.agent_slug || context.agentSlug
  const sessionHref =
    agentSlug && context.chatId
      ? `/chat/${encodeURIComponent(agentSlug)}?session=${encodeURIComponent(context.chatId)}`
      : null
  const routineHref = context.routineSlug
    ? `/routines?slug=${encodeURIComponent(context.routineSlug)}`
    : null

  if (!sessionHref && !routineHref) return null

  return (
    <div className="shrink-0 space-y-1.5 border-t border-white/[0.06] bg-card/60 px-3 py-2.5">
      <div className="text-[10px] uppercase tracking-wider text-muted-foreground/50">
        Context
      </div>
      {sessionHref && (
        <Link
          href={sessionHref}
          className="flex items-center gap-2 rounded border border-white/[0.08] bg-background px-2 py-1.5 text-[11px] transition-colors hover:border-blue-500/40 hover:bg-blue-500/5"
        >
          <ExternalLink className="h-3 w-3 shrink-0 text-blue-300" />
          <span>Open the agent session</span>
          <span className="ml-auto truncate font-mono text-muted-foreground/50">
            {agentSlug}
          </span>
        </Link>
      )}
      {routineHref && (
        <Link
          href={routineHref}
          className="flex items-center gap-2 rounded border border-white/[0.08] bg-background px-2 py-1.5 text-[11px] transition-colors hover:border-emerald-500/40 hover:bg-emerald-500/5"
        >
          <Workflow className="h-3 w-3 shrink-0 text-emerald-300" />
          <span className="truncate">
            Routine · {context.routineName || context.routineSlug}
          </span>
        </Link>
      )}
    </div>
  )
}

// ── Input tab ────────────────────────────────────────────────────────
//
// The step's declared DSL inputs with `{{ steps.X.output }}` /
// `{{ inputs.Y }}` refs resolved against the run's step outputs +
// inputs (display-only — see resolveStepInput). Entries with a resolved
// ref get a "resolved" affordance so the user can tell what flowed in
// from upstream vs. what was hard-coded.

function InputView({
  step,
  stepOutputs,
  runInputs,
}: {
  step: TraceStep
  stepOutputs?: Record<string, unknown> | null
  runInputs?: Record<string, unknown> | null
}) {
  const entries = useMemo(
    () => resolveStepInput(step, { inputs: runInputs, stepOutputs }),
    [step, stepOutputs, runInputs],
  )

  if (entries.length === 0) {
    return <EmptyTab Icon={Send} text="This step declares no input." />
  }

  return (
    <div className="space-y-3">
      {entries.map((e) => (
        <InputEntry key={e.key} entry={e} />
      ))}
    </div>
  )
}

function InputEntry({ entry }: { entry: ResolvedInputEntry }) {
  const { key, value, hasRefs } = entry
  const isStructured = value !== null && typeof value === "object"
  const asText = typeof value === "string" ? value : null
  const isLong = asText !== null && (asText.length > 80 || asText.includes("\n"))

  return (
    <div>
      <div className="mb-1 flex items-center gap-1.5">
        <span className="font-mono text-[10px] uppercase tracking-wider text-muted-foreground/60">
          {key}
        </span>
        {hasRefs && (
          <span
            className="rounded border border-blue-500/30 px-1 text-[9px] text-blue-300"
            title="Resolved from upstream step output / run inputs"
          >
            resolved
          </span>
        )}
      </div>
      {isStructured || isLong ? (
        <OutputView value={value} />
      ) : (
        <code className="block break-words rounded bg-background/60 px-2 py-1 font-mono text-[11px] text-foreground/85">
          {asText ?? String(value)}
        </code>
      )}
    </div>
  )
}

// ── Logs tab ─────────────────────────────────────────────────────────
//
// Raw per-step stdout is a backend gap; the sub-spans ARE the per-step
// activity record, so we present them as the log — one ordered row per
// action (kind · name · duration · status) — plus the run error when
// this step is the one that failed.

function LogsView({
  spans,
  errorMessage,
}: {
  spans: SubSpan[]
  errorMessage?: string
}) {
  if (spans.length === 0 && !errorMessage) {
    return <EmptyTab Icon={ScrollText} text="No activity recorded for this step." />
  }

  return (
    <div className="space-y-3">
      {errorMessage && (
        <div className="space-y-1.5">
          <div className="text-[10px] uppercase tracking-wider text-rose-300/80">
            Error
          </div>
          <div className="rounded border border-rose-500/30 bg-rose-500/5 p-1.5">
            <OutputView value={errorMessage} emptyLabel="No error detail." />
          </div>
        </div>
      )}

      {spans.length > 0 && (
        <ol className="space-y-0.5">
          {spans.map((span, i) => (
            <li
              key={`${span.kind}:${span.name}:${i}`}
              className="grid grid-cols-[16px_18px_1fr_auto] items-center gap-2 rounded px-1 py-1 text-[11px] hover:bg-white/[0.03]"
            >
              <span className="text-right font-mono text-[9px] text-muted-foreground/40">
                {i + 1}
              </span>
              <span className="grid place-items-center">
                <SubSpanIcon
                  kind={span.kind}
                  tool={span.attributes.tool}
                  className="h-3.5 w-3.5"
                />
              </span>
              <span className="flex min-w-0 items-center gap-1.5">
                <span className="truncate font-medium text-foreground">
                  {span.name}
                </span>
                <span className="shrink-0 font-mono text-[9px] uppercase tracking-wider text-muted-foreground/40">
                  {span.kind}
                </span>
              </span>
              <span className="flex shrink-0 items-center gap-2">
                {typeof span.durationMs === "number" && (
                  <span className="font-mono text-[10px] text-muted-foreground/50">
                    {formatDurationDecimal(span.durationMs)}
                  </span>
                )}
                <StatusGlyph status={span.status} />
              </span>
            </li>
          ))}
        </ol>
      )}
    </div>
  )
}

function StatusGlyph({ status }: { status: SubSpanStatus }) {
  const Icon =
    status === "running" ? Loader2 : status === "error" ? X : Check
  return (
    <Icon
      className={cn(
        "h-3 w-3",
        SUB_SPAN_STATUS_COLOR[status],
        status === "running" && "animate-spin",
      )}
    />
  )
}

// ── Files tab (headline) ─────────────────────────────────────────────
//
// The files the step touched: sub-span artifact paths (the files the
// agent actually wrote/read) + paths/JSON inferred from the output.
// Clicking a fetchable file downloads it from the agent files endpoint
// and renders it inline through OutputView; inline JSON artifacts render
// from their carried content without a fetch.

function FilesView({
  step,
  output,
  spans,
  agentId,
  workspaceId,
}: {
  step: TraceStep
  output: unknown
  spans: SubSpan[]
  agentId?: string | null
  workspaceId?: string | null
}) {
  const files = useMemo(
    () => collectStepFiles(spans, step.type, output),
    [spans, step.type, output],
  )
  const [activeIdx, setActiveIdx] = useState<number | null>(null)

  // Drop the open file when the user switches steps. Keying only on
  // step.id (not the file list) means a 3s poll re-creating the run
  // object won't yank the viewer out mid-inspection.
  useEffect(() => {
    setActiveIdx(null)
  }, [step.id])

  const active = activeIdx !== null ? files[activeIdx] ?? null : null
  const canFetch = Boolean(active?.fetchable && agentId && workspaceId)

  // Single fetch slot for the active fetchable file. Disabled (nulls)
  // when nothing's open, the active artifact is inline, or we couldn't
  // resolve the agent id.
  const { content, loading, error } = useAgentFile(
    canFetch ? agentId : null,
    canFetch ? workspaceId : null,
    canFetch && active ? active.path : null,
  )

  if (files.length === 0) {
    return (
      <EmptyTab
        Icon={FileText}
        text="No files touched by this step."
      />
    )
  }

  return (
    <div className="space-y-3">
      <ul className="space-y-1.5">
        {files.map((f, i) => (
          <FileRow
            key={`${f.fetchable ? "F" : "I"}:${f.path}:${i}`}
            file={f}
            active={activeIdx === i}
            agentId={agentId}
            workspaceId={workspaceId}
            onToggle={() => setActiveIdx((prev) => (prev === i ? null : i))}
          />
        ))}
      </ul>

      {active && (
        <div className="border-t border-white/[0.06] pt-3">
          <FileViewerBody
            file={active}
            canFetch={canFetch}
            content={content}
            loading={loading}
            error={error}
          />
        </div>
      )}
    </div>
  )
}

function FileRow({
  file,
  active,
  agentId,
  workspaceId,
  onToggle,
}: {
  file: StepFile
  active: boolean
  agentId?: string | null
  workspaceId?: string | null
  onToggle: () => void
}) {
  const downloadHref =
    file.fetchable && agentId && workspaceId
      ? `/api/v1/agents/${agentId}/files/download?workspace_id=${encodeURIComponent(
          workspaceId,
        )}&path=${encodeURIComponent(file.path)}`
      : null

  return (
    <li
      className={cn(
        "rounded border border-white/[0.06] bg-background transition-colors",
        active && "border-blue-500/40 bg-blue-500/5",
      )}
    >
      <div className="flex items-center gap-2 px-2 py-1.5">
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={active}
          className="flex min-w-0 flex-1 items-center gap-2 text-left text-xs"
        >
          <FileText
            className={cn(
              "h-3.5 w-3.5 shrink-0",
              file.source === "action" ? "text-amber-300" : "text-muted-foreground/60",
            )}
          />
          <span className="flex min-w-0 flex-col">
            <span className="truncate font-mono text-foreground">{file.name}</span>
            {file.path !== file.name && (
              <span className="truncate font-mono text-[9px] text-muted-foreground/40">
                {file.path}
              </span>
            )}
          </span>
          <FileProvenance file={file} />
        </button>
        {downloadHref && (
          <a
            href={downloadHref}
            download={file.name}
            onClick={(e) => e.stopPropagation()}
            title="Download file"
            aria-label={`Download ${file.name}`}
            className="shrink-0 rounded p-1 text-muted-foreground/50 transition-colors hover:bg-white/[0.06] hover:text-foreground"
          >
            <Download className="h-3 w-3" />
          </a>
        )}
      </div>
    </li>
  )
}

// FileProvenance — chips telling the user which actions touched the file
// (write / read / bash …) for action-sourced files, or an "output"
// marker for files only inferred from the step's text output.
function FileProvenance({ file }: { file: StepFile }) {
  if (file.source !== "action" || file.touchedBy.length === 0) {
    return (
      <span className="ml-auto shrink-0 rounded border border-white/[0.08] px-1 text-[9px] text-muted-foreground/50">
        output
      </span>
    )
  }
  const shown = file.touchedBy.slice(0, 3)
  const extra = file.touchedBy.length - shown.length
  return (
    <span className="ml-auto flex shrink-0 items-center gap-1">
      {shown.map((t: StepFileTouch, i: number) => (
        <span
          key={`${t.kind}:${t.name}:${i}`}
          className="rounded border border-amber-500/30 px-1 text-[9px] text-amber-300"
          title={`${t.name} (${t.kind})`}
        >
          {t.kind}
        </span>
      ))}
      {extra > 0 && (
        <span className="text-[9px] text-muted-foreground/40">+{extra}</span>
      )}
    </span>
  )
}

function FileViewerBody({
  file,
  canFetch,
  content,
  loading,
  error,
}: {
  file: StepFile
  canFetch: boolean
  content: string | null
  loading: boolean
  error: unknown
}) {
  // Inline artifact (JSON / text inferred from output) — render its
  // carried content directly, no fetch.
  if (!file.fetchable) {
    if (file.inlineKind === "json") {
      return <JSONViewer value={file.inlineContent} />
    }
    return <OutputView value={file.inlineContent} emptyLabel="Empty file." />
  }

  // Fetchable file, but we couldn't resolve the agent — be honest. The
  // download button is ALSO suppressed in this state (FileRow gates it on the
  // same agentId/workspaceId), so don't point users at a control they can't see.
  if (!canFetch) {
    return (
      <div className="rounded border border-amber-500/20 bg-amber-500/5 px-2 py-3 text-center text-[11px] text-amber-200/80">
        Can&apos;t preview or download this file — the agent that wrote it
        isn&apos;t resolvable from this run.
      </div>
    )
  }

  if (loading) {
    return (
      <div className="space-y-2">
        <div className="flex items-center gap-2 text-[11px] text-muted-foreground/60">
          <Spinner className="h-3.5 w-3.5" />
          Loading {file.name}…
        </div>
        <Skeleton className="h-24 w-full rounded" />
      </div>
    )
  }

  if (error) {
    return (
      <div className="rounded border border-rose-500/30 bg-rose-500/5 px-2 py-3 text-center text-[11px] text-rose-300/80">
        Couldn&apos;t load {file.name}
        {error instanceof Error ? ` — ${error.message}` : ""}.
      </div>
    )
  }

  return <OutputView value={content} emptyLabel="Empty file." />
}

// ── small primitives ────────────────────────────────────────────────

function EmptyTab({ Icon, text }: { Icon: typeof FileText; text: string }) {
  return (
    <div className="flex h-32 flex-col items-center justify-center gap-2 text-center">
      <Icon className="h-6 w-6 text-muted-foreground/30" />
      <div className="text-xs text-muted-foreground/60">{text}</div>
    </div>
  )
}
