"use client"

import { RoutineRunEffects, routineRunFacts } from "./routine-run-effects"
import { useId, useRef, useState, type ReactNode } from "react"
import { Clock, DollarSign, Eye, Hand, Layers, Play, Users } from "lucide-react"
import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceChoice,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceNotice,
} from "@/components/layout/create-surface"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { FormField } from "@/components/features/chat/asks/form-field"
import {
  RoutineInputError,
  routineInputErrors,
  routineInputHint,
  formatInputDefault,
  routineInputsFromValues,
  slashFieldsFromRoutineInputs,
  type RoutineInputSpec,
} from "@/lib/routine-inputs"
import { cn } from "@/lib/utils"

/**
 * Ask for a routine's inputs before running it.
 *
 * Run used to POST `{"inputs":{}}` unconditionally — its own tooltip
 * said "Invoke routine with empty inputs" — so a routine that declares
 * inputs could only ever be run at its defaults from this surface. For
 * a routine whose whole argument is which month to bill, that is a
 * button that does one thing and offers no way to say which.
 *
 * The form is the same one the slash palette opens, built from the same
 * translation (lib/routine-inputs.ts) and rendered by the same field
 * component. Two ways in, one form: what `/msn-etn-podklady` asks for in
 * chat is what Run asks for here, prefilled the same way.
 *
 * The header says which version the run uses and that a draft is not it;
 * the chip row says who decides and what it costs; one sentence at the
 * bottom says what the run will do (operator console proposal, screen 3).
 */
/** v67 D-RTRUN: one dialog for the three ways to start a routine. */
export type RunMode = "run" | "test" | "later"

/** What the person chose besides the inputs (only with `modes`). */
export interface RunStartOptions {
  mode: RunMode
  /** Later: when it starts (RFC 3339) — POST …/run `fire_at`. */
  fireAt?: string
  /** Later: the version it runs, or null to follow the live one — `pinned_version`. */
  pinnedVersion?: number | null
}

const MODE_LABEL: Record<RunMode, string> = { run: "Run now", test: "Test run", later: "Later" }

/** The footer's limits, from the definition: "Max $2.00 · 1 at a time". */
export function routineLimitsLine(definition: Record<string, unknown> | null | undefined): string | null {
  if (!definition) return null
  const parts: string[] = []
  const cap = definition.max_cost_usd
  if (typeof cap === "number" && cap > 0) parts.push(`Max $${cap.toFixed(2)}`)
  const max = definition.max_concurrent
  if (typeof max === "number" && max > 1) parts.push(`Up to ${max} at a time`)
  else if ((typeof definition.concurrency_key === "string" && definition.concurrency_key) || max === 1) parts.push("1 at a time")
  return parts.length ? parts.join(" · ") : null
}

export interface RoutineRunInputsDialogProps {
  definition?: Record<string, unknown> | null
  submitLabel?: "Run" | "Schedule" | "Test"
  /** Run now · Test run · Later as one segment. Without it the dialog is the
   *  single-purpose form `submitLabel` names (Run again, Schedule once). */
  modes?: RunMode[]
  initialMode?: RunMode
  /** Open when non-null; the specs are the routine's declared inputs. */
  versionChoices?: { value: string; label: string }[]
  selectedVersion?: string
  onVersionChange?: (value: string) => void
  initialInputs?: Record<string, unknown>
  inputs: RoutineInputSpec[] | null
  /** Routine name for the heading — what the user clicked Run on. */
  routineName: string
  /** The published version the run uses (`head_version` on the routine). */
  headVersion?: number | null
  /** The routine's unpublished draft, when one exists — named so the reader
   * knows the run does not use it. */
  draft?: { revision: number } | null
  /** The crew whose agents do the work, for the chip row. */
  crewName?: string | null
  submitting?: boolean
  error?: string | null
  onReload?: () => void
  onCancel: () => void
  /** Receives the typed `inputs` map, ready to post — and, with `modes`, the
   *  chosen way to start it. */
  onRun: (inputs: Record<string, unknown>, options?: RunStartOptions) => void
}

export function RoutineRunInputsDialog(props: RoutineRunInputsDialogProps) {
  // Mounted per opening, so the mode, the time and the version start fresh.
  if (props.inputs === null) return null
  return <OpenRunInputsDialog {...props} inputs={props.inputs} />
}

function OpenRunInputsDialog({
  definition,
  initialInputs,
  inputs,
  versionChoices,
  selectedVersion,
  onVersionChange,
  submitLabel = "Run",
  routineName,
  headVersion,
  draft,
  crewName,
  submitting,
  error,
  onReload,
  onCancel,
  onRun,
  modes,
  initialMode,
}: RoutineRunInputsDialogProps & { inputs: RoutineInputSpec[] }) {
  const formId = useId()
  const [mode, setMode] = useState<RunMode>(initialMode ?? modes?.[0] ?? "run")
  const [startAt, setStartAt] = useState("")
  const [pin, setPin] = useState("follow")
  const [laterError, setLaterError] = useState<string | null>(null)
  const segmented = !!modes && modes.length > 0
  const current: RunMode = segmented ? (modes!.includes(mode) ? mode : modes![0]) : "run"
  const verb = segmented ? (current === "test" ? "Test" : "Run") : submitLabel
  const limits = routineLimitsLine(definition)
  const submit = (values: Record<string, unknown>) => {
    if (!segmented) return onRun(values)
    if (current !== "later") return onRun(values, { mode: current })
    const at = startAt ? new Date(startAt) : null
    if (!at || Number.isNaN(at.getTime())) return setLaterError("Pick when it starts.")
    if (at.getTime() <= Date.now()) return setLaterError("Pick a time in the future.")
    setLaterError(null)
    onRun(values, { mode: "later", fireAt: at.toISOString(), pinnedVersion: pin === "follow" ? null : Number(pin) })
  }
  const primaryText = !segmented
    ? inputsSubmitContent(submitLabel, submitting)
    : current === "test"
      ? submitting ? "Starting…" : "Start test run"
      : current === "later"
        ? submitting ? "Planning…" : "Plan run"
        : inputsSubmitContent("Run", submitting)
  // The version the run uses: a chosen historical version, else the head.
  const usedVersion =
    selectedVersion && selectedVersion !== "current"
      ? Number(selectedVersion)
      : headVersion ?? null
  const facts = routineRunFacts(definition, crewName)
  const chips: { icon: typeof Layers; text: string; set?: boolean }[] = []
  if (usedVersion) chips.push({ icon: Layers, text: `Version v${usedVersion}`, set: true })
  if (facts.crew) chips.push({ icon: Users, text: `Crew · ${facts.crew}` })
  if (facts.decides) chips.push({ icon: Hand, text: facts.decides })
  if (facts.cost) chips.push({ icon: DollarSign, text: facts.cost })
  if (facts.duration) chips.push({ icon: Clock, text: facts.duration })
  const dismiss = () => {
    if (!submitting) onCancel()
  }
  return (
    <CreateSurface
      open
      onOpenChange={(open) => !open && dismiss()}
      size="sm"
      onSubmit={() => submitInputsForm(formId)}
    >
      <CreateSurfaceHeader
        concept="routines"
        icon={segmented ? (current === "later" ? Clock : current === "test" ? Eye : Play) : submitLabel === "Run" ? Play : Clock}
        title={`${verb} ${routineName}`}
        description={
          <>
            {usedVersion ? (
              <>
                Uses <span className="font-medium text-foreground">v{usedVersion}</span>
                {draft ? ` — the unpublished draft r${draft.revision} is not used` : ""}
                {" · "}
              </>
            ) : draft ? (
              <>The unpublished draft r{draft.revision} is not used · </>
            ) : null}
            {(segmented ? current === "later" : submitLabel === "Schedule")
              ? "the routine will start at the selected date and time."
              : (segmented ? current === "test" : submitLabel === "Test")
                ? "a test without side effects: nothing is sent, written or published."
                : "a new run is added to History."}
          </>
        }
        onClose={dismiss}
      />
      <CreateSurfaceBody className="space-y-4">
        {error && <p role="alert" className="text-sm text-destructive">{error}{onReload && <Button type="button" variant="outline" size="sm" className="ml-2" onClick={onReload}>Reload recipe</Button>}</p>}
        {chips.length > 0 && (
          <ul aria-label="Run facts" className="flex flex-wrap gap-1.5">
            {chips.map((chip) => (
              <li
                key={chip.text}
                className={cn(
                  "inline-flex items-center gap-1.5 rounded-lg border px-2 py-1 text-xs",
                  chip.set
                    ? "border-primary/40 bg-primary/10 text-primary"
                    : "border-border/60 bg-card text-foreground",
                )}
              >
                <chip.icon className="h-3 w-3 opacity-70" aria-hidden />
                {chip.text}
              </li>
            ))}
          </ul>
        )}
        {segmented && modes!.length > 1 && (
          <CreateSurfaceChoice<RunMode>
            ariaLabel="How to run"
            value={current}
            onChange={(m) => {
              setMode(m)
              setLaterError(null)
            }}
            options={modes!.map((m) => ({ value: m, label: MODE_LABEL[m] }))}
          />
        )}
        {segmented && current === "test" && (
          <CreateSurfaceNotice>
            <b className="font-medium">No side effects</b> — messages, publishing and writes to other systems are simulated. Agents still run and cost money.
            {headVersion ? ` Tests the live version v${headVersion}.` : ""}
          </CreateSurfaceNotice>
        )}
        {segmented && current === "later" && (
          <div className="grid gap-3 sm:grid-cols-2">
            <CreateSurfaceField label="Start at" htmlFor={`${formId}-start`} hint="Creates a one-time run you can cancel in Triggers">
              <Input
                id={`${formId}-start`}
                type="datetime-local"
                value={startAt}
                onChange={(e) => {
                  setStartAt(e.target.value)
                  setLaterError(null)
                }}
                aria-invalid={!!laterError}
                className={CREATE_SURFACE_INPUT}
              />
            </CreateSurfaceField>
            <CreateSurfaceField label="Version" hint={pin === "follow" ? "Runs whatever is live when it starts" : "Runs this version even if a newer one is published"}>
              <Select value={pin} onValueChange={setPin} disabled={submitting}>
                <SelectTrigger aria-label="Version" size="sm" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="follow">Follow the live version</SelectItem>
                  {headVersion ? <SelectItem value={String(headVersion)}>{`Pin v${headVersion}`}</SelectItem> : null}
                </SelectContent>
              </Select>
            </CreateSurfaceField>
            {laterError && (
              <p role="alert" className="text-xs text-destructive sm:col-span-2">
                {laterError}
              </p>
            )}
          </div>
        )}
        {versionChoices && (
          <div className="space-y-1">
            <span className="text-sm font-medium">Recipe version</span>
            <Select value={selectedVersion} onValueChange={(v) => onVersionChange?.(v)} disabled={submitting}>
              <SelectTrigger aria-label="Recipe version" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {versionChoices.map((v) => (
                  <SelectItem key={v.value} value={v.value}>
                    {v.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-xs text-muted-foreground">
              Values below are copied from the earlier run where names match. A new run
              repeats the work; it does not resume the earlier one or undo its actions.
              Inputs are stored as entered and this form has no secret input type.
            </p>
          </div>
        )}
        {/* Keyed on the routine so switching selection in the list
            rebuilds the form at the new routine's defaults rather than
            carrying the previous one's answers across. */}
        <InputsForm
          key={`${routineName}:${selectedVersion ?? "current"}`}
          formId={formId}
          inputs={inputs}
          initialInputs={initialInputs}
          submitLabel={submitLabel}
          submitting={submitting}
          onRun={submit}
          footer={<RoutineRunEffects definition={definition} />}
        />
      </CreateSurfaceBody>
      <CreateSurfaceFooter
        hint={
          segmented && limits
            ? limits
            : (segmented ? current === "later" : submitLabel === "Schedule")
              ? "Esc closes · nothing is scheduled yet"
              : "Esc closes · nothing has started"
        }
        onCancel={onCancel}
        guardCancel={false}
        primaryLabel={primaryText}
        primaryIcon={(segmented ? current !== "later" : submitLabel === "Run") && !submitting ? Play : undefined}
        onPrimary={() => submitInputsForm(formId)}
        busy={submitting}
      />
    </CreateSurface>
  )
}

/** Submit an `InputsForm` rendered with `formId` from a control outside it —
 *  a CreateSurface footer, or the shell's ⌘↵. Goes through the form's own
 *  submit handler, so validation runs exactly as it does on Enter. */
export function submitInputsForm(formId: string) {
  const form = document.getElementById(formId)
  if (form instanceof HTMLFormElement) form.requestSubmit()
}

/** The submit button's text, shared by the inline actions and a surface footer. */
export function inputsSubmitContent(submitLabel: string, submitting?: boolean): string {
  const isRun = submitLabel === "Run"
  if (submitting) {
    if (isRun) return "Running…"
    return submitLabel.toLowerCase().includes("schedule") ? "Scheduling…" : "Working…"
  }
  return isRun ? "Run now" : submitLabel
}

export function InputsForm({
  inputs,
  initialInputs,
  submitting,
  onCancel,
  onRun,
  submitLabel = "Run",
  footer,
  hint,
  formId,
}: {
  inputs: RoutineInputSpec[]
  initialInputs?: Record<string, unknown>
  submitLabel?: string
  submitting?: boolean
  onCancel?: () => void
  onRun: (inputs: Record<string, unknown>) => void
  /** Rendered between the fields and the buttons — the "This run will" line. */
  footer?: ReactNode
  /** Muted text at the left of the buttons. */
  hint?: string
  /**
   * Render inside a CreateSurface: the form takes this id and leaves its
   * actions to the surface footer (which submits it with `submitInputsForm`).
   * Enter in a field still submits through a visually hidden default button.
   */
  formId?: string
}) {
  const fields = slashFieldsFromRoutineInputs(inputs)
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(
      fields.map((f) => [
        f.name,
        initialInputs && Object.hasOwn(initialInputs, f.name)
          ? formatInputDefault(initialInputs[f.name])
          : (f.default ?? ""),
      ]),
    ),
  )
  const formRef = useRef<HTMLFormElement>(null)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [touched, setTouched] = useState<Set<string>>(new Set())
  // The summary at the top counts only the errors a submit found; an error
  // that appears while typing does not need announcing twice.
  const [submitErrors, setSubmitErrors] = useState(0)
  const setField = (name: string) => (e: { target: { value: string } }) => {
    const next = { ...values, [name]: e.target.value }
    setValues(next)
    if (touched.has(name))
      setErrors(
        routineInputErrors(
          fields.filter((f) => touched.has(f.name)),
          next,
        ),
      )
  }
  const validateField = (name: string) => {
    const next = new Set(touched).add(name)
    setTouched(next)
    setErrors(
      routineInputErrors(
        fields.filter((f) => next.has(f.name)),
        values,
      ),
    )
  }

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (submitting) return
    const nextErrors = routineInputErrors(fields, values)
    for (const input of formRef.current?.querySelectorAll<HTMLInputElement>(
      "input[type=number]",
    ) ?? []) {
      if (input.validity.badInput)
        nextErrors[input.id.replace("routine-input-", "")] =
          "Enter a complete number using a decimal point"
    }
    setTouched(new Set(fields.map((f) => f.name)))
    setErrors(nextErrors)
    setSubmitErrors(Object.keys(nextErrors).length)
    if (Object.keys(nextErrors).length > 0) {
      const name = fields.find((f) => nextErrors[f.name])?.name
      const row = Array.from(
        formRef.current?.querySelectorAll<HTMLElement>("[data-input-name]") ?? [],
      ).find((el) => el.dataset.inputName === name)
      row?.querySelector<HTMLElement>("input, textarea, button, select")?.focus()
      return
    }
    try {
      onRun(routineInputsFromValues(fields, values))
    } catch (err) {
      // A value that cannot be restored to its declared type keeps the
      // form open with the field named. The run is not started.
      if (err instanceof RoutineInputError) {
        setErrors({ [err.field]: err.message.replace(`${err.field}: `, "") })
        setSubmitErrors(1)
        return
      }
      throw err
    }
  }

  const remaining = Object.keys(errors).length
  const summary = submitErrors > 0 && remaining > 0 ? Math.min(submitErrors, remaining) : 0
  const isRun = submitLabel === "Run"

  return (
    <form id={formId} ref={formRef} noValidate onSubmit={handleSubmit} className="space-y-4">
      {summary > 0 && (
        <p
          role="alert"
          data-testid="routine-input-error-summary"
          className="rounded-lg bg-destructive/10 px-3 py-2 text-xs font-medium text-destructive"
        >
          {summary === 1 ? "1 answer needs a fix" : `${summary} answers need a fix`} — see
          below.
        </p>
      )}
      {fields.map((f) => (
        <div
          key={f.name}
          data-input-name={f.name}
          onBlurCapture={(event) => {
            // Rendering a new error while a form action is receiving focus can
            // move that button between pointerdown and click, especially on phones.
            // Submission validates every field; reset/cancel own their action.
            const target = event.relatedTarget
            if (target instanceof HTMLElement && target.closest("button") && formRef.current?.contains(target)) return
            validateField(f.name)
          }}
          className="space-y-1"
        >
          <FormField
            field={f}
            disabled={submitting}
            invalid={!!errors[f.name]}
            describedBy={`routine-hint-${f.name}${errors[f.name] ? ` routine-error-${f.name}` : ""}`}
            value={values[f.name] ?? ""}
            onChange={setField(f.name)}
            idPrefix="routine-input-"
          />
          <p id={`routine-hint-${f.name}`} className="text-xs text-muted-foreground">
            {routineInputHint(f)}
          </p>
          {(values[f.name] ?? "") !== (f.default ?? "") &&
            !(
              f.value_type === "boolean" &&
              ["", "false"].includes(values[f.name] ?? "") &&
              ["", "false"].includes(f.default ?? "")
            ) && (
              <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <span>
                  {f.default !== ""
                    ? `Changed from the recipe default (${f.default})`
                    : "Provided value"}
                </span>
                <button
                  type="button"
                  disabled={submitting}
                  className="min-h-8 coarse:min-h-12 text-primary underline"
                  onClick={() => {
                    const next = { ...values, [f.name]: f.default ?? "" }
                    setValues(next)
                    setErrors(
                      routineInputErrors(
                        fields.filter((field) => touched.has(field.name)),
                        next,
                      ),
                    )
                  }}
                >
                  {f.default !== "" ? "Restore default" : "Clear value"}
                </button>
              </div>
            )}
          {errors[f.name] && (
            <p
              id={`routine-error-${f.name}`}
              role="alert"
              data-testid={`routine-input-error-${f.name}`}
              className="text-xs text-destructive"
            >
              {errors[f.name]}
            </p>
          )}
        </div>
      ))}
      {footer}
      {formId ? (
        // The implicit-submission target: without a submit button a form
        // with several fields does not submit on Enter.
        <button type="submit" tabIndex={-1} aria-hidden className="sr-only" disabled={submitting} />
      ) : (
        <div className="flex flex-col-reverse gap-2 sm:flex-row sm:items-center sm:justify-end">
          {hint && <span className="text-xs text-muted-foreground sm:mr-auto">{hint}</span>}
          {onCancel && (
            <Button
              type="button"
              variant="outline"
              onClick={onCancel}
              disabled={submitting}
            >
              Cancel
            </Button>
          )}
          <Button type="submit" disabled={submitting}>
            {isRun && !submitting && <Play className="mr-1 h-3.5 w-3.5" aria-hidden />}
            {inputsSubmitContent(submitLabel, submitting)}
          </Button>
        </div>
      )}
    </form>
  )
}
