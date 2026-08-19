"use client"

import { useState } from "react"
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
  isMissingRequired,
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
 * A routine that declares NO inputs never sees this dialog — the caller
 * checks first and runs straight away, so the button keeps its
 * single-click behaviour everywhere it always had it.
 */
export interface RoutineRunInputsDialogProps {
  /** Open when non-null; the specs are the routine's declared inputs. */
  inputs: RoutineInputSpec[] | null
  /** Routine name for the heading — what the user clicked Run on. */
  routineName: string
  submitting?: boolean
  onCancel: () => void
  /** Receives the typed `inputs` map, ready to post. */
  onRun: (inputs: Record<string, unknown>) => void
}

export function RoutineRunInputsDialog({
  inputs,
  routineName,
  submitting,
  onCancel,
  onRun,
}: RoutineRunInputsDialogProps) {
  if (!inputs?.length) return null
  return (
    <Dialog open onOpenChange={(open) => !open && onCancel()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Run {routineName}</DialogTitle>
          <DialogDescription>
            Fill in this run&apos;s inputs. Anything left empty falls back to the
            routine&apos;s own default.
          </DialogDescription>
        </DialogHeader>
        {/* Keyed on the routine so switching selection in the list
            rebuilds the form at the new routine's defaults rather than
            carrying the previous one's answers across. */}
        <InputsForm
          key={routineName}
          inputs={inputs}
          submitting={submitting}
          onCancel={onCancel}
          onRun={onRun}
        />
      </DialogContent>
    </Dialog>
  )
}

function InputsForm({
  inputs,
  submitting,
  onCancel,
  onRun,
}: {
  inputs: RoutineInputSpec[]
  submitting?: boolean
  onCancel: () => void
  onRun: (inputs: Record<string, unknown>) => void
}) {
  const fields = slashFieldsFromRoutineInputs(inputs)
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(fields.map((f) => [f.name, f.default ?? ""])),
  )
  // Keyed by field name so the message sits under the box it is about,
  // rather than in a toast the user has to map back onto a form.
  const [errors, setErrors] = useState<Record<string, string>>({})

  const setField = (name: string) => (e: { target: { value: string } }) => {
    setValues((prev) => ({ ...prev, [name]: e.target.value }))
    // Clear this field's error as soon as it is edited — leaving it up
    // while the user fixes it reads as "still wrong".
    setErrors((prev) => (name in prev ? Object.fromEntries(Object.entries(prev).filter(([k]) => k !== name)) : prev))
  }

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    const missing: Record<string, string> = {}
    for (const f of fields) {
      if (isMissingRequired(f, values[f.name])) {
        missing[f.name] = "Required"
      }
    }
    if (Object.keys(missing).length > 0) {
      setErrors(missing)
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
    <form onSubmit={handleSubmit} className="space-y-4">
      {fields.map((f) => (
        <div key={f.name} className="space-y-1">
          <FormField
            field={f}
            value={values[f.name] ?? ""}
            onChange={setField(f.name)}
            idPrefix="routine-input-"
          />
          {errors[f.name] && (
            <p data-testid={`routine-input-error-${f.name}`} className="text-xs text-destructive">
              {errors[f.name]}
            </p>
          )}
        </div>
      ))}
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onCancel} disabled={submitting}>
          Cancel
        </Button>
        <Button type="submit" disabled={submitting}>
          {submitting ? "Running…" : "Run"}
        </Button>
      </DialogFooter>
    </form>
  )
}
