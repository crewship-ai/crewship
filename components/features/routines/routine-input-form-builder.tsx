"use client"

import { useState } from "react"
import { Input } from "@/components/ui/input"
import { FormField } from "@/components/features/chat/asks/form-field"
import {
  coerceRoutineInput,
  formatInputDefault,
  slashFieldsFromRoutineInputs,
  type RoutineInputSpec,
} from "@/lib/routine-inputs"

const KINDS = [
  ["text", "Short text", "string", "text"],
  ["long", "Long text", "string", "textarea"],
  ["select", "Choose one", "string", "select"],
  ["multi", "Choose several", "array", "multiselect"],
  ["integer", "Whole number", "integer", "number"],
  ["number", "Decimal number", "number", "number"],
  ["boolean", "Yes / No", "boolean", "boolean"],
  ["object", "Structured data (JSON)", "object", "textarea"],
  ["array", "List (JSON)", "array", "textarea"],
] as const

function kindOf(input: RoutineInputSpec) {
  if (input.widget && !KINDS.some((k) => k[3] === input.widget)) return undefined
  if (input.type && !KINDS.some((k) => k[2] === input.type)) return undefined
  if (input.options?.length) return input.type === "array" ? "multi" : "select"
  return KINDS.find(
    (k) =>
      k[2] === (input.type || "string") &&
      k[3] ===
        (input.widget ||
          (!input.type || input.type === "string"
            ? "text"
            : input.type === "boolean"
              ? "boolean"
              : input.type === "array" || input.type === "object"
                ? "textarea"
                : "number")),
  )?.[0]
}

export function RoutineInputFormBuilder({
  inputs,
  onChange,
  context = "start",
}: {
  inputs: RoutineInputSpec[]
  onChange: (inputs: RoutineInputSpec[]) => void
  context?: "start" | "decision"
}) {
  const [editing, setEditing] = useState<number | null>(null)
  function update(index: number, patch: Partial<RoutineInputSpec>) {
    if (!kindOf(inputs[index])) return
    onChange(
      inputs.map((input, i) =>
        i === index
          ? {
              ...input,
              type: input.type || "string",
              widget: input.widget || KINDS.find((k) => k[0] === kindOf(input))![3],
              ...patch,
            }
          : input,
      ),
    )
  }
  function add() {
    let number = inputs.length + 1
    while (inputs.some((i) => i.name === `input_${number}`)) number++
    onChange([
      ...inputs,
      { name: `input_${number}`, label: "New question", type: "string", widget: "text" },
    ])
    setEditing(inputs.length)
  }
  if (
    inputs.some(
      (i) =>
        i.options != null &&
        (!Array.isArray(i.options) || i.options.some((o) => typeof o !== "string")),
    )
  )
    return (
      <p role="alert" className="text-sm text-destructive">
        The answer options are incomplete. Fix them in Code to use the form builder.
      </p>
    )
  return (
    <section
      className="space-y-4 rounded-2xl border border-hairline p-4 sm:p-5"
      aria-label={context === "decision" ? "Reviewer questions" : "What you provide"}
    >
      <div>
        <h3 className="font-medium">
          {context === "decision" ? "Reviewer questions" : "What you provide"}
        </h3>
        <p className="mt-1 text-sm text-muted-foreground">
          {context === "decision"
            ? "Collect the information needed for the next step. Required answers are checked when the reviewer continues the workflow."
            : "Prepare the questions people answer before starting. Defaults can be changed for each run."}
        </p>
      </div>
      {!inputs.length && (
        <p className="text-sm text-muted-foreground">
          {context === "decision"
            ? "No extra questions. The reviewer chooses a decision below."
            : "No input needed before starting."}
        </p>
      )}
      {inputs.map((input, index) =>
        !kindOf(input) ? (
          <div key={index} className="rounded-xl border border-hairline p-3">
            <p className="text-sm font-medium">{input.label || input.name}</p>
            <p role="status" className="mt-1 text-xs text-muted-foreground">
              This field uses an unsupported type or widget ({input.type || "string"} /{" "}
              {input.widget || "default"}). Its definition is preserved; edit it in Code.
            </p>
          </div>
        ) : (
          <div key={index} className="rounded-xl border border-hairline p-3">
            <button
              type="button"
              className="flex w-full items-center justify-between gap-3 text-left"
              aria-expanded={editing === index}
              onClick={() => setEditing(editing === index ? null : index)}
            >
              <span className="min-w-0 break-words font-medium">
                {input.label || input.name || "Untitled question"}
                <span className="ml-2 text-xs font-normal text-muted-foreground">
                  {KINDS.find((k) => k[0] === kindOf(input))?.[1]}
                  {input.required ? " · Required" : ""}
                </span>
              </span>
              <span className="text-xs text-primary">{editing === index ? "Close" : "Edit"}</span>
            </button>
            {editing === index && (
              <div className="mt-4 space-y-4">
                <label className="block space-y-1 text-sm">
                  <span>Question</span>
                  <Input
                    value={input.label ?? ""}
                    onChange={(e) => update(index, { label: e.target.value })}
                    placeholder="What period should this cover?"
                  />
                </label>
                <div className="grid gap-4 sm:grid-cols-2">
                  <label className="block space-y-1 text-sm">
                    <span>Answer type</span>
                    <select
                      className="w-full rounded-xl border bg-card p-2"
                      value={kindOf(input)}
                      onChange={(e) => {
                        const kind = KINDS.find((k) => k[0] === e.target.value)!
                        update(index, {
                          type: kind[2],
                          widget: kind[3],
                          options:
                            kind[3] === "select" || kind[3] === "multiselect"
                              ? ["Option 1", "Option 2"]
                              : undefined,
                          allow_custom: undefined,
                          default: undefined,
                        })
                      }}
                    >
                      {KINDS.map((k) => (
                        <option key={k[0]} value={k[0]}>
                          {k[1]}
                        </option>
                      ))}
                    </select>
                  </label>
                  <label className="block space-y-1 text-sm">
                    <span>Variable name</span>
                    <Input
                      value={input.name}
                      onChange={(e) => update(index, { name: e.target.value })}
                    />
                  </label>
                </div>
                <p className="break-words text-xs text-muted-foreground">
                  {context === "decision" ? (
                    "The answer is stored in this decision step’s output. Its variable name identifies it for later steps."
                  ) : (
                    <>
                      Use <code>{`{{ inputs.${input.name || "name"} }}`}</code> in step
                      instructions. Answers are values, not executable expressions.
                    </>
                  )}
                </p>
                <label className="block space-y-1 text-sm">
                  <span>Help text</span>
                  <Input
                    value={input.description ?? ""}
                    onChange={(e) => update(index, { description: e.target.value })}
                    placeholder="Explain what a useful answer looks like"
                  />
                </label>
                {(input.widget === "select" ||
                  input.widget === "multiselect" ||
                  !!input.options?.length) && (
                  <label className="block space-y-1 text-sm">
                    <span>Prepared answers — one per line</span>
                    <textarea
                      className="min-h-28 w-full rounded-xl border bg-card p-3"
                      value={(input.options ?? []).join("\n")}
                      onChange={(e) => update(index, { options: e.target.value.split("\n") })}
                    />
                  </label>
                )}
                {(input.widget === "select" || kindOf(input) === "select") && (
                  <label className="flex items-center gap-2 text-sm">
                    <input
                      type="checkbox"
                      checked={input.allow_custom ?? false}
                      onChange={(e) => update(index, { allow_custom: e.target.checked })}
                    />
                    Allow a custom answer
                  </label>
                )}
                {input.options?.length ? (
                  <FormField
                    field={{
                      ...slashFieldsFromRoutineInputs([input])[0],
                      label: "Default answer (optional)",
                      required: false,
                    }}
                    value={formatInputDefault(input.default)}
                    onChange={(e) => {
                      let value: unknown = e.target.value
                      try {
                        value = coerceRoutineInput(input.type, e.target.value, input.name)
                      } catch {
                        /* Static validation reports invalid values. */
                      }
                      update(index, { default: value })
                    }}
                    idPrefix="recipe-default-"
                  />
                ) : (
                  <label className="block space-y-1 text-sm">
                    <span>Default answer (optional)</span>
                    {input.type === "boolean" ? (
                      <select
                        className="w-full rounded-xl border bg-card p-2"
                        value={formatInputDefault(input.default)}
                        onChange={(e) =>
                          update(index, {
                            default: e.target.value === "" ? undefined : e.target.value === "true",
                          })
                        }
                      >
                        <option value="">No default</option>
                        <option value="true">Yes</option>
                        <option value="false">No</option>
                      </select>
                    ) : (
                      <Input
                        value={formatInputDefault(input.default)}
                        placeholder={
                          input.type === "array" ? '["Option 1"]' : "Leave empty to ask each time"
                        }
                        onChange={(e) => {
                          const raw = e.target.value
                          let value: unknown = raw
                          try {
                            value =
                              raw === ""
                                ? undefined
                                : coerceRoutineInput(input.type, raw, input.name)
                          } catch {
                            /* Keep invalid text so server validation can explain it. */
                          }
                          update(index, { default: value })
                        }}
                      />
                    )}
                  </label>
                )}
                {input.default !== undefined && (
                  <button
                    type="button"
                    className="text-xs text-primary"
                    onClick={() => update(index, { default: undefined })}
                  >
                    Clear default answer
                  </button>
                )}
                {(input.type === "integer" || input.type === "number") && (
                  <div className="grid grid-cols-2 gap-3">
                    {(["min", "max"] as const).map((bound) => (
                      <label key={bound} className="space-y-1 text-sm">
                        <span>{bound === "min" ? "Minimum" : "Maximum"}</span>
                        <Input
                          type="number"
                          value={input[bound] ?? ""}
                          onChange={(e) =>
                            update(index, {
                              [bound]: e.target.value === "" ? undefined : Number(e.target.value),
                            })
                          }
                        />
                      </label>
                    ))}
                  </div>
                )}
                <label className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={input.required ?? false}
                    onChange={(e) => update(index, { required: e.target.checked })}
                  />
                  An answer is required
                </label>
                <button
                  type="button"
                  className="text-sm text-destructive"
                  onClick={() => {
                    onChange(inputs.filter((_, i) => i !== index))
                    setEditing(null)
                  }}
                >
                  Remove question
                </button>
              </div>
            )}
          </div>
        ),
      )}
      <button type="button" onClick={add} className="rounded-full border px-4 py-2 text-sm">
        + Add question
      </button>
      {!!inputs.length && context === "start" && (
        <details className="rounded-xl bg-muted/30 p-4">
          <summary className="cursor-pointer text-sm font-medium">Preview the start form</summary>
          <div className="mt-4">
            <RoutineInputFormPreview key={JSON.stringify(inputs)} inputs={inputs} />
          </div>
        </details>
      )}
    </section>
  )
}

function RoutineInputFormPreview({ inputs }: { inputs: RoutineInputSpec[] }) {
  const fields = slashFieldsFromRoutineInputs(inputs)
  const [values, setValues] = useState<Record<string, string>>({})
  return (
    <div className="space-y-4">
      {fields.map((field, index) => (
        <FormField
          key={index}
          field={field}
          value={values[field.name] ?? field.default ?? ""}
          onChange={(e) => setValues({ ...values, [field.name]: e.target.value })}
          idPrefix="recipe-preview-"
        />
      ))}
      <p className="text-xs text-muted-foreground">
        Preview only. Answers entered here are not saved or submitted.
      </p>
    </div>
  )
}
