"use client"

import { useState } from "react"
import { HumanDecisionForm } from "@/components/features/approvals/human-decision-form"
import { MessageSquareText, Plus, Trash2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { isDecisionForm, type DecisionForm } from "@/lib/decision-form"
import { RoutineInputFormBuilder } from "./routine-input-form-builder"

export function RoutineDecisionFormBuilder({
  value,
  onChange,
  onOpenCode,
}: {
  value: unknown
  onChange: (value: DecisionForm | undefined) => void
  onOpenCode: () => void
}) {
  if (value == null)
    return (
      <section className="space-y-3 rounded-2xl border border-hairline bg-muted/20 p-4">
        <div className="flex items-center gap-2">
          <MessageSquareText className="size-4 text-primary" />
          <h3 className="text-sm font-medium">Decision form</h3>
        </div>
        <p className="text-sm text-muted-foreground">
          This step uses standard approval. Add questions and named decisions when the reviewer
          needs to provide more information.
        </p>
        <Button
          type="button"
          variant="outline"
          onClick={() =>
            onChange({
              fields: [],
              actions: [
                { id: "continue", label: "Approve and continue", approved: true },
                { id: "reject", label: "Reject", approved: false },
              ],
            })
          }
        >
          Customize decision
        </Button>
      </section>
    )
  if (
    !isDecisionForm(value) ||
    new Set(value.actions.map((a) => a.id)).size !== value.actions.length
  )
    return (
      <section className="rounded-xl border border-warn/30 bg-warn/5 p-4">
        <p className="text-sm">
          This decision uses a form the visual editor cannot read. Its definition is preserved.
        </p>
        <Button type="button" variant="link" onClick={onOpenCode}>
          Review in Code
        </Button>
      </section>
    )
  const patchAction = (index: number, patch: Partial<DecisionForm["actions"][number]>) =>
    onChange({
      ...value,
      actions: value.actions.map((action, i) => (i === index ? { ...action, ...patch } : action)),
    })
  return (
    <section className="space-y-4" aria-label="Decision form editor">
      <RoutineInputFormBuilder
        context="decision"
        inputs={value.fields}
        onChange={(fields) => onChange({ ...value, fields })}
      />
      <div className="space-y-4 rounded-2xl border border-hairline p-4">
        <div>
          <h3 className="text-sm font-medium">Reviewer decisions</h3>
          <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
            Choose the buttons the reviewer sees and whether each decision continues or stops the
            workflow. Stopping does not require answers to mandatory questions.
          </p>
        </div>
        {value.actions.map((action, index) => (
          <div key={action.id} className="space-y-3 rounded-xl bg-muted/30 p-3">
            <div className="flex items-end gap-2">
              <label className="min-w-0 flex-1 space-y-1 text-xs">
                <span>Decision {index + 1}</span>
                <Input
                  value={action.label}
                  onChange={(e) => patchAction(index, { label: e.target.value })}
                />
              </label>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                disabled={value.actions.length <= 2}
                aria-label={`Remove decision ${index + 1}`}
                onClick={() =>
                  onChange({ ...value, actions: value.actions.filter((_, i) => i !== index) })
                }
              >
                <Trash2 className="size-4" />
              </Button>
            </div>
            <label className="block space-y-1 text-xs">
              <span>After this decision</span>
              <select
                aria-label={`Outcome of decision ${index + 1}`}
                className="min-h-10 w-full rounded-md border bg-background px-3 text-sm"
                value={action.approved ? "continue" : "stop"}
                onChange={(e) => patchAction(index, { approved: e.target.value === "continue" })}
              >
                <option value="continue">Continue the workflow</option>
                <option value="stop">Stop the workflow</option>
              </select>
            </label>
            <details className="text-[11px] text-muted-foreground">
              <summary className="cursor-pointer">Advanced reference</summary>
              <code className="mt-1 block break-all">{action.id}</code>
            </details>
          </div>
        ))}
        <Button
          type="button"
          variant="outline"
          disabled={value.actions.length >= 20}
          onClick={() => {
            let index = value.actions.length + 1
            while (value.actions.some((a) => a.id === `decision_${index}`)) index++
            onChange({
              ...value,
              actions: [
                ...value.actions,
                { id: `decision_${index}`, label: "New decision", approved: false },
              ],
            })
          }}
        >
          <Plus className="size-4" />
          Add decision
        </Button>
      </div>
      <details className="rounded-2xl border border-hairline p-4">
        <summary className="cursor-pointer text-sm font-medium">
          Preview the reviewer experience
        </summary>
        <div className="mt-4">
          <DecisionPreview key={JSON.stringify(value)} form={value} />
        </div>
      </details>
      <details className="rounded-xl border border-hairline p-3">
        <summary className="cursor-pointer text-xs text-muted-foreground">
          Use standard approval instead
        </summary>
        <p className="mt-2 text-xs text-muted-foreground">
          This removes the custom questions and decisions from this draft. Already pending decisions
          keep their saved form.
        </p>
        <Button
          type="button"
          variant="outline"
          className="mt-3"
          onClick={() => onChange(undefined)}
        >
          Remove custom decision form
        </Button>
      </details>
    </section>
  )
}

function DecisionPreview({ form }: { form: DecisionForm }) {
  const [outcome, setOutcome] = useState<string | null>(null)
  return (
    <div className="space-y-3">
      <p className="text-xs text-muted-foreground">
        Preview only. Nothing is submitted and no workflow runs.
      </p>
      <HumanDecisionForm
        form={form}
        onDecide={async (approved) => {
          setOutcome(
            approved
              ? "This decision would continue the workflow."
              : "This decision would stop the workflow.",
          )
          return true
        }}
      />
      {outcome && (
        <p role="status" className="rounded-xl bg-muted/40 p-3 text-sm">
          {outcome}
        </p>
      )}
    </div>
  )
}
