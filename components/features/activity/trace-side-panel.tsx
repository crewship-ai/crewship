"use client"

import { useEffect, useMemo, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import {
  Activity,
  Braces,
  ExternalLink,
  FileText,
  Inbox,
  ScrollText,
  Send,
  Workflow,
  X,
} from "lucide-react"
import Link from "next/link"
import { cn } from "@/lib/utils"
import { panel } from "@/lib/motion"
import { TabBar } from "@/components/ui/tab-bar"
import type { SubSpan, TraceStep } from "@/lib/trace/types"
import { JSONViewer } from "./json-viewer"
import { OutputView } from "./output-view"
import { SubSpanWaterfall } from "./sub-span-waterfall"
import { extractArtifacts, type Artifact } from "@/lib/trace/extract-artifacts"
import { toast } from "sonner"

// TraceSidePanel — right-side panel that opens when a step node is
// selected on the canvas. Tabs: Input / Output / Logs / Files.
// Phase 1 ships the shell + tab layout; the inner viewers (JSON, log
// renderer, artifact list) come in Phase 3-4.
//
// Why a panel over a modal: n8n's pattern. A modal would force the
// user to dismiss it before re-orienting on the canvas; a persistent
// right panel lets them click step → step → step and watch the panel
// hot-swap without losing canvas focus.

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
  // Phase-3 placeholder — the resolved input map for this step
  // (rendered prompt, http body after templating, etc). Populated
  // server-side in Phase 3 (`/api/v1/internal/pipeline-runs/{id}` is
  // already pre-parsed; resolving inputs is FE-side from DSL +
  // step_outputs of upstream steps).
  resolvedInput?: unknown
  // Run-level error, surfaced under Logs when this step is the one
  // marked `failed_at_step`.
  errorMessage?: string
  isFailedStep?: boolean
  // Agent-internal tool calls for the selected step (mapped from
  // run.sub_spans[step.id]) — drives the Actions waterfall tab.
  subSpans?: SubSpan[]
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
  resolvedInput,
  errorMessage,
  isFailedStep,
  subSpans,
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
  }, [step?.id, hasActions])

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
            {tab === "input" && <InputView step={step} resolved={resolvedInput} />}
            {tab === "output" && <OutputView value={output} />}
            {tab === "logs" && (
              <LogsView errorMessage={isFailedStep ? errorMessage : undefined} />
            )}
            {tab === "files" && <FilesView step={step} output={output} />}
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

// ── Tab bodies — minimal Phase-1 viewers; richer Table/JSON/Schema
//    toggles + artifact rendering land in Phase 3-4.

function InputView({ step, resolved }: { step: TraceStep; resolved?: unknown }) {
  // Show whichever raw input the DSL declares. The fully
  // template-resolved value (`{{ steps.X.output }}` substituted)
  // is what `resolved` carries when the caller has it; the run
  // record doesn't currently persist resolved inputs per step, so
  // most of the time we fall back to the declared shape.
  const declared = pickDeclaredInput(step)
  return (
    <div className="space-y-3">
      {resolved !== undefined && (
        <Section label="Resolved">
          <JSONViewer value={resolved} />
        </Section>
      )}
      <Section label={resolved !== undefined ? "Declared (DSL)" : "Input"}>
        <JSONViewer value={declared} />
      </Section>
    </div>
  )
}

function LogsView({ errorMessage }: { errorMessage?: string }) {
  if (errorMessage) {
    // Route the failure body through OutputView so a stack trace, YAML
    // error block, or JSON problem-detail highlights like it does in
    // chat — wrapped in a rose error frame to keep its "this failed"
    // affordance.
    return (
      <div className="space-y-1.5">
        <div className="text-[10px] uppercase tracking-wider text-rose-300/80">
          Error
        </div>
        <div className="rounded border border-rose-500/30 bg-rose-500/5 p-1.5">
          <OutputView value={errorMessage} emptyLabel="No error detail." />
        </div>
      </div>
    )
  }
  return (
    <div className="flex h-32 flex-col items-center justify-center gap-2 text-center">
      <ScrollText className="h-6 w-6 text-muted-foreground/30" />
      <div className="text-xs text-muted-foreground/60">
        Per-step logs land in Phase 3.
      </div>
    </div>
  )
}

function FilesView({ step, output }: { step: TraceStep; output: unknown }) {
  // Memoize so a 1MB http response doesn't get regex-scanned + JSON-
  // parsed on every parent re-render (panel resize, polling tick, …).
  const artifacts = useMemo(
    () => extractArtifacts(step.type, output),
    [step.type, output],
  )
  const [activeArtifact, setActiveArtifact] = useState<Artifact | null>(null)
  // Drop the open artifact when the user switches to a different
  // step. Keying only on step.id (not output) means a 3s polling
  // tick that re-creates the run object — and therefore the output
  // reference — won't yank the artifact viewer out from under the
  // user mid-inspection.
  useEffect(() => {
    setActiveArtifact(null)
  }, [step.id])

  if (artifacts.length === 0) {
    return (
      <div className="flex h-32 flex-col items-center justify-center gap-2 text-center">
        <FileText className="h-6 w-6 text-muted-foreground/30" />
        <div className="text-xs text-muted-foreground/60">
          No artifacts detected.
        </div>
      </div>
    )
  }
  return (
    <div className="space-y-3">
      <ul className="space-y-1.5">
        {artifacts.map((a) => (
          <ArtifactRow
            key={`${a.kind}:${a.name}`}
            artifact={a}
            active={activeArtifact?.name === a.name}
            onToggleActive={() =>
              setActiveArtifact((prev) => (prev?.name === a.name ? null : a))
            }
          />
        ))}
      </ul>

      {activeArtifact?.kind === "json" && (
        <div className="border-t border-white/[0.06] pt-3">
          <JSONViewer value={activeArtifact.content} />
        </div>
      )}
    </div>
  )
}

function ArtifactRow({
  artifact,
  active,
  onToggleActive,
}: {
  artifact: Artifact
  active: boolean
  onToggleActive: () => void
}) {
  const [busy, setBusy] = useState(false)

  const onClick = async () => {
    if (artifact.kind !== "file_ref") {
      onToggleActive()
      return
    }
    // No way to open a file in the user's editor from a web context;
    // copying the path is the most useful thing we can do until the
    // executor persists artifacts as addressable resources. Disable
    // the row while writing so a rapid double-click doesn't fire two
    // racing clipboard writes + two toasts.
    if (busy) return
    const cb = typeof navigator !== "undefined" ? navigator.clipboard : undefined
    if (!cb) {
      toast.error("Clipboard unavailable")
      return
    }
    setBusy(true)
    try {
      await cb.writeText(String(artifact.content))
      toast.success(`Copied ${artifact.name}`)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Copy failed")
    } finally {
      setBusy(false)
    }
  }

  return (
    <li>
      <button
        type="button"
        onClick={onClick}
        disabled={busy}
        className={cn(
          "flex w-full items-center gap-2 rounded border border-white/[0.06] bg-background px-2 py-1.5 text-left text-xs transition-colors hover:bg-white/[0.04] disabled:opacity-60",
          active && "border-blue-500/40 bg-blue-500/5",
        )}
      >
        {artifact.kind === "json" ? (
          <Braces className="h-3 w-3 shrink-0 text-blue-300" />
        ) : (
          <FileText className="h-3 w-3 shrink-0 text-muted-foreground/60" />
        )}
        <span className="truncate font-mono">{artifact.name}</span>
        <span className="ml-auto truncate text-[10px] text-muted-foreground/50">
          {artifact.preview}
        </span>
      </button>
    </li>
  )
}

// ── small primitives ────────────────────────────────────────────────

function Section({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="mb-1 text-[10px] uppercase tracking-wider text-muted-foreground/60">
        {label}
      </div>
      {children}
    </div>
  )
}

function pickDeclaredInput(step: TraceStep): unknown {
  switch (step.type) {
    case "agent_run":
      return step.prompt ?? null
    case "http":
      return step.http ?? null
    case "transform":
      return step.transform ?? null
    case "code":
      return step.code ?? null
    case "wait":
      return step.wait ?? null
    case "call_pipeline":
      return { pipeline_slug: step.pipeline_slug, inputs: step.inputs }
    default:
      return null
  }
}

