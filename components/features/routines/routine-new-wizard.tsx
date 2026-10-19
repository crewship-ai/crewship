"use client"

import * as React from "react"
import { useRouter } from "next/navigation"
import { CalendarClock, CheckCircle2, Clock, Gauge, List, Shield, Sparkles, Users } from "lucide-react"

import { toast } from "@/lib/toast"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { CrewIcon } from "@/components/ui/crew-icon"
import { WizardSteps } from "@/components/ui/wizard-steps"
import { IconPickerDialog } from "@/components/ui/icon-picker-dialog"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceDisclosure,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceNotice,
  CreateSurfaceRefusal,
  CreateSurfaceSecondaryAction,
} from "@/components/layout/create-surface"
import { useUserPreference } from "@/hooks/use-user-preference"
import type { Pipeline } from "@/hooks/use-pipelines"
import { saveRoutineDraft, RoutineDraftError } from "@/lib/routine-drafts"
import { resolveRoutineColor, resolveRoutineIcon } from "@/lib/routine-identity"
import {
  SLUG_RE,
  buildDefinition,
  buildDraftDocument,
  buildSaveBody,
  cronFor,
  describeMessage,
  leadOf,
  newStep,
  onceProblem,
  parseValidationErrors,
  requiresReview,
  sampleInputs,
  slugFromName,
  stepProblems,
  whenText,
  wizardFromDefinition,
  type WizardAgent,
  type WizardState,
} from "@/lib/routine-wizard"
import {
  BasicsPane,
  LimitsPane,
  StepsPane,
  TestPane,
  WhenPane,
  formatStart,
  type NextStarts,
  type SlugStatus,
  type StartMode,
  type TestState,
  type WizardCrew,
} from "./routine-wizard-panes"

// New routine — one wizard, five steps (Basics · When · Steps · Limits ·
// Test and save), mirroring how the server models a routine: one definition
// owned by one crew, a trigger that the save itself can create (manual,
// schedule or once — a webhook or an event is added on the routine page
// afterwards), and a save that needs a passing test run from the last five
// minutes. A web call, credentials or outside hosts send it to a Manager
// review instead of turning it on (internal/pipeline/risk.go).

const STEPS = ["Basics", "When", "Steps", "Limits", "Test and save"]
const TOKEN_TTL_MS = 5 * 60_000

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspaceId: string
  routines: Pipeline[]
  /** Open a routine page (the saved routine or the new draft). */
  onOpenRoutine: (slug: string) => void
  /** Something was created — refresh the list. */
  onCreated?: () => void
  /** "Have a file? Import a routine". */
  onImport?: () => void
}

function browserZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC"
  } catch {
    return "UTC"
  }
}

function initialState(): WizardState {
  return {
    name: "",
    slug: "",
    description: "",
    crewId: null,
    trigger: {
      kind: "schedule",
      preset: "week",
      minute: 0,
      time: "08:00",
      weekday: new Date().getDay(),
      cron: "0 8 * * 1",
      timezone: browserZone(),
      catchup: "once",
      maxFailures: 5,
      fireAt: "",
    },
    slash: false,
    inputs: [],
    steps: [newStep("agent", [])],
    maxCost: "",
    concurrency: "queue",
    sideBySide: 3,
    extra: {},
  }
}

export function RoutineNewWizard({ open, onOpenChange, workspaceId, routines, onOpenRoutine, onCreated, onImport }: Props) {
  const router = useRouter()
  const base = `/api/v1/workspaces/${encodeURIComponent(workspaceId)}`
  const [step, setStep] = React.useState(0)
  const [state, setState] = React.useState<WizardState>(initialState)
  const [start, setStart] = React.useState<StartMode>("build")
  const [goal, setGoal] = React.useState("")
  const [slugEdited, setSlugEdited] = React.useState(false)
  const [slugStatus, setSlugStatus] = React.useState<SlugStatus>({ state: "empty" })
  const [crews, setCrews] = React.useState<WizardCrew[] | null>(null)
  const [agents, setAgents] = React.useState<WizardAgent[]>([])
  const [openStep, setOpenStep] = React.useState<string | null>(null)
  const [next, setNext] = React.useState<NextStarts>({ state: "idle", occurrences: [] })
  const [test, setTest] = React.useState<TestState>({ state: "idle" })
  const [testedKey, setTestedKey] = React.useState<string | null>(null)
  const [now, setNow] = React.useState(() => Date.now())
  const [busy, setBusy] = React.useState<"save" | "draft" | null>(null)
  const [refusal, setRefusal] = React.useState<string | null>(null)
  const [copyBusy, setCopyBusy] = React.useState<string | null>(null)
  const [fromNote, setFromNote] = React.useState<React.ReactNode>(null)
  const [appearance, setAppearance] = React.useState<{ icon: string; color: string } | null>(null)
  const [picking, setPicking] = React.useState(false)
  const [lastCrew, setLastCrew] = useUserPreference<string | null>("routines.lastCrew", null)

  const set = React.useCallback((patch: Partial<WizardState>) => setState((s) => ({ ...s, ...patch })), [])

  // A fresh wizard every time it opens.
  React.useEffect(() => {
    if (!open) return
    const first = initialState()
    setState(first)
    setStep(0)
    setStart("build")
    setGoal("")
    setSlugEdited(false)
    setSlugStatus({ state: "empty" })
    setOpenStep(first.steps[0].id)
    setTest({ state: "idle" })
    setTestedKey(null)
    setRefusal(null)
    setFromNote(null)
    setAppearance(null)
    setCrews(null)
    const controller = new AbortController()
    const ws = encodeURIComponent(workspaceId)
    apiFetch(`/api/v1/crews?workspace_id=${ws}`, { signal: controller.signal })
      .then((r) => (r.ok ? r.json() : []))
      // A bare array or, from the paginated handler, { data }.
      .then((data: WizardCrew[] | { data?: WizardCrew[] }) => {
        if (!controller.signal.aborted) setCrews(Array.isArray(data) ? data : Array.isArray(data?.data) ? data.data : [])
      })
      .catch(() => { if (!controller.signal.aborted) setCrews([]) })
    apiFetch(`/api/v1/agents?workspace_id=${ws}`, { signal: controller.signal })
      .then((r) => (r.ok ? r.json() : []))
      .then((data: WizardAgent[] | { data?: WizardAgent[] }) => {
        if (controller.signal.aborted) return
        setAgents(Array.isArray(data) ? data : Array.isArray(data?.data) ? data.data : [])
      })
      .catch(() => {})
    return () => controller.abort()
  }, [open, workspaceId])

  // The crew last used — only when it is still here and can run a step.
  // Never "the first crew": a routine silently filed under the wrong crew
  // runs with that crew's agents and credentials.
  React.useEffect(() => {
    if (!open || !crews || !lastCrew) return
    if (!crews.some((c) => c.id === lastCrew) || !agents.some((a) => a.crew_id === lastCrew)) return
    setState((s) => (s.crewId ? s : { ...s, crewId: lastCrew }))
  }, [open, crews, agents, lastCrew])

  // ID availability. Save is an upsert keyed on the slug and reports no
  // conflict, so both a routine and a draft holding the slug count as taken.
  React.useEffect(() => {
    if (!open) return
    const slug = state.slug
    if (!slug) return setSlugStatus({ state: "empty" })
    if (!SLUG_RE.test(slug)) return setSlugStatus({ state: "invalid" })
    setSlugStatus({ state: "checking" })
    const controller = new AbortController()
    const timer = setTimeout(async () => {
      try {
        const path = `${base}/pipelines/${encodeURIComponent(slug)}`
        const routine = await apiFetch(path, { signal: controller.signal })
        if (controller.signal.aborted) return
        if (routine.ok) {
          const body = (await routine.json().catch(() => ({}))) as { name?: string }
          if (!controller.signal.aborted) setSlugStatus({ state: "taken", by: body.name || slug })
          return
        }
        if (routine.status !== 404) return setSlugStatus({ state: "error" })
        const draft = await apiFetch(`${path}/draft`, { signal: controller.signal })
        if (controller.signal.aborted) return
        const body = draft.ok ? ((await draft.json().catch(() => ({}))) as { id?: string }) : {}
        if (controller.signal.aborted) return
        if (draft.ok && body.id) setSlugStatus({ state: "taken", by: "a draft" })
        else if (draft.ok || draft.status === 404) setSlugStatus({ state: "free" })
        else setSlugStatus({ state: "error" })
      } catch {
        if (!controller.signal.aborted) setSlugStatus({ state: "error" })
      }
    }, 300)
    return () => {
      clearTimeout(timer)
      controller.abort()
    }
  }, [open, state.slug, base])

  const cron = state.trigger.kind === "schedule" ? cronFor(state.trigger) : ""
  const timezone = state.trigger.timezone

  // Next three starts, from the server's own cron parser.
  React.useEffect(() => {
    if (!open || state.trigger.kind !== "schedule") return
    if (!cron || !timezone) return setNext({ state: "error", occurrences: [], error: !cron ? "Enter a cron expression." : "Pick a time zone." })
    setNext((n) => ({ ...n, state: "loading" }))
    const controller = new AbortController()
    const timer = setTimeout(async () => {
      try {
        const params = new URLSearchParams({ cron_expr: cron, timezone, count: "3" })
        const res = await apiFetch(`${base}/pipeline-schedules/preview?${params}`, { signal: controller.signal })
        const body = (await res.json().catch(() => ({}))) as { occurrences?: string[]; error?: string }
        if (controller.signal.aborted) return
        if (!res.ok) setNext({ state: "error", occurrences: [], error: body.error || "This schedule does not parse." })
        else setNext({ state: "ok", occurrences: Array.isArray(body.occurrences) ? body.occurrences : [] })
      } catch {
        if (!controller.signal.aborted) setNext({ state: "error", occurrences: [], error: "Could not preview the schedule." })
      }
    }, 300)
    return () => {
      clearTimeout(timer)
      controller.abort()
    }
  }, [open, state.trigger.kind, cron, timezone, base])

  const definition = React.useMemo(() => buildDefinition(state), [state])
  const review = requiresReview(definition)
  const crew = crews?.find((c) => c.id === state.crewId) ?? null
  const lead = leadOf(agents, state.crewId)
  const crewHasAgents = !!state.crewId && agents.some((a) => a.crew_id === state.crewId)
  // What a test proves: this definition, for this crew, with these inputs.
  const testKey = JSON.stringify([definition, state.crewId, sampleInputs(state.inputs)])

  // A change after the test makes the token useless: the server binds it to
  // the definition's hash. Say so instead of letting Save fail.
  React.useEffect(() => {
    if (testedKey != null && testedKey !== testKey && (test.state === "passed" || test.state === "failed")) {
      setTest({ state: "expired" })
      setTestedKey(null)
    }
  }, [testKey, testedKey, test.state])

  // The countdown; the token is worthless after five minutes.
  React.useEffect(() => {
    if (test.state !== "passed") return
    const tick = setInterval(() => {
      const t = Date.now()
      setNow(t)
      if (t >= test.expiresAt) setTest({ state: "expired" })
    }, 1000)
    return () => clearInterval(tick)
  }, [test])

  const once = state.trigger.kind === "once" ? onceProblem(state.trigger.fireAt) : null
  const basicsOk = !!state.name.trim() && slugStatus.state === "free" && crewHasAgents
  const whenOk =
    state.trigger.kind === "schedule"
      ? next.state === "ok"
      : state.trigger.kind === "once"
        ? once == null
        : true
  const stepsOk = state.steps.length > 0 && state.steps.every((s) => stepProblems(s).length === 0)
  const passed = test.state === "passed"
  // A one-time start needs an ACTIVE routine; a draft or a routine waiting
  // for review cannot carry one (internal/pipeline/store.go).
  const onceBlocked = state.trigger.kind === "once" && review
  const canSave = basicsOk && whenOk && stepsOk && passed && !onceBlocked && busy == null
  const canDraft = basicsOk && state.trigger.kind !== "once" && busy == null

  const stepReady = [basicsOk, whenOk, stepsOk, true, canSave]
  const canContinue = step < 4 && stepReady[step]

  const dirty =
    !!state.name.trim() ||
    !!state.description.trim() ||
    !!goal.trim() ||
    !!state.crewId ||
    state.inputs.length > 0 ||
    state.steps.some((s) => s.kind !== "agent" || !!s.prompt?.trim() || !!s.agentSlug)

  const onName = (name: string) => set({ name, ...(slugEdited ? {} : { slug: slugFromName(name) }) })
  const onSlug = (slug: string) => {
    setSlugEdited(true)
    set({ slug: slug.trim() })
  }

  const runTest = async () => {
    setTest({ state: "running" })
    setRefusal(null)
    const key = testKey
    try {
      const res = await apiFetch(`${base}/pipelines/test_run`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ definition, ...(state.crewId ? { author_crew_id: state.crewId } : {}), sample_inputs: sampleInputs(state.inputs) }),
      })
      const data = (await res.json().catch(() => ({}))) as {
        status?: string
        error?: string
        error_message?: string
        failed_at_step?: string
        save_token?: string
        would_execute?: { step_id: string; would_call_agent?: string; estimated_cost_usd?: number }[]
      }
      setTestedKey(key)
      if (res.ok && (data.status === "DRY_RUN_OK" || data.status === "COMPLETED") && data.save_token) {
        const t = Date.now()
        setNow(t)
        setTest({ state: "passed", token: data.save_token, expiresAt: t + TOKEN_TTL_MS, wouldExecute: data.would_execute ?? [] })
        return
      }
      if (res.ok && (data.status === "DRY_RUN_OK" || data.status === "COMPLETED")) {
        setTest({ state: "failed", problems: [{ path: "", message: "The test passed, but the server issued no save proof. Ask an admin to check the save-token secret.", stepIndex: null }] })
        return
      }
      const message = data.error || data.error_message || `The test did not pass (${data.status ?? res.status}).`
      const problems = parseValidationErrors(message)
      if (data.failed_at_step) {
        const i = state.steps.findIndex((s) => s.id === data.failed_at_step)
        if (i >= 0) problems.forEach((p) => { if (p.stepIndex == null) p.stepIndex = i })
      }
      setTest({ state: "failed", problems })
    } catch (e) {
      setTestedKey(key)
      setTest({ state: "failed", problems: [{ path: "", message: e instanceof Error ? e.message : String(e), stepIndex: null }] })
    }
  }

  const applyAppearance = async (slug: string, name: string) => {
    if (!appearance) return
    const patch = async () => {
      const res = await apiFetch(`${base}/pipelines/${encodeURIComponent(slug)}/appearance`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(appearance),
      })
      if (!res.ok) throw new Error("appearance")
    }
    try {
      await patch()
    } catch {
      toast.error(`${name} is saved · its icon was not set`, {
        action: { label: "Retry", onClick: () => void patch().then(() => onCreated?.()).catch(() => toast.error("Could not set the icon")) },
      })
    }
  }

  const save = async () => {
    if (!canSave || test.state !== "passed") return
    setBusy("save")
    setRefusal(null)
    const name = state.name.trim()
    try {
      const res = await apiFetch(`${base}/pipelines/save`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(buildSaveBody(state, definition, test.token, lead?.id)),
      })
      const data = (await res.json().catch(() => ({}))) as { error?: string; status?: string; slug?: string; trigger?: { first_fire_at?: string } }
      if (!res.ok) {
        const message = data.error || `Could not save the routine (${res.status}).`
        if (res.status === 422 && /test_run|save_token|test run/i.test(message)) {
          setTest({ state: "expired" })
          return
        }
        if (res.status === 422) {
          setTest({ state: "failed", problems: parseValidationErrors(message) })
          return
        }
        setRefusal(message)
        return
      }
      const slug = data.slug || state.slug
      await applyAppearance(slug, name)
      if (state.crewId) setLastCrew(state.crewId)
      const first = data.trigger?.first_fire_at
      toast.success(
        data.status === "proposed"
          ? `${name} is waiting for a Manager review`
          : first
            ? `${name} is live · first start ${formatStart(first, state.trigger.timezone || undefined)}`
            : `${name} is live`,
        { action: { label: "Open", onClick: () => onOpenRoutine(slug) } },
      )
      onCreated?.()
      onOpenChange(false)
    } catch (e) {
      setRefusal(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(null)
    }
  }

  const saveDraft = async () => {
    if (!canDraft) return
    setBusy("draft")
    setRefusal(null)
    try {
      await saveRoutineDraft(
        workspaceId,
        { id: "", slug: state.slug, revision: 0, base_pipeline_id: "", base_revision: 0, document: {} },
        buildDraftDocument(state, definition, appearance ?? undefined),
      )
      if (state.crewId) setLastCrew(state.crewId)
      toast.success(`${state.name.trim()} is saved as a draft · not live`)
      onCreated?.()
      onOpenChange(false)
      onOpenRoutine(state.slug)
    } catch (e) {
      setRefusal(
        e instanceof RoutineDraftError && e.status === 409
          ? `A routine or draft called “${state.slug}” already exists. Pick another ID.`
          : e instanceof Error
            ? e.message
            : String(e),
      )
    } finally {
      setBusy(null)
    }
  }

  const describe = () => {
    if (!goal.trim() || !lead) return
    router.push(`/chat/${encodeURIComponent(lead.slug)}?prompt=${encodeURIComponent(describeMessage(state.name, goal))}`)
    onOpenChange(false)
  }

  const copy = async (source: Pipeline) => {
    if (copyBusy) return
    setCopyBusy(source.slug)
    setRefusal(null)
    try {
      const res = await apiFetch(`${base}/pipelines/${encodeURIComponent(source.slug)}`)
      if (!res.ok) throw new Error(`Could not read ${source.name || source.slug} (${res.status}).`)
      const detail = (await res.json()) as { name?: string; head_version?: number; author_crew_id?: string; definition?: Record<string, unknown> }
      const copied = wizardFromDefinition(detail.definition ?? {})
      const name = `${detail.name || source.name || source.slug} (copy)`
      setState((s) => ({
        ...s,
        ...copied,
        name,
        slug: slugEdited ? s.slug : slugFromName(name),
        crewId: detail.author_crew_id && crews?.some((c) => c.id === detail.author_crew_id) ? detail.author_crew_id : s.crewId,
      }))
      setOpenStep(null)
      setFromNote(
        <>
          Copied from <b className="font-medium">{detail.name || source.slug}</b>
          {detail.head_version ? ` (published v${detail.head_version})` : ""}. Triggers and history are not copied. Check each step, then continue.
        </>,
      )
      setStart("build")
      setStep(2)
    } catch (e) {
      setRefusal(e instanceof Error ? e.message : String(e))
    } finally {
      setCopyBusy(null)
    }
  }

  const goTo = (i: number) => {
    setRefusal(null)
    setStep(i)
  }

  const primaryLabel =
    start === "describe" && step === 0
      ? lead
        ? `Ask ${lead.name} to draft it`
        : "Ask the crew lead to draft it"
      : step < 4
        ? "Continue"
        : review
          ? "Save and send for review"
          : "Save and turn on"
  const primaryDisabled = start === "describe" && step === 0 ? !goal.trim() || !lead : step < 4 ? !canContinue : !canSave
  const onPrimary = () => {
    if (primaryDisabled) return
    if (start === "describe" && step === 0) return describe()
    if (step < 4) return goTo(step + 1)
    void save()
  }

  const glyph = appearance ? (
    <CrewIcon icon={appearance.icon} color={appearance.color} size="sm" />
  ) : (
    <CrewIcon icon={resolveRoutineIcon({ slug: state.slug || "new-routine" })} color={resolveRoutineColor({ slug: state.slug || "new-routine" })} size="sm" />
  )

  const afterSave = (
    <div className="flex flex-col gap-1.5 rounded-lg border border-hairline bg-foreground/[0.02] p-3 text-xs">
      <span className="font-medium text-foreground">After you save</span>
      <span className="flex items-center gap-2 text-muted-foreground">
        <CalendarClock className="h-3.5 w-3.5 shrink-0" />
        {whenText(state.trigger)}
        {state.trigger.kind === "schedule" && next.state === "ok" && next.occurrences[0] ? ` — first start ${formatStart(next.occurrences[0], timezone || undefined)}` : ""}
        {review && state.trigger.kind === "schedule" ? " · after the review" : ""}
      </span>
      <span className="flex items-center gap-2 text-muted-foreground">
        <Clock className="h-3.5 w-3.5 shrink-0" />
        Every run gets its own page in Activity, with each step and its cost.
      </span>
      <span className="flex items-center gap-2 text-muted-foreground">
        <Sparkles className="h-3.5 w-3.5 shrink-0" />
        Add a webhook or an event on the routine page.
      </span>
      {onceBlocked && (
        <CreateSurfaceNotice tone="warn">
          A one-time start needs a live routine, and this one waits for a Manager review first. Save it to start manually, and add the one-time start after it is approved.
        </CreateSurfaceNotice>
      )}
      {state.trigger.kind === "once" && !onceBlocked && (
        <span className="text-muted-foreground-soft">A one-time start needs a live routine, so it cannot be saved as a draft.</span>
      )}
      {state.trigger.kind === "schedule" && (
        <span className="text-muted-foreground-soft">A draft has no start yet: publish it, then add the schedule on the routine page.</span>
      )}
    </div>
  )

  const summary = (
    <Summary
      state={state}
      crew={crew}
      review={review}
      glyph={glyph}
      checks={[
        ["Name and ID", !!state.name.trim() && slugStatus.state === "free"],
        ["Crew with agents", crewHasAgents],
        ["When it runs", whenOk],
        ["Steps", stepsOk],
        ["Test run", passed],
      ]}
    />
  )

  return (
    <>
      <CreateSurface
        open={open}
        onOpenChange={onOpenChange}
        size="xl"
        dirty={dirty}
        discardLabel="this routine"
        discardDescription="Nothing is saved until the last step. Closing throws this routine away."
        onSubmit={onPrimary}
        ariaLabel="New routine"
        phoneHeight="full"
        className="sm:max-h-[min(90vh,820px)]"
      >
        <CreateSurfaceHeader
          concept="routines"
          context="Routines"
          title="New routine"
          description="Work your crew repeats: when it starts, which steps it takes, and where it asks you."
          onClose={() => onOpenChange(false)}
        />
        <WizardSteps steps={STEPS} current={step} onStepChange={goTo} />
        <CreateSurfaceBody>
          <div className="grid gap-5 xl:grid-cols-[minmax(0,1fr)_15.5rem] xl:items-start">
            <div className="hidden max-sm:block">
              <CreateSurfaceDisclosure label="Summary" summary={`${state.name.trim() || "Untitled"} · ${whenText(state.trigger)}`}>
                {summary}
              </CreateSurfaceDisclosure>
            </div>
            <div className="min-w-0">
              {step === 0 && (
                <BasicsPane
                  state={state}
                  set={set}
                  start={start}
                  onStart={setStart}
                  slugStatus={slugStatus}
                  slugEdited={slugEdited}
                  onName={onName}
                  onSlug={onSlug}
                  crews={crews}
                  agents={agents}
                  goal={goal}
                  onGoal={setGoal}
                  routines={routines}
                  copyBusy={copyBusy}
                  onCopy={(r) => void copy(r)}
                  onImport={onImport ? () => { onOpenChange(false); onImport() } : undefined}
                  appearance={glyph}
                  onAppearance={() => setPicking(true)}
                />
              )}
              {step === 1 && (
                <WhenPane
                  state={state}
                  set={set}
                  cron={cron}
                  next={next}
                  onPhrase={async (phrase, tz) => {
                    try {
                      const params = new URLSearchParams({ phrase, count: "1" })
                      if (tz) params.set("timezone", tz)
                      const res = await apiFetch(`${base}/pipeline-schedules/preview?${params}`)
                      const body = (await res.json().catch(() => ({}))) as { cron_expr?: string; error?: string }
                      if (!res.ok || !body.cron_expr) return { error: body.error || "That is not a schedule Crewship understands." }
                      return { cron: body.cron_expr }
                    } catch {
                      return { error: "Could not reach the server to read that." }
                    }
                  }}
                />
              )}
              {step === 2 && (
                <StepsPane
                  state={state}
                  set={set}
                  agents={agents}
                  routines={routines}
                  openStep={openStep}
                  onOpenStep={setOpenStep}
                  whenLabel={whenText(state.trigger)}
                  fromNote={fromNote}
                  onDismissFrom={() => setFromNote(null)}
                />
              )}
              {step === 3 && <LimitsPane state={state} set={set} review={review} agents={agents} />}
              {step === 4 && (
                <TestPane
                  state={state}
                  test={test}
                  now={now}
                  onTest={() => void runTest()}
                  onGoToStep={(i) => {
                    setOpenStep(state.steps[i]?.id ?? null)
                    goTo(2)
                  }}
                  definition={definition}
                  crewName={crew?.name ?? "the crew"}
                  next={next}
                  afterSave={afterSave}
                />
              )}
            </div>
            <aside aria-label="Summary" className="max-sm:hidden xl:sticky xl:top-0">
              {summary}
            </aside>
          </div>
        </CreateSurfaceBody>
        <CreateSurfaceRefusal message={refusal} onDismiss={() => setRefusal(null)} />
        <CreateSurfaceFooter
          hint={step === 4 ? null : <><kbd className="font-mono">⌘↵</kbd> continue · <kbd className="font-mono">Esc</kbd> cancel</>}
          lead={
            step === 4 ? (
              <CreateSurfaceSecondaryAction onClick={() => void saveDraft()} disabled={!canDraft} title={state.trigger.kind === "once" ? "A one-time start needs a live routine" : undefined}>
                Save as draft
              </CreateSurfaceSecondaryAction>
            ) : undefined
          }
          aside={
            step > 0 ? (
              <CreateSurfaceSecondaryAction onClick={() => goTo(step - 1)} disabled={busy != null}>
                Back
              </CreateSurfaceSecondaryAction>
            ) : undefined
          }
          onCancel={() => onOpenChange(false)}
          primaryLabel={primaryLabel}
          onPrimary={onPrimary}
          primaryDisabled={primaryDisabled}
          busy={busy === "save"}
        />
      </CreateSurface>
      <IconPickerDialog
        open={picking}
        onOpenChange={setPicking}
        context={state.name.trim() || "New routine"}
        concept="routines"
        icon={appearance?.icon ?? resolveRoutineIcon({ slug: state.slug || "new-routine" })}
        color={appearance?.color ?? resolveRoutineColor({ slug: state.slug || "new-routine" })}
        description="How this routine shows in lists and the calendar. Saved with the routine."
        onSave={(next) => {
          setAppearance(next)
          setPicking(false)
        }}
      />
    </>
  )
}

function Summary({
  state,
  crew,
  review,
  glyph,
  checks,
}: {
  state: WizardState
  crew: WizardCrew | null
  review: boolean
  glyph: React.ReactNode
  checks: [string, boolean][]
}) {
  const agentSteps = state.steps.filter((s) => s.kind === "agent").length
  const asks = state.steps.filter((s) => s.kind === "ask").length
  const calls = state.steps.filter((s) => s.kind === "http").length
  const cost = Number(state.maxCost)
  const rows: [React.ComponentType<{ className?: string }>, string, React.ReactNode, React.ReactNode?][] = [
    [Users, "Crew", crew ? <span className="inline-flex items-center gap-1.5"><CrewIcon icon={crew.icon || "folder"} color={crew.color} size="sm" />{crew.name}</span> : "Not chosen"],
    [CalendarClock, "When", whenText(state.trigger), state.trigger.kind === "schedule" ? state.trigger.timezone : undefined],
    [List, "Steps", `${state.steps.length} ${state.steps.length === 1 ? "step" : "steps"}`, `${agentSteps} by agents · ${asks} ${asks === 1 ? "asks" : "ask"} you${calls ? ` · ${calls} web ${calls === 1 ? "call" : "calls"}` : ""}`],
    [Gauge, "Limit", state.maxCost.trim() && cost > 0 ? `$${cost.toFixed(2)} per run` : "No cost limit", state.concurrency === "parallel" ? `${state.sideBySide} at a time` : state.concurrency === "skip" ? "one at a time, skips a new start" : "one at a time"],
    [Shield, "Goes live", review ? <span className="text-warn">After a Manager review</span> : "Right after the test"],
  ]
  return (
    <div className="flex flex-col gap-3 rounded-lg border border-hairline bg-muted p-3.5 text-xs">
      <div className="flex items-center gap-2.5">
        {glyph}
        <span className="min-w-0">
          <span className="block truncate font-medium text-foreground">{state.name.trim() || "Untitled routine"}</span>
          <span className="block truncate font-mono text-label text-muted-foreground">{state.slug || "id"}</span>
        </span>
      </div>
      <dl className="flex flex-col gap-2">
        {rows.map(([Icon, label, value, sub]) => (
          <div key={label} className="flex flex-col gap-0.5">
            <dt className="flex items-center gap-1.5 text-label text-muted-foreground">
              <Icon className="h-3 w-3" />
              {label}
            </dt>
            <dd className="text-foreground">
              {value}
              {sub && <span className="block text-label text-muted-foreground">{sub}</span>}
            </dd>
          </div>
        ))}
      </dl>
      <div className="flex flex-col gap-1 border-t border-hairline pt-2.5">
        <span className="text-label text-muted-foreground">Ready to save</span>
        {checks.map(([label, ok]) => (
          <span key={label} className={cn("flex items-center gap-1.5", ok ? "text-foreground" : "text-muted-foreground")}>
            {ok ? <CheckCircle2 className="h-3.5 w-3.5 text-success" /> : <Clock className="h-3.5 w-3.5" />}
            {label}
          </span>
        ))}
      </div>
    </div>
  )
}
