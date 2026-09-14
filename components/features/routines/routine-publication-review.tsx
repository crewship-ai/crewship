"use client"

import { ArrowRight, GitBranch, ListChecks, ShieldCheck } from "lucide-react"
import { routinePublicationChanges } from "@/lib/routine-publication-changes"

export function RoutinePublicationReview({
  draft,
  published,
  existing,
  name,
  validated,
}: {
  draft: Record<string, unknown> | null
  published?: Record<string, unknown> | null
  existing: boolean
  name: string
  validated: boolean
}) {
  if (!draft)
    return (
      <p role="alert" className="text-sm text-destructive">
        Fix the recipe in Code before reviewing publication.
      </p>
    )
  const changes =
    published || !existing ? routinePublicationChanges(published ?? {}, draft) : null
  const count = (key: string) => (Array.isArray(draft[key]) ? draft[key].length : 0)
  return (
    <section className="space-y-5" aria-label="Publication review">
      <div className="flex items-start gap-3">
        <span className="flex size-11 shrink-0 items-center justify-center rounded-2xl bg-primary/10 text-primary">
          <GitBranch className="size-5" />
        </span>
        <div className="min-w-0 flex-1">
          <h3 className="text-lg font-medium">
            {existing ? "Review the next version" : "Your recipe is ready to review"}
          </h3>
          <p className="mt-1 break-words text-sm text-muted-foreground">
            {name || "Untitled recipe"} ·{" "}
            {existing
              ? "Changes apply when you publish"
              : "Publishing makes this recipe available to run"}
          </p>
        </div>
      </div>
      <div className="grid grid-cols-3 divide-x divide-hairline overflow-hidden rounded-2xl border border-hairline bg-card">
        {[
          ["Steps", count("steps")],
          ["Inputs", count("inputs")],
          ["Results", count("outputs")],
        ].map(([label, value]) => (
          <div key={label} className="min-w-0 p-3 sm:p-4">
            <p className="text-2xl font-medium tabular-nums">{value}</p>
            <p className="mt-1 text-xs text-muted-foreground">{label}</p>
          </div>
        ))}
      </div>
      <div className="space-y-4 rounded-2xl border border-hairline p-4 sm:p-5">
        <h4 className="flex items-center gap-2 text-sm font-medium">
          <ListChecks className="size-4 text-primary" />
          Publication changes
        </h4>
        {!changes && (
          <p className="text-sm text-muted-foreground">
            The published recipe is unavailable here. Open the published recipe to compare it;
            this review cannot confirm which fields changed.
          </p>
        )}
        {changes?.groups.map((group) => (
          <div key={group.label} className="space-y-2 border-t border-hairline pt-3">
            <p className="text-sm font-medium">{group.label}</p>
            {!group.readable ? (
              <p className="text-xs text-muted-foreground">
                This structure needs review in Code.
              </p>
            ) : (
              <>
                {(
                  [
                    ["Added", group.added, "text-success"],
                    ["Changed", group.changed, "text-info"],
                    ["Removed", group.removed, "text-destructive"],
                  ] as const
                )
                  .filter(([, rows]) => rows.length)
                  .map(([label, rows, tone]) => (
                    <div key={label} className="flex items-start gap-3 text-xs">
                      <span className={`w-24 shrink-0 whitespace-nowrap font-medium ${tone}`}>
                        {label} · {rows.length}
                      </span>
                      <span className="min-w-0 break-words text-muted-foreground">
                        {rows.join(", ")}
                      </span>
                    </div>
                  ))}
                {group.reordered && <p className="text-xs text-info">Order changed</p>}
                {!group.reordered &&
                  !group.added.length &&
                  !group.changed.length &&
                  !group.removed.length && (
                    <p className="text-xs text-muted-foreground">Unchanged</p>
                  )}
              </>
            )}
          </div>
        ))}
        {!!changes?.settings.length && (
          <p className="border-t border-hairline pt-3 text-xs text-muted-foreground">
            Other settings changed:{" "}
            {changes.settings.map((key) => key.replaceAll("_", " ")).join(", ")}.
          </p>
        )}
      </div>
      <div className="space-y-3 rounded-2xl bg-muted/30 p-4">
        <p className="flex items-center gap-2 text-sm font-medium">
          <ArrowRight className="size-4" />
          What happens after publication
        </p>
        <p className="text-sm leading-relaxed text-muted-foreground">
          {existing
            ? "Future starts use the published version. Already accepted runs and pinned plans keep their version. Repeating plans that follow the live recipe are checked for compatible inputs."
            : "The recipe becomes available for real runs. Any schedule selected for this new recipe is activated."}
        </p>
        <p className="flex items-start gap-2 text-xs text-muted-foreground">
          <ShieldCheck className="size-4 shrink-0" />
          {validated
            ? "Test passed. Publication checks the current draft again before updating the live recipe."
            : "Publication validates this draft without running agents or external actions."}
        </p>
      </div>
      <details className="rounded-xl border border-hairline p-3">
        <summary className="cursor-pointer text-xs text-muted-foreground">
          Technical details
        </summary>
        <div className="mt-3 grid gap-3 sm:grid-cols-2">
          {[
            [
              "Published recipe",
              published
                ? JSON.stringify(published, null, 2)
                : existing
                  ? "Unavailable"
                  : "New recipe",
            ],
            ["Draft", JSON.stringify(draft, null, 2)],
          ].map(([label, text]) => (
            <div key={label} className="min-w-0">
              <p className="mb-2 text-xs font-medium">{label}</p>
              <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all text-xs text-muted-foreground">
                {text}
              </pre>
            </div>
          ))}
        </div>
      </details>
    </section>
  )
}
