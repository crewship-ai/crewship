"use client"

import { useEffect, useMemo, useState } from "react"
import { useRouter } from "next/navigation"
import { toast } from "sonner"
import { Check, ChevronRight } from "lucide-react"
import {
  Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle,
} from "@/components/ui/dialog"
import { cn } from "@/lib/utils"
import { StepIdentity } from "./create-crew/step-identity"
import { StepLineup } from "./create-crew/step-lineup"
import { StepRuntime } from "./create-crew/step-runtime"
import { StepReview } from "./create-crew/step-review"
import { submitCrew } from "./create-crew/submit"
import { INITIAL_STATE, type WizardState, type WizardStep } from "./create-crew/types"

export interface CreateCrewDialogProps {
  workspaceId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: () => void
}

const STEP_LABELS: Record<WizardStep, { title: string; sub: string }> = {
  1: { title: "Identity", sub: "icon, color, name" },
  2: { title: "Lineup", sub: "templates · AI · blank" },
  3: { title: "Runtime", sub: "resources, network" },
  4: { title: "Review", sub: "create" },
}

export function CreateCrewDialog({ workspaceId, open, onOpenChange, onCreated }: CreateCrewDialogProps) {
  const router = useRouter()
  const [step, setStep] = useState<WizardStep>(1)
  const [state, setStateFull] = useState<WizardState>(INITIAL_STATE)
  const [busy, setBusy] = useState(false)

  // Reset to fresh state every time the dialog re-opens.
  useEffect(() => {
    if (!open) {
      setStep(1)
      setStateFull(INITIAL_STATE)
      setBusy(false)
    }
  }, [open])

  const setState = useMemo(() => (patch: Partial<WizardState>) => {
    setStateFull((prev) => ({ ...prev, ...patch }))
  }, [])

  // Step validity gates the "Continue" button.
  const stepValid = useMemo(() => stepIsValid(step, state), [step, state])

  const lineupSummary = useMemo(() => deriveLineupSummary(state), [state])

  const submit = async () => {
    if (busy) return
    setBusy(true)
    try {
      const result = await submitCrew(workspaceId, state)
      toast.success(`Crew "${result.name}" created`)
      onOpenChange(false)
      onCreated()
      router.replace(`/crews?crew=${encodeURIComponent(result.slug)}`)
    } catch (e) {
      toast.error(`Could not create crew: ${e instanceof Error ? e.message : String(e)}`)
    } finally {
      setBusy(false)
    }
  }

  const advance = () => {
    if (step === 4) {
      submit()
      return
    }
    setStep((step + 1) as WizardStep)
  }

  const back = () => {
    if (step > 1) setStep((step - 1) as WizardStep)
  }

  // Cmd+Enter advances/submits on supported steps.
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
        e.preventDefault()
        if (stepValid) advance()
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, step, stepValid, state])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className={cn(
          "p-0 overflow-hidden",
          step === 2 ? "sm:max-w-[940px]" : "sm:max-w-[680px]",
        )}
      >
        <DialogHeader className="px-5 pt-4 pb-3 border-b border-white/10">
          <DialogTitle className="text-base">
            New crew
            <span className="ml-2 text-sm text-muted-foreground font-normal">
              {step === 4 ? "— ready to create" : `— step ${step} of 3`}
            </span>
          </DialogTitle>
          <DialogDescription className="text-[12.5px]">
            {step === 1 && "Crews group agents that work together. Pick a recognizable icon and name."}
            {step === 2 && "The agents this crew starts with. Pick a curated lineup, ask AI to generate one, or stay empty."}
            {step === 3 && "Resource limits and network policy for the crew's container. Defaults are sane."}
            {step === 4 && "Last look before commit. Click any section to jump back."}
          </DialogDescription>
        </DialogHeader>

        <StepStrip step={step} onJump={(s) => s < step && setStep(s)} />

        <div className="px-5 py-4 max-h-[58vh] overflow-y-auto">
          {step === 1 && <StepIdentity state={state} setState={setState} />}
          {step === 2 && <StepLineup state={state} setState={setState} />}
          {step === 3 && <StepRuntime state={state} setState={setState} />}
          {step === 4 && (
            <StepReview
              state={state}
              onEdit={(s) => setStep(s)}
              lineupSummary={lineupSummary}
            />
          )}
        </div>

        <div className="px-5 py-3 border-t border-white/10 flex items-center gap-2">
          <span className="text-[11.5px] text-muted-foreground mr-auto">
            {step === 4
              ? "⌘+Enter to confirm · Esc cancel"
              : `Step ${step} of 3 · ⌘+Enter to continue`}
          </span>
          <button
            type="button"
            onClick={() => onOpenChange(false)}
            disabled={busy}
            className="text-sm px-3 py-1.5 rounded text-muted-foreground hover:text-foreground"
          >
            Cancel
          </button>
          {step > 1 && (
            <button
              type="button"
              onClick={back}
              disabled={busy}
              className="text-sm px-3 py-1.5 rounded border border-white/10 text-foreground/80 hover:bg-white/5"
            >
              ← Back
            </button>
          )}
          <button
            type="button"
            onClick={advance}
            disabled={!stepValid || busy}
            className="text-sm px-3.5 py-1.5 rounded bg-blue-500 hover:bg-blue-400 text-white disabled:opacity-40 disabled:cursor-not-allowed flex items-center gap-1.5"
          >
            {busy && <span className="h-3 w-3 rounded-full border-2 border-white/30 border-t-white animate-spin" />}
            {step === 4 ? (busy ? "Creating…" : "✓ Create crew") : "Continue"}
            {step < 4 && !busy && <ChevronRight className="h-3.5 w-3.5" />}
          </button>
        </div>
      </DialogContent>
    </Dialog>
  )
}

function StepStrip({ step, onJump }: { step: WizardStep; onJump: (s: WizardStep) => void }) {
  return (
    <div className="px-5 py-2.5 border-b border-white/10 bg-card/50 flex items-center gap-0">
      {([1, 2, 3] as const).map((n, i) => (
        <div key={n} className="flex items-center flex-1 gap-2 text-[12px] min-w-0">
          <button
            type="button"
            disabled={n >= step}
            onClick={() => onJump(n)}
            className={cn(
              "h-5.5 w-5.5 min-w-[22px] min-h-[22px] rounded-full border text-[11px] font-semibold flex items-center justify-center transition-colors",
              n < step
                ? "bg-emerald-500/20 border-emerald-400/50 text-emerald-300 hover:scale-110"
                : n === step
                  ? "bg-blue-500/20 border-blue-400 text-blue-300"
                  : "bg-card border-white/10 text-muted-foreground",
              n >= step && "cursor-default",
            )}
            aria-label={`Step ${n}: ${STEP_LABELS[n].title}`}
          >
            {n < step ? <Check className="h-3 w-3" /> : n}
          </button>
          <div className={cn("flex flex-col leading-tight min-w-0", n !== step && "opacity-70")}>
            <span className="font-medium truncate">{STEP_LABELS[n].title}</span>
            <span className="text-[10.5px] text-muted-foreground truncate">{STEP_LABELS[n].sub}</span>
          </div>
          {i < 2 && <div className={cn("h-px flex-1 max-w-6 mx-1", n < step ? "bg-emerald-400/40" : "bg-white/10")} />}
        </div>
      ))}
    </div>
  )
}

// =============================================================================
// Helpers
// =============================================================================

const SLUG_RE = /^[a-z0-9][a-z0-9-]*[a-z0-9]$/

function stepIsValid(step: WizardStep, s: WizardState): boolean {
  if (step === 1) {
    return s.name.trim().length >= 2 && s.slug.trim().length >= 2 && SLUG_RE.test(s.slug)
  }
  if (step === 2) {
    if (s.mode === "browse") return !!s.pickedTemplateSlug
    if (s.mode === "ai") return !!s.aiResult && s.aiResult.agents.length > 0
    return true // empty
  }
  if (step === 3) {
    return s.memoryMB > 0 && s.cpus > 0 &&
      (s.networkMode === "free" || s.allowedDomains.length > 0 || s.networkMode === "restricted")
    // restricted with zero domains is allowed (locks all egress) — explicit choice.
  }
  return true
}

function deriveLineupSummary(s: WizardState): { count: number; source: string; agents?: { name: string; agent_role: string }[] } {
  if (s.mode === "browse" && s.pickedTemplateMeta) {
    return {
      count: s.pickedTemplateMeta.agentCount,
      source: `template: ${s.pickedTemplateMeta.name}`,
      agents: s.pickedTemplateMeta.agents,
    }
  }
  if (s.mode === "ai" && s.aiResult) {
    return {
      count: s.aiResult.agents.length,
      source: "AI-generated",
      agents: s.aiResult.agents.map((a) => ({ name: a.name, agent_role: a.agent_role })),
    }
  }
  return { count: 0, source: "empty" }
}
