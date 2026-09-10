"use client"

import { useState } from "react"
import { CheckCircle2, FlaskConical, GitCompareArrows, Play, ShieldCheck } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs"
import { RoutineFixtureTest } from "./routine-fixture-test"
import { RoutineComparison } from "./routine-comparison"

export function RoutineTestWorkspace({
  workspaceId,
  slug,
  definition,
  busy,
  result,
  onValidate,
  onPublish,
  onOpenCode,
  parseError,
  published,
}: {
  published: boolean
  workspaceId: string
  slug: string
  definition: Record<string, unknown> | null
  busy: boolean
  result: { passed: boolean; details: string } | null
  onValidate: () => void
  onPublish: () => void
  onOpenCode: () => void
  parseError: string | null
}) {
  const [mode, setMode] = useState("definition")
  const [comparing, setComparing] = useState(false)
  return (
    <section className="space-y-5" aria-label="Recipe testing">
      <div className="flex items-start gap-3">
        <span className="flex size-11 shrink-0 items-center justify-center rounded-2xl bg-info/10 text-info">
          <FlaskConical className="size-5" />
        </span>
        <div>
          <h3 className="text-lg font-medium">Build confidence before you run</h3>
          <p className="mt-1 text-sm leading-relaxed text-muted-foreground">
            Check your recipe, try a step with sample data, or compare published versions on real
            work.
          </p>
        </div>
      </div>
      {comparing && (
        <div
          role="status"
          className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-warn/30 bg-warn/5 p-3 text-sm"
        >
          <span>Live comparison is running.</span>
          <Button type="button" variant="ghost" size="sm" onClick={() => setMode("compare")}>
            View progress
          </Button>
        </div>
      )}
      <Tabs value={mode} onValueChange={setMode}>
        <TabsList
          aria-label="Test method"
          className="grid h-auto! w-full! grid-cols-1 gap-1 sm:grid-cols-3"
        >
          <TabsTrigger value="definition" className="min-h-11">
            <ShieldCheck />
            Check definition
          </TabsTrigger>
          <TabsTrigger value="fixtures" className="min-h-11">
            <FlaskConical />
            Test a step
          </TabsTrigger>
          <TabsTrigger value="compare" className="min-h-11">
            <GitCompareArrows />
            Compare versions
          </TabsTrigger>
        </TabsList>
        <TabsContent
          value="definition"
          hidden={mode !== "definition"}
          forceMount
          className="space-y-4 data-[state=inactive]:hidden"
        >
          <div className="rounded-2xl border border-hairline bg-card p-5">
            <div className="flex items-center justify-between gap-3">
              <h4 className="font-medium">Definition check</h4>
              <span className="rounded-full bg-success/10 px-2.5 py-1 text-xs text-success">
                No external actions
              </span>
            </div>
            <p className="mt-3 text-sm leading-relaxed text-muted-foreground">
              Find invalid inputs, missing references and configuration problems. This check does
              not run agents or prove that a real result will succeed.
            </p>
            <Button
              type="button"
              className="mt-5"
              disabled={busy || !definition}
              onClick={onValidate}
            >
              {busy ? "Validating…" : "Validate recipe"}
            </Button>
            {result && (
              <div role="status" className="mt-4 rounded-xl bg-muted/40 p-3">
                <p className={result.passed ? "text-sm text-success" : "text-sm text-destructive"}>
                  {result.passed ? "Definition validation passed" : "Definition needs attention"}
                </p>
                <pre className="mt-2 max-h-40 overflow-auto whitespace-pre-wrap break-words text-xs text-muted-foreground">
                  {result.details}
                </pre>
              </div>
            )}
          </div>
          <div className="flex items-start gap-3 rounded-2xl border border-hairline p-4">
            <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
            <p className="text-sm text-muted-foreground">
              You can publish after validation. Running paid or external work is not required to
              save your recipe.
            </p>
          </div>
        </TabsContent>
        <TabsContent
          value="fixtures"
          hidden={mode !== "fixtures"}
          forceMount
          className="data-[state=inactive]:hidden"
        >
          <RoutineFixtureTest workspaceId={workspaceId} definition={definition} />
        </TabsContent>
        <TabsContent
          value="compare"
          hidden={mode !== "compare"}
          forceMount
          className="data-[state=inactive]:hidden"
        >
          {published ? (
            <RoutineComparison
              key={`${workspaceId}:${slug}`}
              workspaceId={workspaceId}
              slug={slug}
              onBusyChange={setComparing}
            />
          ) : (
            <div className="space-y-3 rounded-2xl border border-hairline p-5">
              <h4 className="font-medium">Publish a version to compare real runs</h4>
              <p className="text-sm text-muted-foreground">
                Comparisons use archived recipe versions. You can check the definition and test with
                sample data while this recipe is still a draft.
              </p>
              <Button type="button" variant="outline" onClick={onPublish}>
                Review first publication
              </Button>
            </div>
          )}
        </TabsContent>
      </Tabs>
      {parseError && (
        <Button
          type="button"
          variant="outline"
          className="h-auto max-w-full whitespace-normal break-all text-left text-destructive"
          onClick={onOpenCode}
        >
          {parseError} · Open Code
        </Button>
      )}
      <div className="flex flex-wrap items-center justify-between gap-4 rounded-2xl border border-hairline bg-muted/20 p-4">
        <div className="flex items-start gap-3">
          <Play className="mt-0.5 size-4 shrink-0 text-primary" />
          <div>
            <p className="text-sm font-medium">Ready for a real run?</p>
            <p className="mt-1 text-xs text-muted-foreground">
              Publish your recipe, then use Run to review inputs and start work.
            </p>
          </div>
        </div>
        <Button type="button" variant="outline" disabled={busy} onClick={onPublish}>
          Review publication
        </Button>
      </div>
    </section>
  )
}
