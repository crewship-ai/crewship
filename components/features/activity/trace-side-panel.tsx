"use client"

import { useMemo, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { Braces, FileText, Inbox, ScrollText, Send, X } from "lucide-react"
import { cn } from "@/lib/utils"
import type { TraceStep } from "@/lib/trace/types"
import { JSONViewer } from "./json-viewer"
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

export type SidePanelTab = "input" | "output" | "logs" | "files"

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
  onClose: () => void
}

const TABS: ReadonlyArray<{ id: SidePanelTab; label: string; Icon: typeof Send }> = [
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
  onClose,
}: TraceSidePanelProps) {
  const [tab, setTab] = useState<SidePanelTab>("output")

  return (
    <AnimatePresence>
      {open && step && (
        <motion.aside
          role="complementary"
          aria-label="Step detail"
          initial={{ x: 360, opacity: 0 }}
          animate={{ x: 0, opacity: 1 }}
          exit={{ x: 360, opacity: 0 }}
          transition={{ type: "spring", damping: 28, stiffness: 320 }}
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
          <div className="flex shrink-0 items-center gap-0 border-b border-white/[0.06] px-1">
            {TABS.map(({ id, label, Icon }) => (
              <button
                key={id}
                type="button"
                onClick={() => setTab(id)}
                aria-pressed={tab === id}
                className={cn(
                  "flex items-center gap-1 border-b-2 px-2.5 py-1.5 text-[11px] font-medium transition-colors",
                  tab === id
                    ? "border-blue-400 text-blue-300"
                    : "border-transparent text-muted-foreground/60 hover:text-foreground/80",
                )}
              >
                <Icon className="h-3 w-3" />
                {label}
              </button>
            ))}
          </div>

          {/* Body */}
          <div className="min-h-0 flex-1 overflow-y-auto p-3">
            {tab === "input" && <InputView step={step} resolved={resolvedInput} />}
            {tab === "output" && <OutputView output={output} />}
            {tab === "logs" && (
              <LogsView errorMessage={isFailedStep ? errorMessage : undefined} />
            )}
            {tab === "files" && <FilesView step={step} output={output} />}
          </div>
        </motion.aside>
      )}
    </AnimatePresence>
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

function OutputView({ output }: { output: unknown }) {
  if (output === undefined || output === null || output === "") {
    return (
      <div className="flex h-32 items-center justify-center text-xs text-muted-foreground/50">
        No output yet.
      </div>
    )
  }
  return <JSONViewer value={output} />
}

function LogsView({ errorMessage }: { errorMessage?: string }) {
  if (errorMessage) {
    return (
      <div className="rounded border border-rose-500/30 bg-rose-500/10 p-2 font-mono text-[11px] text-rose-300">
        {errorMessage}
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
          <li key={`${a.kind}:${a.name}`}>
            <button
              type="button"
              onClick={() => {
                if (a.kind === "file_ref") {
                  // No way to open a file in the user's editor from
                  // a web context; copying the path is the most
                  // useful thing we can do until the executor
                  // persists artifacts as addressable resources.
                  navigator.clipboard?.writeText(String(a.content)).catch(() => {})
                  toast.success(`Copied ${a.name}`)
                  return
                }
                setActiveArtifact((prev) => (prev?.name === a.name ? null : a))
              }}
              className={cn(
                "flex w-full items-center gap-2 rounded border border-white/[0.06] bg-background px-2 py-1.5 text-left text-xs transition-colors hover:bg-white/[0.04]",
                activeArtifact?.name === a.name && "border-blue-500/40 bg-blue-500/5",
              )}
            >
              {a.kind === "json" ? (
                <Braces className="h-3 w-3 shrink-0 text-blue-300" />
              ) : (
                <FileText className="h-3 w-3 shrink-0 text-muted-foreground/60" />
              )}
              <span className="truncate font-mono">{a.name}</span>
              <span className="ml-auto truncate text-[10px] text-muted-foreground/50">
                {a.preview}
              </span>
            </button>
          </li>
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

