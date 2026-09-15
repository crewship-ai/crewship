"use client"

import { RoutineRunEffects, routineRunFacts } from "./routine-run-effects"
import { useRef, useState, type ReactNode } from "react"
import { Clock, DollarSign, Hand, Layers, Play, Users } from "lucide-react"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
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
export interface RoutineRunInputsDialogProps {
  definition?: Record<string, unknown> | null
  submitLabel?: "Run" | "Schedule"
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
  onCancel: () => void
  /** Receives the typed `inputs` map, ready to post. */
  onRun: (inputs: Record<string, unknown>) => void
}

export function RoutineRunInputsDialog({
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
  onCancel,
  onRun,
}: RoutineRunInputsDialogProps) {
  if (inputs === null) return null
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
  return (
    <Dialog open onOpenChange={(open) => !open && !submitting && onCancel()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto border-border bg-card sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {submitLabel} {routineName}
          </DialogTitle>
          <DialogDescription>
            {usedVersion ? (
              <>
                Uses <span className="font-medium text-foreground">v{usedVersion}</span>
                {draft ? ` — the unpublished draft r${draft.revision} is not used` : ""}
                {" · "}
              </>
            ) : draft ? (
              <>The unpublished draft r{draft.revision} is not used · </>
            ) : null}
            {submitLabel === "Schedule"
              ? "the routine will start at the selected date and time."
              : "a new run is added to History."}
          </DialogDescription>
        </DialogHeader>
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
        {versionChoices && (
          <div className="space-y-1">
            <label htmlFor="routine-run-version" className="text-sm font-medium">
              Recipe version
            </label>
            <select
              id="routine-run-version"
              className="w-full rounded-md border bg-card p-2 text-sm"
              value={selectedVersion}
              onChange={(e) => onVersionChange?.(e.target.value)}
              disabled={submitting}
            >
              {versionChoices.map((v) => (
                <option key={v.value} value={v.value}>
                  {v.label}
                </option>
              ))}
            </select>
            <p className="text-xs text-muted-foreground">
              Values below are copied from the earlier run where names match. A new run
              repeats the work; it does not resume the earlier one or undo its actions.
            </p>
          </div>
        )}
        {/* Keyed on the routine so switching selection in the list
            rebuilds the form at the new routine's defaults rather than
            carrying the previous one's answers across. */}
        <InputsForm
          key={`${routineName}:${selectedVersion ?? "current"}`}
          inputs={inputs}
          initialInputs={initialInputs}
          submitLabel={submitLabel}
          submitting={submitting}
          onCancel={onCancel}
          onRun={onRun}
          footer={<RoutineRunEffects definition={definition} />}
          hint={
            submitLabel === "Schedule"
              ? "Esc closes · nothing is scheduled yet"
              : "Esc closes · nothing has started"
          }
        />
      </DialogContent>
    </Dialog>
  )
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
    <form ref={formRef} noValidate onSubmit={handleSubmit} className="space-y-4">
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
      <DialogFooter className="sm:items-center">
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
          {submitting ? (
            isRun ? (
              "Running…"
            ) : submitLabel.toLowerCase().includes("schedule") ? (
              "Scheduling…"
            ) : (
              "Working…"
            )
          ) : isRun ? (
            <>
              <Play className="mr-1 h-3.5 w-3.5" aria-hidden />
              Run now
            </>
          ) : (
            submitLabel
          )}
        </Button>
      </DialogFooter>
    </form>
  )
}
