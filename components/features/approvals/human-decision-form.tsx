"use client"

import { useId, useRef, useState } from "react"
import { Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { FormField } from "@/components/features/chat/asks/form-field"
import {
  isMissingRequired,
  RoutineInputError,
  routineInputsFromValues,
  slashFieldsFromRoutineInputs,
} from "@/lib/routine-inputs"
import type { DecisionAction, DecisionAnswer, DecisionForm } from "@/lib/decision-form"

/** Shared by the run and Inbox. The server validates against its frozen copy. */
export function HumanDecisionForm({
  form,
  disabled,
  onDecide,
}: {
  form: DecisionForm
  disabled?: boolean
  onDecide: (approved: boolean, answer: DecisionAnswer) => Promise<boolean>
}) {
  const prefix = useId()
  const fields = slashFieldsFromRoutineInputs(form.fields)
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(fields.map((f) => [f.name, f.default ?? ""])),
  )
  const [error, setError] = useState<string | null>(null)
  // The action being sent: its button keeps its size and shows a spinner in
  // place of its label (no layout shift), and every button waits for it.
  const [busy, setBusy] = useState<string | null>(null)
  const sending = useRef(false)
  const decide = async (action: DecisionAction) => {
    if (disabled || sending.current) return
    setError(null)
    try {
      const decisionFields = fields.map((f) => ({ ...f, required: action.approved && f.required }))
      const missing = decisionFields.find((f) => isMissingRequired(f, values[f.name]))
      if (missing) {
        setError(`${missing.label || missing.name}: Required`)
        return
      }
      const data: Record<string, unknown> = {}
      for (const field of decisionFields) {
        try {
          Object.assign(
            data,
            routineInputsFromValues([field], { [field.name]: values[field.name] }),
          )
        } catch (error) {
          // Rejection must remain possible while an answer is incomplete.
          // Keep valid feedback, omit values the server cannot accept.
          if (action.approved || !(error instanceof RoutineInputError)) throw error
        }
      }
      sending.current = true
      setBusy(action.id)
      if (!(await onDecide(action.approved, { action_id: action.id, data })))
        setError("Decision was not accepted. Review the fields and try again.")
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      sending.current = false
      setBusy(null)
    }
  }
  return (
    <div className="min-w-0 w-full space-y-3 [overflow-wrap:anywhere]">
      <fieldset disabled={disabled || busy !== null} className="min-w-0 space-y-3">
        {fields.map((field) => (
          <FormField
            key={field.name}
            field={field}
            value={values[field.name] ?? ""}
            idPrefix={prefix}
            onChange={(e) => {
              setValues((v) => ({ ...v, [field.name]: e.target.value }))
              setError(null)
            }}
          />
        ))}
        {/* The approving action last: the primary sits on the right, as in
            every decision dock. On a phone the buttons share the row. */}
        <div className="flex flex-wrap gap-2">
          {[...form.actions.filter((a) => !a.approved), ...form.actions.filter((a) => a.approved)].map((action) => {
            const pending = busy === action.id
            return (
              <Button
                type="button"
                key={action.id}
                className="relative h-auto min-h-9 max-w-full whitespace-normal [overflow-wrap:anywhere] max-md:flex-1"
                variant={action.approved ? "default" : "outline"}
                aria-busy={pending || undefined}
                onClick={() => void decide(action)}
              >
                <span data-slot="decision-label" className={pending ? "invisible" : undefined}>{action.label}</span>
                {pending && <Loader2 className="absolute h-4 w-4 animate-spin" aria-hidden />}
              </Button>
            )
          })}
        </div>
      </fieldset>
      {error && (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      )}
    </div>
  )
}
