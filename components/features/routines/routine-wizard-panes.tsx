"use client"

import * as React from "react"
import {
  AlertTriangle,
  Bell,
  Bot,
  CalendarClock,
  CheckCircle2,
  ChevronRight,
  Clock,
  Copy,
  Globe,
  Hand,
  ListChecks,
  Play,
  Plus,
  RefreshCw,
  Repeat,
  Sparkles,
  SquareTerminal,
  Trash2,
  Upload,
  Webhook,
  Workflow,
  type LucideIcon,
} from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Switch } from "@/components/ui/switch"
import { Spinner } from "@/components/ui/spinner"
import { CrewIcon } from "@/components/ui/crew-icon"
import {
  CREATE_SURFACE_INPUT,
  CreateSurfaceChoice,
  CreateSurfaceDisclosure,
  CreateSurfaceField,
  CreateSurfaceGrid,
  CreateSurfaceNotice,
  CreateSurfaceSection,
  CreateSurfaceTile,
  CreateSurfaceToggleRow,
  useCreateSurfaceClose,
} from "@/components/layout/create-surface"
import type { Pipeline } from "@/hooks/use-pipelines"
import type { RoutineInputSpec } from "@/lib/routine-inputs"
import {
  STEP_KINDS,
  describeCliCommands,
  insertableRefs,
  newStep,
  onceProblem,
  stepProblems,
  type StepKind,
  type ValidationProblem,
  type WizardAgent,
  type WizardState,
  type WizardStep,
  type WizardTrigger,
} from "@/lib/routine-wizard"
import { RoutineInputFormBuilder } from "./routine-input-form-builder"

export interface WizardCrew {
  id: string
  name: string
  icon?: string | null
  color?: string | null
}

export type SlugStatus =
  | { state: "empty" }
  | { state: "invalid" }
  | { state: "checking" }
  | { state: "free" }
  | { state: "taken"; by: string }
  | { state: "error" }

const INPUT = cn("w-full", CREATE_SURFACE_INPUT)

/* ------------------------------------------------------------------ shared */

/** A set of tiles where exactly one is chosen — the kit tile, as a radio. */
function TileChoice<T extends string>({
  label,
  value,
  options,
  onChange,
  cols = 2,
}: {
  label: string
  value: T
  options: { value: T; icon: LucideIcon; title: string; hint: string }[]
  onChange: (v: T) => void
  cols?: 2 | 4
}) {
  return (
    <div role="radiogroup" aria-label={label} className={cn("grid gap-2 sm:grid-cols-2", cols === 4 && "xl:grid-cols-4")}>
      {options.map((o) => (
        <CreateSurfaceTile
          key={o.value}
          role="radio"
          aria-checked={value === o.value}
          aria-pressed={undefined}
          selected={value === o.value}
          icon={o.icon}
          title={o.title}
          description={o.hint}
          onClick={() => onChange(o.value)}
        />
      ))}
    </div>
  )
}

/* ------------------------------------------------------------------ Basics */

export type StartMode = "build" | "describe" | "copy"

export function BasicsPane({
  state,
  set,
  start,
  onStart,
  slugStatus,
  slugEdited,
  onName,
  onSlug,
  crews,
  agents,
  goal,
  onGoal,
  routines,
  copyBusy,
  onCopy,
  onImport,
  appearance,
  onAppearance,
}: {
  state: WizardState
  set: (patch: Partial<WizardState>) => void
  start: StartMode
  onStart: (m: StartMode) => void
  slugStatus: SlugStatus
  slugEdited: boolean
  onName: (v: string) => void
  onSlug: (v: string) => void
  crews: WizardCrew[] | null
  agents: WizardAgent[]
  goal: string
  onGoal: (v: string) => void
  routines: Pipeline[]
  copyBusy: string | null
  onCopy: (r: Pipeline) => void
  onImport?: () => void
  appearance: React.ReactNode
  onAppearance: () => void
}) {
  const nameId = React.useId()
  const slugId = React.useId()
  const goalId = React.useId()
  const descId = React.useId()
  const [cli, setCli] = React.useState(false)
  // Leaving for Import is a way out of the wizard: it asks before dropping typed input.
  const guardClose = useCreateSurfaceClose()
  const agentCount = (crewId: string) => agents.filter((a) => a.crew_id === crewId).length
  const copyable = routines.filter((r) => (r.head_version ?? 0) > 0)
  const crewSlug = crews?.find((c) => c.id === state.crewId)?.name.toLowerCase().replace(/[^a-z0-9]+/g, "-") ?? ""

  const slugHint =
    slugStatus.state === "invalid" ? null : "Used by the CLI, links and the chat command. Fixed after the first save."
  const slugBadge = (() => {
    switch (slugStatus.state) {
      case "free":
        return <span className="inline-flex items-center gap-1 text-label text-success"><CheckCircle2 className="h-3 w-3" />Free</span>
      case "taken":
        return <span className="text-label text-destructive">Taken by {slugStatus.by}</span>
      case "checking":
        return <span className="text-label text-muted-foreground">Checking…</span>
      case "error":
        return <span className="text-label text-muted-foreground">Could not check</span>
      default:
        return null
    }
  })()

  return (
    <div className="flex flex-col gap-4">
      <TileChoice<StartMode>
        label="How to start"
        value={start}
        onChange={onStart}
        options={[
          { value: "build", icon: ListChecks, title: "Build it here", hint: "Fill in each step yourself" },
          { value: "describe", icon: Sparkles, title: "Describe it", hint: "The crew lead drafts it in chat; you review the draft" },
          { value: "copy", icon: Copy, title: "Copy a routine", hint: "Steps, inputs and limits of an existing one" },
        ]}
      />

      {start === "describe" && (
        <CreateSurfaceField
          label="What should happen, and when?"
          htmlFor={goalId}
          hint="The crew lead drafts the routine in chat and saves it as a draft. Nothing runs until it is published."
        >
          <Textarea
            id={goalId}
            value={goal}
            onChange={(e) => onGoal(e.target.value)}
            rows={3}
            placeholder="Every Thursday, compare competitor pricing pages with last week and draft a report I approve."
            className="text-xs coarse:text-base"
          />
        </CreateSurfaceField>
      )}

      {start === "copy" && (
        <CreateSurfaceSection title="Routine to copy" hint="Triggers, history and access are not copied">
          {copyable.length === 0 && <p className="text-xs text-muted-foreground">No published routine to copy yet.</p>}
          <div className="flex max-h-56 flex-col gap-1.5 overflow-y-auto">
            {copyable.map((r) => (
              <CreateSurfaceTile
                key={r.slug}
                data-testid={`routine-copy-${r.slug}`}
                icon={Workflow}
                title={r.name || r.slug}
                meta={copyBusy === r.slug ? <Spinner className="h-3 w-3" /> : undefined}
                description={r.description || r.slug}
                disabled={!!copyBusy}
                onClick={() => onCopy(r)}
              />
            ))}
          </div>
        </CreateSurfaceSection>
      )}

      <CreateSurfaceGrid>
        <CreateSurfaceField label="Name" htmlFor={nameId}>
          <Input
            id={nameId}
            value={state.name}
            onChange={(e) => onName(e.target.value)}
            placeholder="Competitor price watch"
            autoFocus
            className={INPUT}
          />
        </CreateSurfaceField>
        <CreateSurfaceField
          label="ID"
          htmlFor={slugId}
          hint={slugHint}
          error={slugStatus.state === "invalid" ? "Lowercase letters, digits, - and _ only; start with a letter or digit." : undefined}
        >
          <div className="relative">
            <Input
              id={slugId}
              value={state.slug}
              onChange={(e) => onSlug(e.target.value)}
              aria-describedby={slugStatus.state === "invalid" ? `${slugId}-error` : undefined}
              data-edited={slugEdited || undefined}
              className={cn(INPUT, "pr-28 font-mono")}
            />
            <span className="pointer-events-none absolute inset-y-0 right-2 flex items-center">{slugBadge}</span>
          </div>
        </CreateSurfaceField>
      </CreateSurfaceGrid>

      <CreateSurfaceField label="Crew" hint="Every step runs as an agent of this crew, with its connections, files and credentials.">
        {crews == null ? (
          <p className="text-xs text-muted-foreground">Loading crews…</p>
        ) : crews.length === 0 ? (
          <p className="text-xs text-muted-foreground">No crew yet. Create a crew with an agent first.</p>
        ) : (
          <div role="radiogroup" aria-label="Crew" className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
            {crews.map((c) => {
              const n = agentCount(c.id)
              return (
                <CreateSurfaceTile
                  key={c.id}
                  role="radio"
                  aria-checked={state.crewId === c.id}
                  aria-pressed={undefined}
                  selected={state.crewId === c.id}
                  disabled={n === 0}
                  title={c.name}
                  description={n === 0 ? "No agent to run a step" : `${n} ${n === 1 ? "agent" : "agents"}`}
                  leading={<CrewIcon icon={c.icon || "folder"} color={c.color} size="sm" />}
                  onClick={() => set({ crewId: c.id })}
                />
              )
            })}
          </div>
        )}
      </CreateSurfaceField>

      {start !== "describe" && (
        <CreateSurfaceField label="What it is for (optional)" htmlFor={descId}>
          <Textarea
            id={descId}
            value={state.description}
            onChange={(e) => set({ description: e.target.value })}
            rows={2}
            placeholder="Weekly look at competitor pricing so Product hears about changes the same day."
            className="text-xs coarse:text-base"
          />
        </CreateSurfaceField>
      )}

      {start !== "describe" && (
        <div className="flex items-center gap-3 rounded-lg border border-hairline bg-foreground/[0.02] px-3 py-2">
          {appearance}
          <span className="min-w-0 flex-1 text-xs text-muted-foreground">Icon and colour — how it shows in lists and the calendar.</span>
          <Button type="button" variant="ghost" size="sm" onClick={onAppearance} className="h-8 text-xs coarse:h-[44px]">
            Icon and colour
          </Button>
        </div>
      )}

      <div className="flex flex-col gap-2 text-xs text-muted-foreground">
        <p className="flex flex-wrap items-center gap-1.5">
          <Upload className="h-3.5 w-3.5" />
          Have a file?
          <button type="button" className="text-primary-hover hover:underline" onClick={() => onImport && guardClose(onImport)}>
            Import a routine
          </button>
          <span aria-hidden>·</span>
          <SquareTerminal className="h-3.5 w-3.5" />
          <button type="button" aria-expanded={cli} className="text-primary-hover hover:underline" onClick={() => setCli((v) => !v)}>
            Build it on the command line
          </button>
        </p>
        {cli && (
          <div className="flex flex-col gap-2 rounded-lg border border-hairline bg-foreground/[0.02] p-3">
            {describeCliCommands(state.slug, state.name, crewSlug).map((c) => (
              <div key={c.command}>
                <p className="mb-1">{c.step}</p>
                <pre className="overflow-x-auto whitespace-pre-wrap rounded-md border border-hairline bg-background px-3 py-2 font-mono text-label text-foreground">{c.command}</pre>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

/* -------------------------------------------------------------------- When */

const PRESETS: { value: WizardTrigger["preset"]; label: string }[] = [
  { value: "hour", label: "Every hour" },
  { value: "day", label: "Every day" },
  { value: "weekdays", label: "Weekdays" },
  { value: "week", label: "Weekly" },
  { value: "month", label: "Monthly" },
  { value: "custom", label: "Custom (cron)" },
]
// cron numbers the week from Sunday = 0; the row reads Monday first.
const DOW: { value: string; label: string }[] = [
  { value: "1", label: "Mo" },
  { value: "2", label: "Tu" },
  { value: "3", label: "We" },
  { value: "4", label: "Th" },
  { value: "5", label: "Fr" },
  { value: "6", label: "Sa" },
  { value: "0", label: "Su" },
]

function timeZones(): string[] {
  try {
    const all = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.("timeZone")
    if (all?.length) return all
  } catch {
    /* older engines */
  }
  return ["UTC", Intl.DateTimeFormat().resolvedOptions().timeZone]
}

export interface NextStarts {
  state: "idle" | "loading" | "ok" | "error"
  occurrences: string[]
  error?: string
}

export function formatStart(iso: string, timeZone?: string): string {
  try {
    return new Date(iso).toLocaleString(undefined, { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", timeZone })
  } catch {
    return new Date(iso).toLocaleString()
  }
}

export function WhenPane({
  state,
  set,
  cron,
  next,
}: {
  state: WizardState
  set: (patch: Partial<WizardState>) => void
  cron: string
  next: NextStarts
}) {
  const t = state.trigger
  const setT = (patch: Partial<WizardTrigger>) => set({ trigger: { ...t, ...patch } })
  const ids = { at: React.useId(), minute: React.useId(), tz: React.useId(), cron: React.useId(), once: React.useId(), fails: React.useId(), tzList: React.useId() }
  const zones = React.useMemo(timeZones, [])
  const onceLocal = t.fireAt ? toLocalInput(t.fireAt) : ""
  const once = t.kind === "once" ? onceProblem(t.fireAt) : null

  return (
    <div className="flex flex-col gap-4">
      <TileChoice<WizardTrigger["kind"]>
        label="When it starts"
        value={t.kind}
        cols={4}
        onChange={(kind) => setT({ kind })}
        options={[
          { value: "manual", icon: Play, title: "Manually", hint: "Run now, from an issue or a chat command" },
          { value: "schedule", icon: Repeat, title: "On a schedule", hint: "Repeats at set times" },
          { value: "once", icon: CalendarClock, title: "Once", hint: "One start at a date and time" },
          { value: "app", icon: Webhook, title: "When an app calls", hint: "Webhook or an event · after saving" },
        ]}
      />

      {t.kind === "schedule" && (
        <div className="flex flex-col gap-3 rounded-lg border border-hairline bg-foreground/[0.02] p-3">
          <CreateSurfaceField label="Repeat">
            <CreateSurfaceChoice ariaLabel="Repeat" value={t.preset} options={PRESETS} onChange={(preset) => setT({ preset })} />
          </CreateSurfaceField>
          {t.preset === "week" && (
            <CreateSurfaceField label="On">
              <CreateSurfaceChoice ariaLabel="On" value={String(t.weekday)} options={DOW} onChange={(d) => setT({ weekday: Number(d) })} />
            </CreateSurfaceField>
          )}
          {t.preset === "custom" && (
            <CreateSurfaceField label="Cron expression" htmlFor={ids.cron} hint="5 fields: minute hour day month weekday. @daily and @weekly work too.">
              <Input id={ids.cron} value={t.cron} onChange={(e) => setT({ cron: e.target.value })} className={cn(INPUT, "font-mono")} />
            </CreateSurfaceField>
          )}
          <CreateSurfaceGrid cols={3}>
            {t.preset === "hour" ? (
              <CreateSurfaceField label="At minute" htmlFor={ids.minute}>
                <Input id={ids.minute} type="number" min={0} max={59} value={t.minute} onChange={(e) => setT({ minute: clamp(Number(e.target.value), 0, 59) })} className={INPUT} />
              </CreateSurfaceField>
            ) : t.preset !== "custom" ? (
              <CreateSurfaceField label="At" htmlFor={ids.at}>
                <Input id={ids.at} type="time" value={t.time} onChange={(e) => setT({ time: e.target.value || "00:00" })} className={INPUT} />
              </CreateSurfaceField>
            ) : (
              <span />
            )}
            <CreateSurfaceField label="Time zone" htmlFor={ids.tz}>
              <Input id={ids.tz} list={ids.tzList} value={t.timezone} onChange={(e) => setT({ timezone: e.target.value })} className={INPUT} />
              <datalist id={ids.tzList}>
                {zones.map((z) => (
                  <option key={z} value={z} />
                ))}
              </datalist>
            </CreateSurfaceField>
            <CreateSurfaceField label="Cron">
              <div data-testid="routine-cron" className="flex h-8 items-center rounded-md border border-hairline bg-background px-2.5 font-mono text-xs text-foreground coarse:h-[44px]">
                {cron || "—"}
              </div>
            </CreateSurfaceField>
          </CreateSurfaceGrid>
          <div className="flex flex-wrap items-center gap-1.5 text-xs" aria-live="polite">
            <span className="text-muted-foreground">Next starts</span>
            {next.state === "loading" && <Spinner className="h-3 w-3" />}
            {next.state === "error" && <span role="alert" className="text-destructive">{next.error || "This schedule does not parse."}</span>}
            {next.state === "ok" &&
              next.occurrences.map((o) => (
                <span key={o} data-testid="routine-next-start" className="inline-flex items-center gap-1 rounded-md border border-hairline bg-card px-2 py-0.5 text-foreground">
                  <CalendarClock className="h-3 w-3 text-muted-foreground" />
                  {formatStart(o, t.timezone || undefined)}
                </span>
              ))}
          </div>
          <CreateSurfaceDisclosure
            label="If a start is missed or keeps failing"
            summary={`${t.catchup === "once" ? "Run once when back" : t.catchup === "all" ? "Run every missed start" : "Skip missed starts"} · off after ${t.maxFailures} failures`}
          >
            <CreateSurfaceField label="Missed while Crewship was down" hint="Every missed start is capped at 20.">
              <CreateSurfaceChoice
                ariaLabel="Missed while Crewship was down"
                value={t.catchup}
                onChange={(catchup) => setT({ catchup })}
                options={[
                  { value: "once", label: "Run once when back" },
                  { value: "skip", label: "Skip them" },
                  { value: "all", label: "Run every missed start" },
                ]}
              />
            </CreateSurfaceField>
            <CreateSurfaceField label="Turn the schedule off after this many failed runs in a row" htmlFor={ids.fails}>
              <Input id={ids.fails} type="number" min={1} max={100} value={t.maxFailures} onChange={(e) => setT({ maxFailures: clamp(Number(e.target.value), 1, 100) })} className={cn(INPUT, "sm:w-32")} />
            </CreateSurfaceField>
          </CreateSurfaceDisclosure>
        </div>
      )}

      {t.kind === "once" && (
        <div className="flex flex-col gap-2 rounded-lg border border-hairline bg-foreground/[0.02] p-3">
          <CreateSurfaceField
            label="Date and time"
            htmlFor={ids.once}
            error={t.fireAt || onceLocal ? once ?? undefined : undefined}
            hint="Any time in the next two years, in your time zone. Starts only once the routine is live."
          >
            <Input
              id={ids.once}
              type="datetime-local"
              value={onceLocal}
              onChange={(e) => setT({ fireAt: e.target.value ? new Date(e.target.value).toISOString() : "" })}
              className={cn(INPUT, "sm:w-64")}
            />
          </CreateSurfaceField>
        </div>
      )}

      {t.kind === "manual" && (
        <div className="grid gap-2 rounded-lg border border-hairline bg-foreground/[0.02] p-3 sm:grid-cols-3">
          {[
            [Play, "Run now", "On the routine page"],
            [ListChecks, "From an issue", "Run routine on any issue"],
            [SquareTerminal, "In chat", state.slash ? `/${state.slug || "routine"}` : "Turn on the chat command below"],
          ].map(([Icon, title, hint]) => {
            const I = Icon as LucideIcon
            return (
              <span key={title as string} className="flex items-start gap-2 text-xs">
                <I className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                <span>
                  <span className="block font-medium text-foreground">{title as string}</span>
                  <span className="text-muted-foreground">{hint as string}</span>
                </span>
              </span>
            )
          })}
        </div>
      )}

      {t.kind === "app" && (
        <CreateSurfaceNotice icon={Webhook}>
          Webhooks and events attach to a saved routine. It is saved to start manually now; on the routine page, <b className="font-medium">Plan</b> gives you the webhook URL and secret, or an event to start from.
        </CreateSurfaceNotice>
      )}

      <CreateSurfaceToggleRow
        icon={SquareTerminal}
        label="Also start it from chat"
        hint={<>Type <span className="font-mono">/{state.slug || "routine"}</span> in any crew chat.</>}
        control={<Switch checked={state.slash} onCheckedChange={(slash) => set({ slash })} aria-label="Also start it from chat" />}
      />

      <CreateSurfaceSection title="Asked at each start" hint="optional">
        <RoutineInputFormBuilder inputs={state.inputs} onChange={(inputs: RoutineInputSpec[]) => set({ inputs })} />
        <p className="text-label text-muted-foreground-soft">
          {t.kind === "schedule"
            ? "Scheduled starts use each input's default, so a required input needs one."
            : "Steps read them as {{ inputs.name }}."}
        </p>
      </CreateSurfaceSection>
    </div>
  )
}

function clamp(n: number, lo: number, hi: number) {
  if (!Number.isFinite(n)) return lo
  return Math.min(hi, Math.max(lo, Math.round(n)))
}

function toLocalInput(iso: string): string {
  const d = new Date(iso)
  if (!Number.isFinite(d.getTime())) return ""
  return new Date(d.getTime() - d.getTimezoneOffset() * 60_000).toISOString().slice(0, 16)
}

/* ------------------------------------------------------------------- Steps */

export const STEP_ICON: Record<StepKind, LucideIcon> = {
  agent: Bot,
  ask: Hand,
  notify: Bell,
  wait: Clock,
  issue: ListChecks,
  http: Globe,
  sub: Workflow,
  other: SquareTerminal,
}

const kindLabel = (k: StepKind) => STEP_KINDS.find((x) => x.kind === k)?.label ?? "Kept as it was"

export function StepsPane({
  state,
  set,
  agents,
  routines,
  openStep,
  onOpenStep,
  whenLabel,
  fromNote,
  onDismissFrom,
}: {
  state: WizardState
  set: (patch: Partial<WizardState>) => void
  agents: WizardAgent[]
  routines: Pipeline[]
  openStep: string | null
  onOpenStep: (id: string | null) => void
  whenLabel: string
  fromNote: React.ReactNode
  onDismissFrom: () => void
}) {
  const [adding, setAdding] = React.useState(false)
  const crewAgents = agents.filter((a) => a.crew_id === state.crewId)
  const update = (id: string, patch: Partial<WizardStep>) =>
    set({ steps: state.steps.map((s) => (s.id === id ? { ...s, ...patch } : s)) })
  const remove = (id: string) => {
    set({ steps: state.steps.filter((s) => s.id !== id) })
    onOpenStep(null)
  }
  const add = (kind: StepKind) => {
    const step = newStep(kind, state.steps)
    set({ steps: [...state.steps, step] })
    onOpenStep(step.id)
    setAdding(false)
  }

  return (
    <div className="flex flex-col gap-3">
      {fromNote && (
        <CreateSurfaceNotice icon={Copy}>
          <span className="flex items-start gap-2">
            <span className="flex-1">{fromNote}</span>
            <button type="button" onClick={onDismissFrom} className="text-muted-foreground hover:text-foreground">
              Dismiss
            </button>
          </span>
        </CreateSurfaceNotice>
      )}
      <p className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
        <span className="flex-1">Steps run top to bottom; each waits for the one above. Open a step to edit it.</span>
        <span className="inline-flex items-center gap-1 rounded-md border border-hairline px-2 py-0.5">
          <Repeat className="h-3 w-3" />
          {whenLabel}
        </span>
      </p>
      {state.steps.length === 0 && (
        <p className="rounded-lg border border-dashed border-border px-3 py-6 text-center text-xs text-muted-foreground">
          No steps yet. Add the first one below.
        </p>
      )}
      <ol className="flex flex-col gap-1.5">
        {state.steps.map((step, i) => {
          const Icon = STEP_ICON[step.kind]
          const open = openStep === step.id
          const problems = stepProblems(step)
          const agent = step.kind === "agent" ? crewAgents.find((a) => a.slug === step.agentSlug) : null
          const title = step.name || step.id
          return (
            <li key={step.id} className={cn("rounded-lg border", open ? "border-primary/40 bg-primary/[0.04]" : "border-hairline bg-foreground/[0.02]")}>
              <button
                type="button"
                aria-expanded={open}
                aria-label={`Step ${i + 1}: ${title}`}
                onClick={() => onOpenStep(open ? null : step.id)}
                className="flex w-full items-center gap-2.5 px-3 py-2 text-left coarse:min-h-[44px]"
              >
                <span className="w-4 shrink-0 text-center font-mono text-label text-muted-foreground">{i + 1}</span>
                <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-foreground/[0.05]">
                  <Icon className="h-3.5 w-3.5 text-foreground/80" />
                </span>
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-xs font-medium text-foreground">{title}</span>
                  <span className="block truncate text-label text-muted-foreground">
                    {kindLabel(step.kind)}
                    {agent ? ` · ${agent.name}` : ""}
                  </span>
                </span>
                {step.kind === "http" && <span className="shrink-0 text-label text-warn">Review</span>}
                {problems.length > 0 && !open && <span className="shrink-0 text-label text-warn">{problems[0]}</span>}
                <ChevronRight className={cn("h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform", open && "rotate-90")} />
              </button>
              {open && (
                <div className="flex flex-col gap-3 border-t border-hairline px-3 py-3">
                  <StepEditor
                    step={step}
                    index={i}
                    state={state}
                    crewAgents={crewAgents}
                    routines={routines}
                    onChange={(patch) => update(step.id, patch)}
                  />
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="flex-1 text-label text-muted-foreground-soft">
                      {step.kind === "other" ? null : (
                        <>Later steps read its result as <span className="font-mono">{`{{ steps.${step.id}.output }}`}</span></>
                      )}
                    </span>
                    <Button type="button" variant="ghost" size="sm" onClick={() => remove(step.id)} className="h-8 gap-1.5 text-xs coarse:h-[44px]">
                      <Trash2 className="h-3 w-3" />
                      Remove
                    </Button>
                    <Button type="button" variant="outline" size="sm" onClick={() => onOpenStep(null)} className="h-8 text-xs coarse:h-[44px]">
                      Done
                    </Button>
                  </div>
                </div>
              )}
            </li>
          )
        })}
      </ol>

      <div className="flex flex-col gap-2">
        <Button type="button" variant="outline" size="sm" aria-expanded={adding} onClick={() => setAdding((v) => !v)} className="h-8 w-fit gap-1.5 text-xs coarse:h-[44px]">
          <Plus className="h-3 w-3" />
          Add a step
        </Button>
        {adding && (
          <div className="grid gap-1.5 sm:grid-cols-2">
            {STEP_KINDS.map((k) => (
              <CreateSurfaceTile key={k.kind} icon={STEP_ICON[k.kind]} title={k.label} description={k.hint} onClick={() => add(k.kind)} />
            ))}
            <p className="text-label text-muted-foreground-soft sm:col-span-2">
              Code, transform, query and loops are written in the definition file: use the CLI.
            </p>
          </div>
        )}
      </div>
    </div>
  )
}

function InsertRefs({ refs, onInsert }: { refs: string[]; onInsert: (ref: string) => void }) {
  if (refs.length === 0) return null
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <span className="text-label text-muted-foreground">Insert</span>
      {refs.map((r) => (
        <button
          key={r}
          type="button"
          aria-label={`Insert ${r}`}
          onClick={() => onInsert(r)}
          className="rounded-md border border-hairline bg-foreground/[0.03] px-1.5 py-0.5 font-mono text-label text-muted-foreground hover:text-foreground coarse:min-h-[44px]"
        >
          {r}
        </button>
      ))}
    </div>
  )
}

function StepEditor({
  step,
  index,
  state,
  crewAgents,
  routines,
  onChange,
}: {
  step: WizardStep
  index: number
  state: WizardState
  crewAgents: WizardAgent[]
  routines: Pipeline[]
  onChange: (patch: Partial<WizardStep>) => void
}) {
  const id = React.useId()
  const f = (name: string) => `${id}-${name}`
  const refs = insertableRefs(state.steps, index, state.inputs)
  const append = (key: keyof WizardStep, ref: string) => {
    const current = String(step[key] ?? "")
    onChange({ [key]: current ? `${current} ${ref}` : ref } as Partial<WizardStep>)
  }
  const nameField = (
    <CreateSurfaceField label="Step name" htmlFor={f("name")}>
      <Input id={f("name")} value={step.name} onChange={(e) => onChange({ name: e.target.value })} className={INPUT} />
    </CreateSurfaceField>
  )

  switch (step.kind) {
    case "agent": {
      const known = crewAgents.some((a) => a.slug === step.agentSlug)
      return (
        <>
          {nameField}
          <CreateSurfaceField label="Agent" error={step.agentSlug && !known ? `“${step.agentSlug}” is not in this crew. Pick one of its agents.` : undefined}>
            {crewAgents.length === 0 ? (
              <p className="text-xs text-muted-foreground">Pick a crew with agents on the first step.</p>
            ) : (
              <CreateSurfaceChoice
                ariaLabel="Agent"
                value={step.agentSlug ?? ""}
                options={crewAgents.map((a) => ({ value: a.slug, label: a.name }))}
                onChange={(agentSlug) => onChange({ agentSlug })}
              />
            )}
          </CreateSurfaceField>
          <CreateSurfaceField label="Instructions" htmlFor={f("prompt")}>
            <Textarea id={f("prompt")} rows={3} value={step.prompt ?? ""} onChange={(e) => onChange({ prompt: e.target.value })} className="text-xs coarse:text-base" />
          </CreateSurfaceField>
          <InsertRefs refs={refs} onInsert={(r) => append("prompt", r)} />
          <CreateSurfaceGrid cols={3}>
            <CreateSurfaceField label="Effort">
              <CreateSurfaceChoice
                ariaLabel="Effort"
                value={step.effort ?? "moderate"}
                onChange={(effort) => onChange({ effort })}
                options={[
                  { value: "fast", label: "Fast" },
                  { value: "moderate", label: "Moderate" },
                  { value: "smart", label: "Smart" },
                ]}
              />
            </CreateSurfaceField>
            <CreateSurfaceField label="If it fails">
              <CreateSurfaceChoice
                ariaLabel="If it fails"
                value={step.onFail ?? "retry"}
                onChange={(onFail) => onChange({ onFail })}
                options={[
                  { value: "retry", label: "Retry 3×" },
                  { value: "abort", label: "Stop" },
                  { value: "escalate", label: "Stronger model", hint: "Try again with a stronger model" },
                ]}
              />
            </CreateSurfaceField>
            <CreateSurfaceField label="Time limit (minutes)" htmlFor={f("timeout")} hint="Empty: the default.">
              <Input
                id={f("timeout")}
                type="number"
                min={1}
                value={step.timeoutMin ?? ""}
                onChange={(e) => onChange({ timeoutMin: e.target.value ? Math.max(1, Number(e.target.value)) : undefined })}
                className={INPUT}
              />
            </CreateSurfaceField>
          </CreateSurfaceGrid>
        </>
      )
    }
    case "ask":
      return (
        <>
          <CreateSurfaceField label="Question" htmlFor={f("q")}>
            <Input id={f("q")} value={step.question ?? ""} onChange={(e) => onChange({ question: e.target.value, name: e.target.value || step.name })} className={INPUT} />
          </CreateSurfaceField>
          <CreateSurfaceField label="They see" htmlFor={f("shows")} hint="A Manager or above decides, in Inbox.">
            <Textarea id={f("shows")} rows={2} value={step.shows ?? ""} onChange={(e) => onChange({ shows: e.target.value })} className="text-xs coarse:text-base" />
          </CreateSurfaceField>
          <InsertRefs refs={refs} onInsert={(r) => append("shows", r)} />
          <CreateSurfaceGrid>
            <CreateSurfaceField label="Risk">
              <CreateSurfaceChoice
                ariaLabel="Risk"
                value={step.risk ?? "normal"}
                onChange={(risk) => onChange({ risk })}
                options={[
                  { value: "normal", label: "Normal" },
                  { value: "destructive", label: "Destructive" },
                ]}
              />
            </CreateSurfaceField>
            <CreateSurfaceField label="No answer after (hours)" htmlFor={f("to")} hint="The run stops when nobody answers.">
              <Input id={f("to")} type="number" min={1} value={step.timeoutHours ?? 24} onChange={(e) => onChange({ timeoutHours: Math.max(1, Number(e.target.value) || 24) })} className={INPUT} />
            </CreateSurfaceField>
          </CreateSurfaceGrid>
        </>
      )
    case "notify":
      return (
        <>
          {nameField}
          <CreateSurfaceGrid>
            <CreateSurfaceField label="To">
              <CreateSurfaceChoice
                ariaLabel="To"
                value={step.to ?? "workspace"}
                onChange={(to) => onChange({ to })}
                options={[
                  { value: "workspace", label: "Whole workspace" },
                  { value: "trigger", label: "Who started it" },
                  { value: "role:MANAGER", label: "Managers" },
                  { value: "role:OWNER", label: "Owners" },
                ]}
              />
            </CreateSurfaceField>
            <CreateSurfaceField label="Priority">
              <CreateSurfaceChoice
                ariaLabel="Priority"
                value={step.priority ?? "medium"}
                onChange={(priority) => onChange({ priority })}
                options={[
                  { value: "low", label: "Low" },
                  { value: "medium", label: "Medium" },
                  { value: "high", label: "High" },
                  { value: "urgent", label: "Urgent" },
                ]}
              />
            </CreateSurfaceField>
          </CreateSurfaceGrid>
          <CreateSurfaceField label="Message" htmlFor={f("msg")}>
            <Textarea id={f("msg")} rows={2} value={step.message ?? ""} onChange={(e) => onChange({ message: e.target.value })} className="text-xs coarse:text-base" />
          </CreateSurfaceField>
          <InsertRefs refs={refs} onInsert={(r) => append("message", r)} />
        </>
      )
    case "wait":
      return (
        <>
          {nameField}
          <CreateSurfaceField label="Wait until">
            <CreateSurfaceChoice
              ariaLabel="Wait until"
              value={step.waitKind ?? "datetime"}
              onChange={(waitKind) => onChange({ waitKind })}
              options={[
                { value: "datetime", label: "A time" },
                { value: "event", label: "An event" },
              ]}
            />
          </CreateSurfaceField>
          {step.waitKind === "event" ? (
            <CreateSurfaceField label="Event" htmlFor={f("ev")} hint="A journal event type, for example issue.created. Waits up to an hour.">
              <Input id={f("ev")} value={step.eventType ?? ""} onChange={(e) => onChange({ eventType: e.target.value })} className={cn(INPUT, "font-mono")} />
            </CreateSurfaceField>
          ) : (
            <CreateSurfaceField label="Until" htmlFor={f("until")} hint="A time, or a template such as {{ inputs.when }}.">
              <Input id={f("until")} value={step.until ?? ""} onChange={(e) => onChange({ until: e.target.value })} placeholder="2026-11-02T09:00:00+01:00" className={cn(INPUT, "font-mono")} />
            </CreateSurfaceField>
          )}
        </>
      )
    case "issue":
      return (
        <>
          {nameField}
          <CreateSurfaceField label="Action">
            <CreateSurfaceChoice
              ariaLabel="Action"
              value={step.issueAction ?? "issue.create"}
              onChange={(issueAction) => onChange({ issueAction })}
              options={[
                { value: "issue.create", label: "Create" },
                { value: "issue.comment", label: "Comment" },
              ]}
            />
          </CreateSurfaceField>
          {step.issueAction === "issue.comment" ? (
            <>
              <CreateSurfaceField label="Issue" htmlFor={f("ident")} hint="Its identifier, for example ENG-12, or a template.">
                <Input id={f("ident")} value={step.identifier ?? ""} onChange={(e) => onChange({ identifier: e.target.value })} className={cn(INPUT, "font-mono")} />
              </CreateSurfaceField>
              <CreateSurfaceField label="Comment" htmlFor={f("body")} hint="Posted as the crew lead.">
                <Textarea id={f("body")} rows={2} value={step.body ?? ""} onChange={(e) => onChange({ body: e.target.value })} className="text-xs coarse:text-base" />
              </CreateSurfaceField>
            </>
          ) : (
            <>
              <CreateSurfaceField label="Title" htmlFor={f("title")} hint="Created in this routine's crew.">
                <Input id={f("title")} value={step.title ?? ""} onChange={(e) => onChange({ title: e.target.value })} className={INPUT} />
              </CreateSurfaceField>
              <CreateSurfaceField label="Description" htmlFor={f("body")}>
                <Textarea id={f("body")} rows={2} value={step.body ?? ""} onChange={(e) => onChange({ body: e.target.value })} className="text-xs coarse:text-base" />
              </CreateSurfaceField>
            </>
          )}
          <InsertRefs refs={refs} onInsert={(r) => append("body", r)} />
        </>
      )
    case "http":
      return (
        <>
          <CreateSurfaceNotice tone="warn" icon={AlertTriangle}>
            Calling an outside URL sends the routine to a <b className="font-medium">Manager review</b> before it can run. The host is added to the routine&apos;s allowed outside hosts.
          </CreateSurfaceNotice>
          {nameField}
          <CreateSurfaceField label="Method">
            <CreateSurfaceChoice
              ariaLabel="Method"
              value={step.method ?? "POST"}
              onChange={(method) => onChange({ method })}
              options={(["GET", "POST", "PUT", "PATCH", "DELETE"] as const).map((m) => ({ value: m, label: m }))}
            />
          </CreateSurfaceField>
          <CreateSurfaceField label="URL" htmlFor={f("url")}>
            <Input id={f("url")} value={step.url ?? ""} onChange={(e) => onChange({ url: e.target.value })} placeholder="https://api.example.com/v1/items" className={cn(INPUT, "font-mono")} />
          </CreateSurfaceField>
          <CreateSurfaceField label="Credential (optional)" htmlFor={f("cred")} hint="A credential type from the crew's Vault, sent as a bearer token.">
            <Input id={f("cred")} value={step.credentialType ?? ""} onChange={(e) => onChange({ credentialType: e.target.value })} className={cn(INPUT, "font-mono")} />
          </CreateSurfaceField>
        </>
      )
    case "sub":
      return (
        <>
          {nameField}
          <CreateSurfaceField label="Routine" htmlFor={f("slug")} hint="Runs as its own run; this one waits for it.">
            <Input id={f("slug")} list={f("slugs")} value={step.pipelineSlug ?? ""} onChange={(e) => onChange({ pipelineSlug: e.target.value })} className={cn(INPUT, "font-mono")} />
            <datalist id={f("slugs")}>
              {routines.filter((r) => r.slug !== state.slug).map((r) => (
                <option key={r.slug} value={r.slug}>
                  {r.name}
                </option>
              ))}
            </datalist>
          </CreateSurfaceField>
          <CreateSurfaceField label="Pass as input" htmlFor={f("pass")} hint="One per line: name = value.">
            <Textarea id={f("pass")} rows={2} value={step.passInputs ?? ""} onChange={(e) => onChange({ passInputs: e.target.value })} className="font-mono text-xs coarse:text-base" />
          </CreateSurfaceField>
          <InsertRefs refs={refs} onInsert={(r) => append("passInputs", r)} />
        </>
      )
    default:
      return (
        <p className="text-xs text-muted-foreground">
          This <span className="font-mono">{String(step.raw?.type ?? "step")}</span> step is kept exactly as it was. Change it in the definition file with the CLI.
        </p>
      )
  }
}

/* ------------------------------------------------------------------ Limits */

export function LimitsPane({
  state,
  set,
  review,
  agents,
}: {
  state: WizardState
  set: (patch: Partial<WizardState>) => void
  review: boolean
  agents: WizardAgent[]
}) {
  const ids = { cost: React.useId(), par: React.useId() }
  const asks = state.steps.map((s, i) => ({ s, i })).filter(({ s }) => s.kind === "ask")
  const usedAgents = Array.from(new Set(state.steps.filter((s) => s.kind === "agent" && s.agentSlug).map((s) => s.agentSlug!)))
  const agentNames = usedAgents.map((slug) => agents.find((a) => a.slug === slug && a.crew_id === state.crewId)?.name ?? slug)
  const hosts = state.steps.filter((s) => s.kind === "http").map((s) => s.url).filter(Boolean)
  const creds = state.steps.filter((s) => s.kind === "http" && s.credentialType?.trim()).map((s) => s.credentialType!)
  const need: [boolean, string, string][] = [
    [true, agentNames.length ? `Agents ${agentNames.join(", ")}` : "No agent steps", agentNames.length ? "from this crew" : "nothing runs as an agent"],
    [creds.length === 0, creds.length ? `Credentials ${creds.join(", ")}` : "No credentials", creds.length ? "read from the crew's Vault" : "nothing read from Vault"],
    [hosts.length === 0, hosts.length ? `Calls ${hosts.length === 1 ? hosts[0] : `${hosts.length} outside URLs`}` : "No outside calls", hosts.length ? "needs a Manager review" : "no outside host added"],
  ]

  return (
    <div className="flex flex-col gap-4">
      <CreateSurfaceGrid>
        <CreateSurfaceField label="Cost limit per run" htmlFor={ids.cost} hint="The run stops when it reaches this. Empty: no limit.">
          <div className="relative">
            <span className="pointer-events-none absolute inset-y-0 left-2.5 flex items-center text-xs text-muted-foreground">$</span>
            <Input id={ids.cost} type="number" min={0} step="0.01" value={state.maxCost} onChange={(e) => set({ maxCost: e.target.value })} className={cn(INPUT, "pl-6")} />
          </div>
        </CreateSurfaceField>
        <CreateSurfaceField
          label="If it is still running when the next start comes"
          hint={state.concurrency === "parallel" ? `Up to ${state.sideBySide} runs at once.` : "One run at a time; the next start waits in line."}
        >
          <CreateSurfaceChoice
            ariaLabel="If it is still running when the next start comes"
            value={state.concurrency}
            onChange={(concurrency) => set({ concurrency })}
            options={[
              { value: "queue", label: "Wait in line" },
              { value: "parallel", label: "Run side by side" },
            ]}
          />
        </CreateSurfaceField>
      </CreateSurfaceGrid>
      {state.concurrency === "parallel" && (
        <CreateSurfaceField label="Runs at the same time" htmlFor={ids.par}>
          <Input id={ids.par} type="number" min={2} max={20} value={state.sideBySide} onChange={(e) => set({ sideBySide: clamp(Number(e.target.value), 2, 20) })} className={cn(INPUT, "sm:w-32")} />
        </CreateSurfaceField>
      )}

      <CreateSurfaceSection title="Where it asks a person">
        {asks.length === 0 ? (
          <p className="text-xs text-muted-foreground">Nowhere — it runs to the end on its own.</p>
        ) : (
          asks.map(({ s, i }) => (
            <span key={s.id} className="flex items-start gap-2 text-xs">
              <Hand className="mt-0.5 h-3.5 w-3.5 shrink-0 text-warn" />
              <span>
                <span className="block font-medium text-foreground">Step {i + 1} · {s.question || s.name}</span>
                <span className="text-muted-foreground">A Manager decides · stops after {s.timeoutHours ?? 24} h without an answer</span>
              </span>
            </span>
          ))
        )}
      </CreateSurfaceSection>

      <CreateSurfaceSection title="What it needs">
        <div className="grid gap-2 sm:grid-cols-3">
          {need.map(([ok, title, hint]) => (
            <span key={title} className="flex items-start gap-2 text-xs">
              {ok ? <CheckCircle2 className="mt-0.5 h-3.5 w-3.5 shrink-0 text-success" /> : <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-warn" />}
              <span>
                <span className="block font-medium text-foreground">{title}</span>
                <span className="text-muted-foreground">{hint}</span>
              </span>
            </span>
          ))}
        </div>
      </CreateSurfaceSection>

      <CreateSurfaceNotice tone={review ? "warn" : "ok"} icon={review ? AlertTriangle : CheckCircle2}>
        {review
          ? "Because it reaches outside, it is saved as Waiting for review; a Manager approves it in Inbox. Its schedule waits until then."
          : "Nothing here needs a review. It goes live right after the test."}
      </CreateSurfaceNotice>
    </div>
  )
}

/* ---------------------------------------------------------- Test and save */

export type TestState =
  | { state: "idle" }
  | { state: "running" }
  | { state: "passed"; token: string; expiresAt: number; wouldExecute: { step_id: string; would_call_agent?: string; estimated_cost_usd?: number }[] }
  | { state: "failed"; problems: ValidationProblem[] }
  | { state: "expired" }

export function TestPane({
  state,
  test,
  now,
  onTest,
  onGoToStep,
  definition,
  crewName,
  next,
  afterSave,
}: {
  state: WizardState
  test: TestState
  now: number
  onTest: () => void
  onGoToStep: (index: number) => void
  definition: Record<string, unknown>
  crewName: string
  next: NextStarts
  afterSave: React.ReactNode
}) {
  const passed = test.state === "passed"
  const left = passed ? Math.max(0, test.expiresAt - now) : 0
  const mmss = `${Math.floor(left / 60_000)}:${String(Math.floor((left % 60_000) / 1000)).padStart(2, "0")}`
  const agentSteps = state.steps.filter((s) => s.kind === "agent").length
  const cost = passed ? test.wouldExecute.reduce((sum, w) => sum + (w.estimated_cost_usd ?? 0), 0) : 0

  return (
    <div className="flex flex-col gap-4">
      <div className={cn("flex flex-col gap-3 rounded-lg border p-3", passed ? "border-success/40 bg-success/[0.05]" : "border-hairline bg-foreground/[0.02]")}>
        <div className="flex flex-wrap items-center gap-3">
          {passed ? <CheckCircle2 className="h-5 w-5 shrink-0 text-success" /> : <Play className="h-5 w-5 shrink-0 text-muted-foreground" />}
          <span className="min-w-0 flex-1">
            <span className="block text-body font-medium text-foreground">{passed ? "Test passed" : "Test it once, without side effects"}</span>
            <span className="block text-xs text-muted-foreground">
              {passed
                ? `You have ${mmss} to save this version. Change anything and it needs a new test.`
                : test.state === "expired"
                  ? "That test is no longer valid — it is older than 5 minutes or the routine changed. Test it again."
                  : "Nothing is published, posted or paid for. Saving needs a passed test."}
            </span>
          </span>
          {passed || test.state === "expired" || test.state === "failed" ? (
            <Button type="button" variant="outline" size="sm" onClick={onTest} className="h-8 gap-1.5 text-xs coarse:h-[44px]">
              <RefreshCw className="h-3 w-3" />
              Test again
            </Button>
          ) : (
            <Button type="button" size="sm" onClick={onTest} disabled={test.state === "running"} className="h-8 gap-1.5 text-xs coarse:h-[44px]">
              {test.state === "running" ? <Spinner className="h-3 w-3" /> : <Play className="h-3 w-3" />}
              {test.state === "running" ? "Testing…" : "Run the test"}
            </Button>
          )}
        </div>
        {passed && (
          <ul className="grid gap-2 sm:grid-cols-2">
            <Result title="Definition is valid" hint={`${state.steps.length} ${state.steps.length === 1 ? "step" : "steps"} · DSL 1.0`} />
            {agentSteps > 0 && <Result title={`Agents from ${crewName}`} hint="checked against the crew again when it is saved" />}
            {state.trigger.kind === "schedule" && next.state === "ok" && next.occurrences[0] && (
              <Result title="Schedule parses" hint={`next start ${formatStart(next.occurrences[0], state.trigger.timezone || undefined)}`} />
            )}
            <Result title="Dry run without side effects" hint={`${test.wouldExecute.length} ${test.wouldExecute.length === 1 ? "step" : "steps"} would run`} />
            {cost > 0 && <Result title="Cost estimate" hint={`about $${cost.toFixed(2)}${state.maxCost ? ` of the $${Number(state.maxCost).toFixed(2)} limit` : ""}`} />}
          </ul>
        )}
        {test.state === "failed" && (
          <ul role="alert" className="flex flex-col gap-1.5">
            {test.problems.map((p, i) => (
              <li key={`${p.path}-${i}`} className="flex flex-wrap items-start gap-2 text-xs">
                <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-destructive" />
                <span className="min-w-0 flex-1 text-foreground">
                  {p.path && <span className="font-mono text-destructive">{p.path} </span>}
                  {p.message}
                </span>
                {p.stepIndex != null && (
                  <button type="button" onClick={() => onGoToStep(p.stepIndex!)} className="shrink-0 text-primary-hover hover:underline">
                    Go to step {p.stepIndex + 1}
                  </button>
                )}
              </li>
            ))}
          </ul>
        )}
      </div>

      {afterSave}

      <CreateSurfaceDisclosure label="See it as JSON" summary="the definition the server stores and the CLI reads">
        <pre className="max-h-72 overflow-auto rounded-md border border-hairline bg-background p-3 font-mono text-label text-foreground">{JSON.stringify(definition, null, 2)}</pre>
      </CreateSurfaceDisclosure>
    </div>
  )
}

function Result({ title, hint }: { title: string; hint: string }) {
  return (
    <li className="flex items-start gap-2 text-xs">
      <CheckCircle2 className="mt-0.5 h-3.5 w-3.5 shrink-0 text-success" />
      <span>
        <span className="block font-medium text-foreground">{title}</span>
        <span className="text-muted-foreground">{hint}</span>
      </span>
    </li>
  )
}
