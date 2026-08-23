"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import { useRouter } from "next/navigation"
import { toast } from "sonner"
import { Check, FastForward } from "lucide-react"

import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceRefusal,
  CreateSurfaceSecondaryAction,
  CreateSurfaceSteps,
  type CreateSurfaceStep,
} from "@/components/layout/create-surface"
import { StepIdentity } from "./create-crew/step-identity"
import { StepLineup } from "./create-crew/step-lineup"
import { StepContainer } from "./create-crew/step-container"
import { BaseImagePanel, effectiveBaseImage, patchImage } from "./create-crew/base-image"
import { StepReview } from "./create-crew/step-review"
import { submitCrew } from "./create-crew/submit"
import { INITIAL_STATE, type WizardState, type WizardStep } from "./create-crew/types"

export interface CreateCrewDialogProps {
  workspaceId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: () => void
}

/**
 * Four steps, and everything that counts them says four.
 *
 * There were five, and the counter beside the title said "step N of 4"
 * because Review was read as the confirmation rather than a question — a
 * wording that survived from an older strip and left the header claiming
 * "step 3 of 4" above a row of five chips.
 *
 * Runtime is gone as a step of its own. Resource limits are an
 * administrator's question and now sit folded inside Container; the egress
 * control went with them, next to the image it applies to. What is left is
 * four questions, counted honestly, and a phone progress bar whose
 * `aria-valuenow` matches its max.
 */
const CREW_STEPS: CreateSurfaceStep[] = [
  { id: "identity", label: "Identity" },
  { id: "lineup", label: "Lineup" },
  { id: "container", label: "Container" },
  { id: "review", label: "Review" },
]

const STEP_DESCRIPTION: Record<WizardStep, string> = {
  1: "Crews group agents that work together. Pick a recognizable icon and name.",
  2: "The agents this crew starts with. Pick a curated lineup, or stay empty and add agents later.",
  3: "The box this crew runs in — image, tooling, network. All optional; skip to defaults if unsure.",
  4: "Last look before commit. Click any section to jump back.",
}

export function CreateCrewDialog({ workspaceId, open, onOpenChange, onCreated }: CreateCrewDialogProps) {
  const router = useRouter()
  const [step, setStep] = useState<WizardStep>(1)
  const [state, setStateFull] = useState<WizardState>(INITIAL_STATE)
  const [busy, setBusy] = useState(false)
  // What the server said when it said no. The toast stays (a wizard that
  // closes on success wants one), but the band is the copy you can still read
  // ten seconds later, and it sits outside the scrollport.
  const [refusal, setRefusal] = useState<string | null>(null)

  // Reset to fresh state every time the dialog re-opens.
  useEffect(() => {
    if (!open) {
      setStep(1)
      setStateFull(INITIAL_STATE)
      setBusy(false)
      setRefusal(null)
    }
  }, [open])

  const setState = useMemo(() => (patch: Partial<WizardState>) => {
    setStateFull((prev) => ({ ...prev, ...patch }))
  }, [])

  // Step validity gates the "Continue" button.
  const stepValid = useMemo(() => stepIsValid(step, state), [step, state])

  const lineupSummary = useMemo(() => deriveLineupSummary(state), [state])

  // What the discard guard protects. Past Step 1 there is always work to lose
  // (the lineup step auto-picks a template on mount, so state alone would say
  // "dirty" a beat later anyway); on Step 1 it is whatever has been typed.
  const dirty = useMemo(
    () => step > 1 || JSON.stringify(state) !== JSON.stringify(INITIAL_STATE),
    [step, state],
  )

  // submittingRef is a synchronous latch — `busy` is only updated on the next
  // render, so two fast clicks (or ⌘+Enter while a click is mid-flight) can
  // both observe busy=false and fire submit twice, creating duplicate crews.
  // The ref flips immediately and gates the second call before any async work.
  const submittingRef = useRef(false)

  const submit = async () => {
    if (submittingRef.current || busy) return
    submittingRef.current = true
    setBusy(true)
    setRefusal(null)
    try {
      const result = await submitCrew(workspaceId, state)
      // applyOverrides() inside submit fires toast.warning when partial=true.
      // Suppress the success toast in that case so the user doesn't see a
      // contradictory pair ("Created" + "Some customizations didn't apply").
      if (!result.partial) {
        toast.success(`Crew "${result.name}" created`)
      }
      onOpenChange(false)
      onCreated()
      router.replace(`/crews?crew=${encodeURIComponent(result.slug)}`)
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e)
      setRefusal(`Could not create crew: ${message}`)
      toast.error(`Could not create crew: ${message}`)
    } finally {
      setBusy(false)
      submittingRef.current = false
    }
  }

  const advance = () => {
    if (step === 4) {
      submit()
      return
    }
    setStep((step + 1) as WizardStep)
  }

  // Inert rather than absent while a create is in flight: the header's back
  // arrow has no disabled state (absent means "no arrow", not "a dead one"),
  // and making it vanish for the length of a POST shifts the title sideways.
  const back = () => {
    if (busy) return
    if (step > 1) setStep((step - 1) as WizardStep)
  }

  // Skip-to-defaults must clear Step 4 overrides — otherwise a user who typed
  // a custom image / devcontainer / mise / MCP and then clicks "Skip to
  // defaults" still has those values land on Review and submit, which
  // contradicts the CTA's promise.
  const skipToReview = () => {
    setState({
      runtimeImage: INITIAL_STATE.runtimeImage,
      devcontainerConfig: INITIAL_STATE.devcontainerConfig,
      miseConfig: INITIAL_STATE.miseConfig,
      mcpConfig: INITIAL_STATE.mcpConfig,
    })
    setStep(4)
  }

  // Auto-focus the primary action when the user lands on Review (Step 5) so
  // ⌘+Enter is unambiguous and screen readers announce "Create crew" first.
  // Step 1's Name input keeps its inline `autoFocus` (mounts fresh each entry).
  //
  // It matters more than it used to: ⌘↵ is now the shell's, wired on the
  // dialog rather than on `window`, so it needs focus to be somewhere inside
  // the surface — and the click that lands you on Review ("Skip to defaults")
  // unmounts the very button that had it. CreateSurfaceFooter exposes no ref
  // to its primary, hence the query: the primary is its last button.
  // The base-image picker, as a panel this surface swaps to rather than a
  // second dialog over it.
  const [panel, setPanel] = useState<null | "image">(null)

  const footerRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (step === 4 && !busy) {
      const buttons = footerRef.current?.querySelectorAll("button")
      buttons?.[buttons.length - 1]?.focus()
    }
  }, [step, busy])

  return (
    <CreateSurface
      open={open}
      onOpenChange={onOpenChange}
      size="lg"
      dirty={dirty}
      discardLabel="this crew"
      onSubmit={() => {
        // ⌘↵ inside the picker closes the picker; it must not also advance the
        // step underneath it.
        if (panel) { setPanel(null); return }
        if (stepValid) advance()
      }}
    >
      {/* The base-image catalogue is a PANEL, not a list on the step: the
          surface swaps its header, body and footer for it and the back arrow
          returns. Same shape as the icon picker, and the reason a nine-item
          catalogue fits on a step that also carries tooling, network and
          sizing. */}
      <CreateSurfaceHeader
        concept="crews"
        context="Crews"
        title={panel === "image" ? "Base image — new crew" : "New crew"}
        description={
          panel === "image"
            ? "What the container starts from. Node 22 is the recommendation for most agent work; the rest are there for a crew that needs a toolchain preinstalled."
            : STEP_DESCRIPTION[step]
        }
        onBack={panel ? () => setPanel(null) : step > 1 ? back : undefined}
        onClose={() => onOpenChange(false)}
        meta={
          panel ? undefined : (
            <span className="max-sm:hidden">
              {step === 4 ? "ready to create" : `step ${step} of 4`}
            </span>
          )
        }
      />

      {/* The kit's step strip, which draws its own landmark — this used to
          wrap it in a second <nav>. Hidden inside a panel: the panel is not a
          step, and a strip saying "3 of 4" over a picker is a lie about where
          you are. */}
      {!panel && (
        <CreateSurfaceSteps
          ariaLabel="Wizard progress"
          steps={CREW_STEPS}
          current={step - 1}
          onJump={(i) => setStep((i + 1) as WizardStep)}
        />
      )}

      <CreateSurfaceBody>
        {panel === "image" && (
          <BaseImagePanel
            value={effectiveBaseImage(state)}
            onChange={(image) => setState(patchImage(state, image))}
          />
        )}
        {!panel && step === 1 && <StepIdentity state={state} setState={setState} />}
        {!panel && step === 2 && <StepLineup state={state} setState={setState} workspaceId={workspaceId} />}
        {!panel && step === 3 && (
          <StepContainer state={state} setState={setState} onPickImage={() => setPanel("image")} />
        )}
        {!panel && step === 4 && (
          <StepReview
            state={state}
            onEdit={(s) => setStep(s)}
            lineupSummary={lineupSummary}
          />
        )}
      </CreateSurfaceBody>

      <CreateSurfaceRefusal message={refusal} onDismiss={() => setRefusal(null)} />

      <div ref={footerRef} className="shrink-0">
        <CreateSurfaceFooter
          // No step counter here. CreateSurfaceSteps already states the
          // position twice over — the numbered chips on a pointer device, and
          // "3 / 4" beside a progress bar on a phone — and the header's meta
          // says it a third time. The footer's job is the keyboard hint.
          hint={
            panel
              ? undefined
              : step === 4
                ? "⌘+Enter to confirm · Esc cancel"
                : "⌘+Enter to continue"
          }
          // Inside the panel, Cancel means "back out of the panel" — the same
          // rule the project modal's icon panel follows, and the reason
          // guardCancel is off there: nothing is discarded by leaving a picker.
          onCancel={panel ? () => setPanel(null) : () => onOpenChange(false)}
          guardCancel={!panel}
          cancelLabel={panel ? "Back" : "Cancel"}
          secondary={
            !panel && step === 3 ? (
              <CreateSurfaceSecondaryAction
                icon={FastForward}
                onClick={skipToReview}
                disabled={busy}
                title="Skip to Review with default container settings"
              >
                Skip to defaults
              </CreateSurfaceSecondaryAction>
            ) : undefined
          }
          primaryLabel={
            panel ? "Use this image" : step === 4 ? (busy ? "Creating…" : "Create crew") : "Continue"
          }
          primaryIcon={!panel && step === 4 ? Check : undefined}
          onPrimary={panel ? () => setPanel(null) : advance}
          primaryDisabled={panel ? false : !stepValid}
          busy={panel ? false : busy}
        />
      </div>
    </CreateSurface>
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
    return true // empty
  }
  // step === 3 (Container) is always valid: image and tooling are optional,
  // an empty allowlist is an explicit choice that locks all egress, and the
  // sizing chips cannot produce a zero — CustomNumberChip refuses anything
  // outside [MIN, MAX] and keeps the previous value.
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
  return { count: 0, source: "empty" }
}
