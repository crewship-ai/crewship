"use client"

import { useEffect, useRef, useState } from "react"

import { Button } from "@/components/ui/button"
import { PagePreviewFrame } from "@/components/features/pages/page-preview"
import { usePagePreview } from "@/hooks/use-page-preview"
import type { WirePageDetail } from "@/hooks/use-page-grants"
import type { WirePage } from "@/hooks/use-pages"

/**
 * The candidate's build, as a workspace of its own.
 *
 * It is a separate pane rather than a third column because a form, a diff and
 * a live iframe do not fit side by side at any width anybody actually uses —
 * and because the preview is a *detour* from a decision. Leaving it is
 * therefore not free: the caller clears the review consent (`onReturn`), since
 * the person is coming back to make the decision, not carrying one across.
 *
 * `onRequest` is deliberately not passed. A draft preview must not execute the
 * application's actions; the runtime refuses an unhandled request already
 * (`page-preview.tsx:121`, V10) and omitting the handler is what makes that
 * refusal the only possible outcome rather than a policy someone can flip.
 */
export function ReviewPreview({
  workspaceId,
  slug,
  page,
  candidateRevision,
  onReturn,
}: {
  workspaceId: string
  slug: string
  page: WirePageDetail | null
  candidateRevision: number | null
  onReturn: () => void
}) {
  const { query, build } = usePagePreview(workspaceId, slug)
  const heading = useRef<HTMLHeadingElement>(null)
  const [stopped, setStopped] = useState(false)
  // Entering the preview is a navigation. Park focus on its heading so a
  // keyboard user is not left on a control that no longer exists.
  useEffect(() => heading.current?.focus(), [])

  const data = query.data
  const job = data?.build
  const running = build.isPending || job?.state === "running"
  const stale = typeof candidateRevision === "number" && typeof job?.source_revision === "number" && job.source_revision !== candidateRevision
  const live = !!data?.artifact && !!data.runtime_url && job?.state === "ready" && !!page && !query.isError && !stopped

  return (
    <section className="flex min-h-0 w-full min-w-0 flex-col gap-4" aria-labelledby="review-preview-heading">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 id="review-preview-heading" ref={heading} tabIndex={-1} className="text-lg font-semibold outline-none">
            Candidate preview
          </h2>
          {/* The label names the revision that is actually RUNNING, not the one
              under review. When a stale build is mounted those are different,
              and a header reading "Draft 7" over an artifact built from draft 5
              invites the reviewer to treat what they see as the candidate. */}
          <p className="text-sm text-muted-foreground">
            {stale
              ? `Showing draft ${job!.source_revision} — not the draft ${candidateRevision} under review`
              : candidateRevision === null
                ? "Candidate build"
                : `Draft ${candidateRevision}`}{" "}
            · actions are not executed here
          </p>
        </div>
        <Button variant="outline" className="min-h-11" onClick={onReturn}>
          Back to review
        </Button>
      </div>

      <div role="note" className="rounded-md border border-dashed p-3 text-sm">
        <p>This preview does not run the application&apos;s actions. No action handler is connected to it, and the runtime refuses a request it cannot answer.</p>
        <p className="mt-2 text-muted-foreground">
          It renders with the live data you are allowed to read. A new or changed panel may legitimately show an empty state here. This is not a test of the actions and not a
          measurement of what a customer will see.
        </p>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <Button
          className="min-h-11"
          // Not disabled by `query.isError`, because that wedges the one
          // control that can clear the state — and `retry: false` means a
          // single network blip is enough to set it.
          //
          // The endpoint's own 404 is narrower than it looks: it fires only
          // when there is no draft at all (`pages_build.go:187`), and a draft
          // with no build answers 200 with `build: null` (`:212`). So the
          // first build was never blocked by a 404. What does block it is a
          // 503 (`builds == nil` at `:182`, or a runtime origin that does not
          // isolate the Studio host at `:207`) — and there, building fails
          // again with the same message, loudly, which is better than an
          // inert grey button with no account of itself.
          //
          // `candidateRevision` comes from the review snapshot, not from this
          // endpoint, so it is known even when the preview read failed. With
          // no revision from either source there is genuinely nothing to
          // build, and only then is the control dead.
          disabled={running || (data == null && candidateRevision == null)}
          onClick={() => {
            setStopped(false)
            build.mutate(data?.revision ?? candidateRevision!)
          }}
        >
          {running ? "Building…" : "Build preview"}
        </Button>
        {/* Stop is a host control outside the iframe: there is no in-frame
            stop message, and a preview that can only be stopped from inside
            itself cannot be stopped at all. Unmounting the frame is the stop. */}
        {live && (
          <Button variant="outline" className="min-h-11" onClick={() => setStopped(true)}>
            Stop preview
          </Button>
        )}
        {stale && <span className="text-sm text-muted-foreground">This build is of an older draft. Build again to preview the candidate under review.</span>}
      </div>

      {(query.error || build.error) && (
        <p role="alert" className="text-sm text-destructive">
          {query.error?.message ?? build.error?.message}
        </p>
      )}
      {data && !data.runtime_url && (
        <p role="alert" className="text-sm text-muted-foreground">
          An administrator needs to configure the application preview domain before a candidate can be previewed.
        </p>
      )}

      <div className="min-h-[24rem] min-w-0 flex-1">
        {live ? (
          <PagePreviewFrame
            workspaceId={workspaceId}
            key={`${workspaceId}:${slug}:${job!.id}`}
            artifact={data!.artifact!}
            page={page as WirePage}
            runtimeURL={data!.runtime_url}
            developmentSameOrigin={data!.development_same_origin === true}
            title="Candidate application preview"
          />
        ) : (
          <div className="flex h-full min-h-[24rem] items-center justify-center rounded-md border border-dashed p-4 text-center text-sm text-muted-foreground">
            {/* Every reason the frame is not mounted, named. A placeholder that
                says "build this" while the real reason is a revoked permission
                or a Page whose data vanished teaches the reviewer to click
                Build at the one moment nothing should be running. */}
            {running
              ? "Preparing the candidate's preview…"
              : stopped
                ? "Preview stopped."
                : query.isError
                  ? "The preview could not be loaded, so nothing is running here."
                  : !page
                    ? "This Page's data is not available, so nothing is running here."
                    : job?.state === "failed"
                      ? "This candidate did not build. Its preview cannot be shown."
                      : job?.state === "interrupted"
                        ? "This build stopped before it finished, so there is nothing to preview. Build the candidate again."
                        : "Build this candidate to preview it."}
          </div>
        )}
      </div>
    </section>
  )
}
