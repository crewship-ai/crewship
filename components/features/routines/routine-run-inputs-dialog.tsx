"use client"

import { RoutineRunEffects } from "./routine-run-effects"
import { useRef, useState } from "react"
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
 * Even without input questions, a real run requires a review of its effects.
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
  submitting,
  onCancel,
  onRun,
}: RoutineRunInputsDialogProps) {
  if (inputs === null) return null
  return (
    <Dialog open onOpenChange={(open) => !open && !submitting && onCancel()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto border-border bg-card sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {submitLabel} {routineName}
          </DialogTitle>
          <DialogDescription>
            Review the inputs for this run. Saved defaults are filled in below.{" "}
            {submitLabel === "Schedule"
              ? "The routine will start at the selected date and time."
              : "Starting creates a new run in this routine’s history."}
          </DialogDescription>
        </DialogHeader>
        <RoutineRunEffects definition={definition} />
        {versionChoices && (
          <p className="text-xs text-muted-foreground">
            A new run repeats work using the selected version. It does not resume the
            previous attempt or undo its actions.
          </p>
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
              Inputs are copied from the selected historical run where names match. Review
              them before repeating external actions.
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
}: {
  inputs: RoutineInputSpec[]
  initialInputs?: Record<string, unknown>
  submitLabel?: string
  submitting?: boolean
  onCancel?: () => void
  onRun: (inputs: Record<string, unknown>) => void
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
        return
      }
      throw err
    }
  }

  return (
    <form ref={formRef} noValidate onSubmit={handleSubmit} className="space-y-4">
      {fields.map((f) => (
        <div
          key={f.name}
          data-input-name={f.name}
          onBlurCapture={() => validateField(f.name)}
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
                  {f.default !== "" ? "Changed from recipe default" : "Provided value"}
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
      <DialogFooter>
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
          {submitting
            ? submitLabel === "Run"
              ? "Running…"
              : submitLabel.toLowerCase().includes("schedule")
                ? "Scheduling…"
                : "Working…"
            : submitLabel}
        </Button>
      </DialogFooter>
    </form>
  )
}
