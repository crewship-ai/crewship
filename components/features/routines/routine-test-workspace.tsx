"use client"

import { Button } from "@/components/ui/button"
import { RoutineFixtureTest } from "./routine-fixture-test"

export interface RoutineTestWorkspaceProps {
  workspaceId: string
  definition: Record<string, unknown> | null
  busy: boolean
  result: { passed: boolean; details: string } | null
  onValidate: () => void
  onOpenCode: () => void
  parseError: string | null
  stepId: string
}

export function RoutineTestWorkspace({
  workspaceId,
  definition,
  busy,
  result,
  onValidate,
  onOpenCode,
  parseError,
  stepId,
}: RoutineTestWorkspaceProps) {
  return (
    <section className="space-y-5" aria-label="Test">
      <div className="space-y-2 border-b border-border pb-4">
        <h4 className="text-sm font-medium">Test routine</h4>
        <p className="text-xs text-muted-foreground">
          Checks the recipe, inputs and references. It does not run agents, scripts or HTTP
          calls, and does not prove a real run will succeed.
        </p>
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={busy || !definition}
          onClick={onValidate}
        >
          {busy ? "Testing…" : "Test routine"}
        </Button>
        {result && (
          <div role="status" className="text-xs">
            <p className={result.passed ? "text-success" : "text-destructive"}>
              {result.passed ? "Test passed" : "Test needs attention"}
            </p>
            <pre className="mt-2 max-h-40 overflow-auto whitespace-pre-wrap break-words">
              {result.details}
            </pre>
          </div>
        )}
      </div>
      <RoutineFixtureTest
        workspaceId={workspaceId}
        definition={definition}
        selectedStepId={stepId}
      />
      {parseError && (
        <Button
          type="button"
          variant="outline"
          className="h-auto whitespace-normal text-destructive"
          onClick={onOpenCode}
        >
          {parseError} · Open Code
        </Button>
      )}
    </section>
  )
}
