"use client"

import { describeStep } from "@/lib/routine-step-describe"

import { useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { routineInputSpecs } from "@/lib/routine-inputs"
import { RoutineFixtureOutputsEditor } from "./routine-fixture-outputs-editor"
import { RoutineFixtureImport } from "./routine-fixture-import"
import type { CapturedRoutineFixture } from "@/lib/routine-fixtures"
import { InputsForm } from "./routine-run-inputs-dialog"

interface FixtureResult {
  execution_mode: string
  step_id: string
  output_source: string
  output: string
  valid: boolean
  validation_declared: boolean
  validation_reason?: string
  definition_hash: string
  fixture_hash: string
  limitations: string[]
}

export function RoutineFixtureTest({
  workspaceId,
  definition,
  selectedStepId,
}: {
  workspaceId: string
  selectedStepId?: string
  definition: Record<string, unknown> | null
}) {
  const steps = Array.isArray(definition?.steps)
    ? definition.steps.filter(
        (step): step is Record<string, unknown> =>
          !!step && typeof step === "object" && typeof step.id === "string",
      )
    : []
  const [localStepId, setStepId] = useState("")
  const stepId = selectedStepId ?? localStepId
  const selected = steps.find((step) => step.id === stepId)
  const [upstream, setUpstream] = useState("{}")
  const [captured, setCaptured] = useState<CapturedRoutineFixture | null>(null)
  const [captureRevision, setCaptureRevision] = useState(0)
  const [replacement, setReplacement] = useState("")
  const [replaceConfirmed, setReplaceConfirmed] = useState(false)
  const [busy, setBusy] = useState(false)
  const inFlight = useRef(false)
  const scope = useRef(0)
  const [error, setError] = useState<string | null>(null)
  const [last, setLast] = useState<{ definition: string; result: FixtureResult } | null>(null)
  useEffect(() => {
    scope.current++
    inFlight.current = false
    setBusy(false)
    setError(null)
    setReplacement("")
    setReplaceConfirmed(false)
    setLast(null)
  }, [selectedStepId])
  useEffect(() => {
    const epochRef = scope
    epochRef.current++
    inFlight.current = false
    setBusy(false)
    setCaptured(null)
    setUpstream("{}")
    setStepId("")
    setReplacement("")
    setReplaceConfirmed(false)
    setLast(null)
    setError(null)
    return () => {
      epochRef.current++
    }
  }, [workspaceId])
  const needsFixture =
    selected && ["agent_run", "http", "script"].includes(String(selected.type))
  const supported = selected?.type === "transform" || needsFixture
  const run = async (inputs: Record<string, unknown>) => {
    if (inFlight.current || !definition || !supported || (needsFixture && !replaceConfirmed))
      return
    inFlight.current = true
    setBusy(true)
    setError(null)
    const epoch = scope.current
    try {
      const parsed = JSON.parse(upstream)
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed))
        throw new Error("Upstream outputs must be an object keyed by step ID.")
      const stepOutputs = Object.fromEntries(
        Object.entries(parsed).map(([id, value]) => [
          id,
          typeof value === "string" ? value : JSON.stringify(value),
        ]),
      )
      const snapshot = JSON.stringify(definition)
      const response = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/fixture_test`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            definition,
            step_id: stepId,
            inputs,
            step_outputs: stepOutputs,
            ...(needsFixture ? { fixture_output: replacement } : {}),
          }),
        },
      )
      const result = await response.json()
      if (scope.current !== epoch) return
      if (!response.ok) throw new Error(result.error || "Fixture test failed")
      if (result.execution_mode !== "fixtures" || typeof result.valid !== "boolean")
        throw new Error("The server did not confirm a test with sample data.")
      setLast({ definition: snapshot, result })
    } catch (e) {
      if (scope.current === epoch) setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (scope.current === epoch) {
        inFlight.current = false
        setBusy(false)
      }
    }
  }
  return (
    <section className="space-y-4 rounded-2xl border border-hairline p-5">
      <div>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h3 className="font-medium">Test this step</h3>
          <span className="rounded-full bg-success/10 px-2.5 py-1 text-xs text-success">
            Sample data · no external actions
          </span>
        </div>
        <p className="mt-2 text-sm text-muted-foreground">
          Test one step using supplied data. Transforms compute their result; agent, HTTP and
          script steps only validate your replacement output. No external work runs.
        </p>
      </div>
      <RoutineFixtureImport
        workspaceId={workspaceId}
        disabled={busy}
        onImport={(fixture) => {
          setCaptured(fixture)
          setCaptureRevision((v) => v + 1)
          setUpstream(JSON.stringify(fixture.step_outputs, null, 2))
          setReplaceConfirmed(false)
        }}
      />
      {captured && (
        <details className="break-all text-xs text-muted-foreground">
          <summary className="cursor-pointer">Technical details · sample origin</summary>
          <p>
            Source run {captured.source.run_id} · {captured.source.status} · recipe{" "}
            {captured.source.definition_hash}. Only recorded outputs were copied; missing steps
            remain missing. Inputs below use captured values for matching fields.
          </p>
        </details>
      )}
      <div hidden={selectedStepId !== undefined}>
        <label htmlFor="fixture-step" className="text-sm font-medium">
          Step to test
        </label>
        <select
          id="fixture-step"
          value={stepId}
          disabled={busy}
          onChange={(e) => {
            setStepId(e.target.value)
            setReplaceConfirmed(false)
          }}
          className="mt-1 w-full rounded-md border bg-card p-2 text-sm"
        >
          <option value="">Choose a step…</option>
          {steps.map((step) => (
            <option key={String(step.id)} value={String(step.id)}>
              {describeStep(step, 1).title}
            </option>
          ))}
        </select>
      </div>
      {selected && !supported && (
        <p role="status" className="text-sm text-muted-foreground">
          Tests with sample data do not support this step type. No work will be executed.
        </p>
      )}
      {supported && (
        <>
          {selected && (
            <RoutineFixtureOutputsEditor
              value={upstream}
              onChange={setUpstream}
              step={selected}
              steps={steps}
              disabled={busy}
            />
          )}
          {needsFixture && (
            <div className="space-y-2">
              {captured && Object.hasOwn(captured.step_outputs, stepId) && (
                <button
                  type="button"
                  disabled={busy}
                  className="text-sm underline"
                  onClick={() => {
                    setReplacement(captured.step_outputs[stepId])
                    setReplaceConfirmed(true)
                  }}
                >
                  Use captured output for this step
                </button>
              )}
              <label className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={replaceConfirmed}
                  disabled={busy}
                  onChange={(e) => setReplaceConfirmed(e.target.checked)}
                />
                Replace this step with the output below
              </label>
              <textarea
                aria-label="Replacement output"
                value={replacement}
                disabled={busy}
                onChange={(e) => setReplacement(e.target.value)}
                rows={4}
                className="w-full rounded-md border bg-card p-2 font-mono text-xs"
              />
              <p className="text-xs text-muted-foreground">
                An empty value is an explicit empty sample. This does not test what the real
                service or model returns.
              </p>
            </div>
          )}
          <fieldset disabled={busy || !!(needsFixture && !replaceConfirmed)}>
            <InputsForm
              key={`${stepId}:${captureRevision}:${JSON.stringify(routineInputSpecs(definition))}`}
              inputs={routineInputSpecs(definition).map((field) =>
                captured && Object.hasOwn(captured.inputs, field.name)
                  ? { ...field, default: captured.inputs[field.name] }
                  : field,
              )}
              submitting={busy}
              onRun={run}
              onCancel={selectedStepId === undefined ? () => setStepId("") : undefined}
              submitLabel="Test this step"
            />
          </fieldset>
        </>
      )}
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      {last && (
        <div className="space-y-2 rounded-md border p-3 text-sm">
          <h4 className="font-medium">
            Last step test ·{" "}
            {
              describeStep(
                steps.find((step) => step.id === last.result.step_id),
                1,
              ).title
            }
          </h4>
          <p>
            {last.result.valid
              ? last.result.validation_declared
                ? "Sample result passed its checks."
                : "No output checks are declared for this step."
              : `Sample result failed: ${last.result.validation_reason || "Result checks did not pass"}`}
          </p>
          {last.definition !== JSON.stringify(definition) && (
            <p className="text-warn">The recipe has changed since this test.</p>
          )}
          <p className="text-xs text-muted-foreground">
            Output source: {last.result.output_source}. Results describe the last submitted
            inputs.
          </p>
          <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-words text-xs">
            {last.result.output}
          </pre>
          <details className="text-xs text-muted-foreground">
            <summary>Technical details</summary>
            <p className="break-all">Recipe hash: {last.result.definition_hash}</p>
            <p className="break-all">Sample hash: {last.result.fixture_hash}</p>
            {last.result.limitations?.map((line) => (
              <p key={line}>{line}</p>
            ))}
          </details>
        </div>
      )}
    </section>
  )
}
