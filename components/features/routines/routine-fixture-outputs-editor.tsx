"use client"

import { useId, useState } from "react"
import { Button } from "@/components/ui/button"

export function RoutineFixtureOutputsEditor({
  value,
  onChange,
  step,
  steps,
  disabled,
}: {
  value: string
  onChange: (value: string) => void
  step: Record<string, unknown>
  steps: Record<string, unknown>[]
  disabled: boolean
}) {
  const prefix = useId()
  const [added, setAdded] = useState<string | null>(null)
  let outputs: Record<string, unknown> | null = null
  try {
    const parsed = JSON.parse(value)
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) outputs = parsed
  } catch {
    /* Keep malformed data editable in Advanced. */
  }
  const references = [
    ...new Set(
      [...JSON.stringify(step).matchAll(/steps\.([a-zA-Z0-9_-]+)\.output/g)].map(
        (match) => match[1],
      ),
    ),
  ]
  const keys = [...new Set([...references, ...Object.keys(outputs ?? {})])]
  const update = (key: string, text: string | undefined) => {
    if (!outputs) return
    const next = { ...outputs }
    if (text === undefined) delete next[key]
    else next[key] = text
    onChange(JSON.stringify(next, null, 2))
  }
  return (
    <section className="space-y-3" aria-label="Data from earlier steps">
      <div>
        <h4 className="text-sm font-medium">Data from earlier steps</h4>
        <p className="mt-1 text-xs text-muted-foreground">
          Provide a sample or import a previous run. A missing result stays missing until you add
          it.
        </p>
      </div>
      {outputs && !keys.length && (
        <p className="rounded-xl bg-muted/30 p-3 text-xs text-muted-foreground">
          This step does not reference earlier results.
        </p>
      )}
      {outputs &&
        keys.map((key, index) => {
          const present = Object.hasOwn(outputs, key)
          const source = steps.find((row) => row.id === key)
          const text =
            typeof outputs[key] === "string"
              ? (outputs[key] as string)
              : JSON.stringify(outputs[key], null, 2)
          return (
            <div key={key} className="space-y-2 rounded-xl border border-hairline p-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <label
                  htmlFor={`${prefix}-${index}`}
                  className="min-w-0 max-w-full break-words text-sm font-medium"
                >
                  {String(source?.name || key)}
                </label>
                <span className="text-xs text-muted-foreground">
                  {present ? "Sample provided" : "Not provided"}
                </span>
              </div>
              {present ? (
                <>
                  <textarea
                    id={`${prefix}-${index}`}
                    value={text}
                    autoFocus={added === key}
                    disabled={disabled}
                    onChange={(event) => update(key, event.target.value)}
                    rows={3}
                    className="w-full rounded-md border bg-card p-3 text-sm"
                  />
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <p className="text-xs text-muted-foreground">
                      Text or JSON output. An empty sample is an explicit empty result.
                    </p>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      disabled={disabled}
                      onClick={() => update(key, undefined)}
                    >
                      Remove sample
                    </Button>
                  </div>
                </>
              ) : (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="h-auto max-w-full whitespace-normal break-all text-left"
                  disabled={disabled}
                  onClick={() => {
                    setAdded(key)
                    update(key, "")
                  }}
                >
                  Add sample for {String(source?.name || key)}
                </Button>
              )}
            </div>
          )
        })}
      <details open={!outputs} className="rounded-xl border border-hairline p-3">
        <summary className="cursor-pointer text-xs text-muted-foreground">
          Advanced · all captured outputs
        </summary>
        <div className="mt-3">
          <label htmlFor={`${prefix}-raw`} className="text-sm font-medium">
            Captured upstream outputs · JSON
          </label>
          <textarea
            id={`${prefix}-raw`}
            value={value}
            disabled={disabled}
            onChange={(event) => onChange(event.target.value)}
            rows={4}
            className="mt-2 w-full rounded-md border bg-card p-3 font-mono text-xs"
          />
          {!outputs && (
            <p role="alert" className="text-xs text-destructive">
              Enter a JSON object keyed by step ID to use the sample editor.
            </p>
          )}
        </div>
      </details>
    </section>
  )
}
