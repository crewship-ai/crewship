"use client"

/**
 * History — three kinds of history, and three genuinely different effects.
 *
 * This is the correctness rule the whole section is built around, and it is
 * the one the independent review called a lie in the previous design: a
 * single "Restore" spanning all three would be wrong in a way the reader
 * cannot detect from the screen.
 *
 *   · **Panel definitions** — `Restore panel version` writes the LIVE
 *     definition, immediately, for everyone. Panels whose shape changed lose
 *     their data and wait for the next push.
 *   · **Application source** — `Restore to draft` creates a NEW DRAFT. The
 *     live publication does not move at all.
 *   · **Application publications** — `Publish this version` makes a NEW LIVE
 *     PUBLICATION of a retained version. It is not rewinding the clock: the
 *     version counter goes forward, and the retained version is republished
 *     under a new number.
 *
 * So there are three confirm dialogs and no shared wording between them. A
 * shared dialog would be a shared sentence, and a shared sentence is how the
 * three effects become one in a reader's head.
 *
 * The two application sub-sections appear only when this Page HAS an
 * application. On an ordinary panel Page there is no "Application source"
 * heading with an empty state under it and no permanently disabled Publish —
 * a heading for a thing that does not exist is an invitation to look for it.
 *
 * And no list here is allowed to render as "nothing happened" when the truth
 * is "we could not read it". A refused or failed read says which, in the
 * server's words: an empty list is a claim about history, and this section
 * only makes that claim when the server actually made it.
 *
 * Padding and the readable measure belong to the shell, which supplies them
 * for all four sections. This one carries neither: a second `max-w-*` here
 * would nest two measures and leave this section narrower than its
 * neighbours, and its own padding would double the shell's.
 */

import * as React from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { SectionCard } from "@/components/ui/section-card"
import { Spinner } from "@/components/ui/spinner"
import { EmptyState } from "@/components/layout/empty-state"
import { AlertTriangle, FileCode2, History, Rocket } from "lucide-react"

import { apiFetch } from "@/lib/api-fetch"
import type { PagePublication } from "@/hooks/use-page-application"
import { usePageProjectHistory } from "@/hooks/use-page-project-history"
import { usePagePublications, type PublicationReceipt } from "@/hooks/use-page-publications"
import { PublishFenceError, publishConflictOf } from "@/hooks/use-page-review"
import type {
  FencedPublishRequest,
  PublishConflictWire,
  ReviewSnapshotWire,
} from "@/lib/pages/editor-contract"
import { publicationReceiptMessage } from "@/lib/pages/publication-receipt"
import { formatDateTime } from "@/lib/time"

import {
  CardAnswer,
  CardLabel,
  ControlRefusal,
  PanelVersionsCard,
  Refusal,
} from "@/components/features/pages/page-settings"

import type { EditorSectionProps } from "./section-props"

/**
 * The revision row, taken from the hook rather than re-declared. The hook
 * does not export its wire types, and a hand-copied interface here would be
 * a second declaration free to drift from the one the fetch actually returns.
 */
type SourceRevision = NonNullable<
  ReturnType<typeof usePageProjectHistory>["query"]["data"]
>["pages"][number]["revisions"][number]

function when(iso: string | null | undefined): string {
  return iso ? formatDateTime(iso) : "—"
}

function errorMessage(err: unknown, fallback: string): string {
  return err instanceof Error && err.message ? err.message : fallback
}

// ── Application source ──────────────────────────────────────────────────────

/**
 * Source revisions — the only one of the three restores that publishes
 * nothing.
 *
 * Lifted out of `page-project-history.tsx`, which put this list behind a
 * dialog opened from a toolbar. The restore is CAS-fenced on the revision
 * currently displayed as the draft head (`expected_revision`), exactly as it
 * was there: restoring against a head that moved under you would silently
 * discard whatever arrived in between.
 */
function SourceRevisionsCard({ workspaceId, slug }: { workspaceId: string; slug: string }) {
  const { query, restore } = usePageProjectHistory(workspaceId, slug)
  const [target, setTarget] = React.useState<SourceRevision | null>(null)

  const revisions = query.data?.pages.flatMap((page) => page.revisions) ?? []
  const current = revisions[0]?.revision
  const failed = query.isError

  return (
    <SectionCard
      data-slot="history-source"
      title={<CardLabel icon={FileCode2}>Application source</CardLabel>}
      actions={
        <CardAnswer>
          {failed
            ? "unavailable"
            : query.isPending
              ? "loading"
              : revisions.length === 0
                ? "no revisions"
                : `${revisions.length} retained`}
        </CardAnswer>
      }
      className="gap-4 py-4"
    >
      <div className="flex flex-col gap-2">
        <p className="type-page-meta text-muted-foreground">
          Restoring a source revision creates a <strong>new draft</strong>. The published
          application does not change, and nothing goes live until you publish it.
        </p>

        {failed && <Refusal>{errorMessage(query.error, "Could not load source history.")}</Refusal>}
        {restore.isError && (
          <Refusal>{errorMessage(restore.error, "Could not restore source revision.")}</Refusal>
        )}
        {restore.isSuccess && (
          <p role="status" className="type-page-meta text-muted-foreground">
            Draft restored. Build a preview to check it, then publish it if you want it live.
          </p>
        )}

        {query.isPending && !failed && (
          <div className="flex items-center gap-2 py-3 text-xs text-muted-foreground">
            <Spinner className="h-3.5 w-3.5" />
            Reading the source history…
          </div>
        )}

        {!query.isPending && !failed && revisions.length === 0 && (
          <EmptyState
            size="inline"
            icon={FileCode2}
            title="No source revisions yet"
            description="Every save of this Page's application project keeps a revision here."
          />
        )}

        {!failed && revisions.length > 0 && (
          <div data-slot="source-revisions">
            {revisions.map((revision) => {
              const isCurrent = revision.revision === current
              return (
                <div
                  key={revision.revision}
                  data-slot="source-revision"
                  data-revision={revision.revision}
                  className="flex items-center gap-2 border-b border-border/40 py-2 last:border-b-0"
                >
                  <span className="type-page-stamp w-12 shrink-0 text-muted-foreground-soft">
                    r{revision.revision}
                  </span>
                  <div className="flex min-w-0 flex-1 flex-col">
                    <span className="truncate text-xs text-foreground/85">
                      {when(revision.created_at)}
                    </span>
                    <span className="type-page-meta truncate text-muted-foreground-soft">
                      {/* A revision written before the project carried a commit
                          is still a revision; saying "legacy snapshot" is the
                          honest name for one whose source cannot be pinned. */}
                      {revision.git_commit ? revision.git_commit.slice(0, 10) : "legacy snapshot"}
                      {revision.restorable ? "" : " · not restorable"}
                    </span>
                  </div>
                  {isCurrent ? (
                    <Badge variant="outline" className="h-4 shrink-0 px-1.5 leading-none">
                      current draft
                    </Badge>
                  ) : (
                    <Button
                      size="sm"
                      variant="ghost"
                      className="type-page-meta h-6 shrink-0 px-2 text-muted-foreground hover:text-foreground"
                      disabled={!revision.restorable || !current || restore.isPending}
                      aria-label={`Restore revision ${revision.revision} to draft`}
                      onClick={() => setTarget(revision)}
                    >
                      Restore to draft
                    </Button>
                  )}
                </div>
              )
            })}
          </div>
        )}

        {query.hasNextPage && (
          <div>
            <Button
              size="sm"
              variant="outline"
              className="h-7 px-2.5 text-xs"
              disabled={query.isFetchingNextPage}
              onClick={() => void query.fetchNextPage()}
            >
              Load older revisions
            </Button>
          </div>
        )}
      </div>

      <AlertDialog open={target != null} onOpenChange={(open) => !open && setTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="text-sm">
              Restore revision {target?.revision} to draft
            </AlertDialogTitle>
            <AlertDialogDescription className="text-xs">
              This creates a new draft from revision {target?.revision}. The live publication does
              not change and nobody viewing this Page sees anything move. Build a preview to check
              the draft, then publish it if you want it live. Your current draft is replaced by
              this one, so save anything in it you still want.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel className="h-7 text-xs">Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="h-7 text-xs"
              disabled={restore.isPending}
              onClick={() => {
                if (!target || !current) return
                restore.mutate({ revision: target.revision, expectedRevision: current })
                setTarget(null)
              }}
            >
              {restore.isPending && <Spinner className="mr-1.5 h-3 w-3" />}
              Restore to draft
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SectionCard>
  )
}

// ── The publication fence ───────────────────────────────────────────────────

/**
 * What publishing THIS retained version would be fenced on.
 *
 * `usePageReview` asks the same endpoint about the *draft candidate* and has
 * no publication parameter; `?publication=N` is the rollback arm of the same
 * question. The answer carries the digests the server will compare inside the
 * publishing transaction, so the values sent with the publish are the values
 * the human was shown — a fence re-read at click time is a fence nobody
 * reviewed (V04), which is the entire reason the snapshot exists.
 */
function usePublicationReview(workspaceId: string, slug: string, version: number | undefined) {
  return useQuery<ReviewSnapshotWire, Error>({
    // Deliberately under `page-review`: an invalidation of that prefix after
    // `page.updated` must reach this read too, because a definition that
    // moved invalidates exactly this fence.
    queryKey: ["page-review", workspaceId, slug, "publication", version ?? null],
    enabled: version !== undefined,
    retry: false,
    gcTime: 0,
    queryFn: async ({ signal }) => {
      const params = new URLSearchParams({
        publication: String(version),
        workspace_id: workspaceId,
      })
      const response = await apiFetch(
        `/api/v1/pages/${encodeURIComponent(slug)}/project/review?${params}`,
        { signal },
      )
      if (!response.ok) {
        const body = (await response.json().catch(() => null)) as { error?: string } | null
        throw new Error(
          body?.error ?? "Could not read what publishing this version would be fenced on.",
        )
      }
      return (await response.json()) as ReviewSnapshotWire
    },
  })
}

/**
 * The 409 body, typed.
 *
 * `hooks/use-page-review.ts` has the same shaping and does not export it, and
 * `usePageApplication().publish` flattens a 409 into a plain `Error` — which
 * throws away the `conflict` kind and the routine names this screen has to
 * put on the page. So the request is made here, and it raises the SHARED
 * `PublishFenceError` so the shared `publishConflictOf` narrows it. If that
 * hook ever throws the typed error itself, this whole function and the
 * mutation under it should collapse into it.
 */
function conflictFromBody(body: unknown): PublishConflictWire {
  const raw = (body ?? {}) as Partial<PublishConflictWire>
  const kinds = ["definition", "routines", "publication", "draft"] as const
  const conflict = kinds.find((kind) => kind === raw.conflict)
  const routines = Array.isArray(raw.routines)
    ? raw.routines.filter((r): r is string => typeof r === "string")
    : undefined
  return {
    error:
      typeof raw.error === "string" && raw.error !== ""
        ? raw.error
        : "A base you reviewed changed before this publication was applied.",
    ...(conflict ? { conflict } : {}),
    ...(routines && routines.length > 0 ? { routines } : {}),
  }
}

/** POST the fenced publish, keeping the 409's shape all the way to the screen. */
function useRetainedVersionPublish(workspaceId: string, slug: string, onFenceTripped: () => void) {
  const client = useQueryClient()
  return useMutation<PagePublication, Error, FencedPublishRequest>({
    mutationFn: async (request) => {
      const params = new URLSearchParams({ workspace_id: workspaceId })
      const response = await apiFetch(
        `/api/v1/pages/${encodeURIComponent(slug)}/project/publish?${params}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(request),
        },
      )
      const body = await response.json().catch(() => null)
      if (response.status === 409) throw new PublishFenceError(conflictFromBody(body))
      if (!response.ok) {
        throw new Error((body as { error?: string } | null)?.error ?? "Publication failed.")
      }
      return body as PagePublication
    },
    onError: (error) => {
      // A 409 means a base moved under the review. The tick goes with it:
      // what was reviewed is no longer what would be published.
      if (publishConflictOf(error)) onFenceTripped()
    },
    onSettled: () => {
      for (const key of ["page-application", "page-publications", "pages"]) {
        void client.invalidateQueries({ queryKey: [key, workspaceId] })
      }
    },
  })
}

// ── Application publications ────────────────────────────────────────────────

/**
 * Publications — the retained versions, and the one control here that goes
 * live.
 *
 * Lifted out of `page-publications.tsx`. Three properties of that dialog are
 * kept exactly, because each of them exists to stop a specific wrong publish:
 *
 *  1. **The per-version review gate.** "I reviewed version N" is ticked for a
 *     version, and it is thrown away when the live publication moves under
 *     you (`reviewedHead`) — a review of what USED to be current is not a
 *     review of what is current now.
 *  2. **The commit fence.** The archived source has to be the source of the
 *     version being published; a mismatch disables the control rather than
 *     publishing something that was never read.
 *  3. **`expected_publication`.** The publish is CAS-fenced on the version
 *     the reviewer was looking at.
 *
 * What changes is the wording. This is not "restore": it makes a NEW
 * publication of a retained version, and the counter moves forward.
 */
function PublicationsCard({
  workspaceId,
  slug,
  mayPublish,
}: {
  workspaceId: string
  slug: string
  mayPublish: boolean
}) {
  const [selected, setSelected] = React.useState<PublicationReceipt | null>(null)
  const [path, setPath] = React.useState("")
  const [reviewed, setReviewed] = React.useState(false)
  const [reviewedHead, setReviewedHead] = React.useState<number | null>(null)
  /** The version whose changed routines the reviewer has acknowledged. */
  const [acknowledgedRoutines, setAcknowledgedRoutines] = React.useState<number | null>(null)
  const [confirmedWithdrawal, setConfirmedWithdrawal] = React.useState<number | null>(null)
  const [publishTarget, setPublishTarget] = React.useState<PublicationReceipt | null>(null)

  const { query, source, withdraw } = usePagePublications(workspaceId, slug, selected?.source_revision)
  const review = usePublicationReview(workspaceId, slug, selected?.version)
  const publish = useRetainedVersionPublish(workspaceId, slug, () => {
    setReviewed(false)
    setAcknowledgedRoutines(null)
  })

  const snapshot = review.data
  const blockers = snapshot?.blockers ?? []
  const routines = snapshot?.routines ?? []
  /**
   * `routines` is a UNION: the ones the version being published declares, and
   * the ones only the live publication called. Only the first kind belongs in
   * the fence — sending a routine this version drops produces a 409 naming a
   * routine nobody moved, and refetching never clears it because the snapshot
   * says the same thing again.
   */
  const fencedRoutines = routines.filter((r) => r.in_candidate)
  /** Dropped by this version. Shown, never fenced: a reviewer has to see the loss. */
  const droppedRoutines = routines.filter((r) => !r.in_candidate)
  /**
   * The case this whole panel exists for: restoring old code binds it to the
   * routine as it is NOW, which is not the routine anybody approved for that
   * version. Only routines this version actually calls can do that to it.
   */
  const changedRoutines = fencedRoutines.filter((r) => r.state === "changed")
  /** No current digest means the publish cannot fence on it. Say so; never imply it is covered. */
  const uncoveredRoutines = fencedRoutines.filter(
    (r) => typeof r.current_digest !== "string" || r.current_digest === "",
  )
  /**
   * The fence, taken off the snapshot verbatim. The routine key set is the
   * server's own — deriving it from the archived spec here would be a second
   * copy of a decision the server already makes for the rollback path.
   */
  const fence: Pick<
    FencedPublishRequest,
    "expected_definition_digest" | "expected_routine_digests"
  > | null = snapshot
    ? {
        expected_definition_digest: snapshot.baseline.definition_digest,
        expected_routine_digests: Object.fromEntries(
          fencedRoutines
            .filter((r): r is typeof r & { current_digest: string } =>
              typeof r.current_digest === "string" && r.current_digest !== "",
            )
            .map((r) => [r.routine, r.current_digest]),
        ),
      }
    : null
  const conflict = publishConflictOf(publish.error)

  const state = query.data?.pages[0]
  const receipts = query.data?.pages.flatMap((page) => page.publications) ?? []
  const file = source.data?.project.files.find((f) => f.path === path) ?? source.data?.project.files[0]
  const busy = withdraw.isPending || publish.isPending
  // The list read failing is its own answer. It is NOT "no publications" —
  // that conflation is the one this card is written to avoid.
  const listFailed = query.isError
  // Publishing is the server's decision; `can_publish` is what it said about
  // this caller, and `mayPublish` is the section's optimistic copy. Both have
  // to hold before a control that goes live is drawn.
  const canPublish = mayPublish && state?.can_publish === true

  return (
    <SectionCard
      data-slot="history-publications"
      title={<CardLabel icon={Rocket}>Application publications</CardLabel>}
      actions={
        <CardAnswer>
          {listFailed
            ? "unavailable"
            : query.isPending
              ? "loading"
              : state?.published
                ? `live version ${state.publication_version}`
                : "not published"}
        </CardAnswer>
      }
      className="gap-4 py-4"
    >
      <div className="flex flex-col gap-3">
        <p className="type-page-meta text-muted-foreground">
          Publishing a retained version makes a <strong>new live publication</strong> of it. It is
          not a return to an earlier moment: the version counter goes forward, and completed
          routine actions are not undone.
        </p>

        {listFailed && (
          <Refusal>{errorMessage(query.error, "Could not load application history.")}</Refusal>
        )}
        {conflict ? (
          <Refusal>
            <span data-slot="publish-conflict" data-conflict={conflict.conflict ?? "unknown"}>
              {conflict.error}
              {conflict.conflict === "routines" && conflict.routines?.length
                ? ` The routines that moved: ${conflict.routines.join(", ")}. Read them again before you publish.`
                : conflict.conflict === "definition"
                  ? " The Page's definition moved after you reviewed this version."
                  : conflict.conflict === "publication"
                    ? " Somebody else published while you were reading this."
                    : ""}
            </span>
          </Refusal>
        ) : (
          publish.isError && <Refusal>{errorMessage(publish.error, "Publication failed.")}</Refusal>
        )}
        {withdraw.isError && (
          <Refusal>{errorMessage(withdraw.error, "Could not withdraw the application.")}</Refusal>
        )}
        {publish.isSuccess && (
          <p role="status" className="type-page-meta text-muted-foreground">
            {publicationReceiptMessage(publish.data)}
          </p>
        )}
        {withdraw.isSuccess && (
          <p role="status" className="type-page-meta text-muted-foreground">
            Application withdrawn. Panels and producers stay available.
          </p>
        )}

        {query.isPending && !listFailed && (
          <div className="flex items-center gap-2 py-3 text-xs text-muted-foreground">
            <Spinner className="h-3.5 w-3.5" />
            Reading the publication history…
          </div>
        )}

        {state && canPublish && state.published && (
          <div className="flex flex-wrap items-center gap-3 rounded-md border border-border/50 p-2.5">
            <label className="type-page-meta flex items-center gap-1.5 text-muted-foreground">
              <input
                type="checkbox"
                className="h-3 w-3"
                checked={confirmedWithdrawal === state.publication_version}
                onChange={(event) =>
                  setConfirmedWithdrawal(event.target.checked ? state.publication_version : null)
                }
              />
              Stop this application for all viewers
            </label>
            <Button
              size="sm"
              variant="outline"
              className="h-7 border-destructive/50 px-2.5 text-xs text-destructive hover:bg-destructive/10"
              disabled={confirmedWithdrawal !== state.publication_version || busy || listFailed}
              onClick={() => withdraw.mutate(state.publication_version)}
            >
              Withdraw application
            </Button>
          </div>
        )}

        {!query.isPending && !listFailed && receipts.length === 0 && (
          <EmptyState
            size="inline"
            icon={Rocket}
            title="No application has been published yet"
            description="A publication is kept for every version that went live, so you can inspect and republish one."
          />
        )}

        {!listFailed && receipts.length > 0 && (
          <div data-slot="publications">
            {receipts.map((receipt) => (
              <div
                key={receipt.version}
                data-slot="publication"
                data-version={receipt.version}
                className="flex items-center gap-2 border-b border-border/40 py-2 last:border-b-0"
              >
                <span className="type-page-stamp w-12 shrink-0 text-muted-foreground-soft">
                  v{receipt.version}
                </span>
                <div className="flex min-w-0 flex-1 flex-col">
                  <span className="truncate text-xs text-foreground/85">
                    source r{receipt.source_revision}
                    {receipt.rollback_of ? ` · republished version ${receipt.rollback_of}` : ""}
                  </span>
                  <span className="type-page-meta truncate text-muted-foreground-soft">
                    {when(receipt.created_at)}
                    {receipt.withdrawn_at ? " · withdrawn" : ""}
                    {state?.published && state.publication_version === receipt.version
                      ? " · live now"
                      : ""}
                  </span>
                </div>
                <Button
                  size="sm"
                  variant="ghost"
                  className="type-page-meta h-6 shrink-0 px-2 text-muted-foreground hover:text-foreground"
                  onClick={() => {
                    setSelected(receipt)
                    setReviewed(false)
                    setPath("")
                  }}
                >
                  Inspect version {receipt.version}
                </Button>
              </div>
            ))}
          </div>
        )}

        {query.hasNextPage && (
          <div>
            <Button
              size="sm"
              variant="outline"
              className="h-7 px-2.5 text-xs"
              disabled={query.isFetchingNextPage}
              onClick={() => void query.fetchNextPage()}
            >
              Older publications
            </Button>
          </div>
        )}

        {!canPublish && state && (
          <ControlRefusal>
            You may not publish this Page&rsquo;s application. Inspecting a retained version is
            still yours to do; making one live is the owner&rsquo;s or a workspace
            admin&rsquo;s.
          </ControlRefusal>
        )}

        {selected && (
          <section
            className="flex flex-col gap-2 rounded-md border border-border/50 p-2.5"
            aria-label={`Inspect version ${selected.version}`}
          >
            <p className="type-page-stamp break-all text-muted-foreground">
              Build: {selected.build_id}
              <br />
              Git: {selected.git_commit}
              <br />
              Artifact: {selected.artifact_digest}
            </p>

            {source.isError && (
              <Refusal>{errorMessage(source.error, "Could not read the archived source.")}</Refusal>
            )}
            {source.isPending && !source.isError && (
              <p role="status" className="type-page-meta text-muted-foreground">
                Loading archived source…
              </p>
            )}

            {source.data && !source.isError && !listFailed && (
              <>
                <label className="type-page-meta flex items-center gap-2 text-muted-foreground">
                  Source file
                  <select
                    className="h-7 max-w-full rounded-md border border-input bg-transparent px-2 text-xs"
                    value={file?.path ?? ""}
                    onChange={(event) => setPath(event.target.value)}
                  >
                    {source.data.project.files.map((f) => (
                      <option key={f.path} value={f.path}>
                        {f.path}
                      </option>
                    ))}
                  </select>
                </label>
                {/* Text, never HTML: the archive is somebody's source and this
                    surface renders it, it does not run it. */}
                <pre className="max-h-64 overflow-auto whitespace-pre-wrap rounded border border-border/50 p-2.5 text-xs">
                  {file?.encoding === "base64"
                    ? "Binary asset (base64). Export the project to inspect this asset."
                    : file?.content}
                </pre>

                {canPublish && state && (
                  <>
                    {/* What the tick below actually attests to. Ticking "I
                        reviewed this code" while the routines it calls have
                        moved is a review of half the thing that will run. */}
                    {review.isPending && (
                      <p role="status" className="type-page-meta text-muted-foreground">
                        Reading what publishing version {selected.version} would be fenced on…
                      </p>
                    )}
                    {review.isError && (
                      <Refusal>
                        {errorMessage(
                          review.error,
                          "Could not read what publishing this version would be fenced on.",
                        )}{" "}
                        Publishing needs those values: the server compares them inside the
                        publishing transaction, and this screen will not send a fence nobody
                        reviewed.
                      </Refusal>
                    )}

                    {blockers.map((blocker) => (
                      <Refusal key={blocker.code}>
                        <span data-slot="publish-blocker" data-code={blocker.code}>
                          {blocker.message}
                        </span>
                      </Refusal>
                    ))}

                    {snapshot && (
                      <div data-slot="publication-fence" className="flex flex-col gap-1.5">
                        <p className="type-page-meta text-muted-foreground">
                          Publishing version {selected.version} is fenced on this Page&rsquo;s
                          current definition and on{" "}
                          {Object.keys(fence?.expected_routine_digests ?? {}).length} routine
                          {Object.keys(fence?.expected_routine_digests ?? {}).length === 1
                            ? ""
                            : "s"}
                          . If any of them moves before the server applies this, the publication
                          is refused rather than applied to something you did not read.
                        </p>

                        {routines.length > 0 && (
                          <ul className="flex flex-col gap-0.5">
                            {routines.map((routine) => (
                              <li
                                key={routine.routine}
                                data-slot="fence-routine"
                                data-state={routine.state}
                                data-in-candidate={routine.in_candidate ? "true" : "false"}
                                className="type-page-meta text-muted-foreground-soft"
                              >
                                <span className="font-mono text-foreground/85">
                                  {routine.routine}
                                </span>
                                {!routine.in_candidate
                                  ? ` — called by the live application; version ${selected.version} does not call it, so it is not fenced`
                                  : routine.state === "changed"
                                    ? " — changed since this version was published"
                                    : routine.state === "unknown"
                                      ? " — its current definition could not be read"
                                      : " — unchanged since this version was published"}
                              </li>
                            ))}
                          </ul>
                        )}

                        {droppedRoutines.length > 0 && (
                          <ControlRefusal>
                            Version {selected.version} does not call{" "}
                            {droppedRoutines.map((r) => r.routine).join(", ")}, which the live
                            application does. Publishing this version stops calling{" "}
                            {droppedRoutines.length === 1 ? "it" : "them"}; work already completed
                            by {droppedRoutines.length === 1 ? "it" : "them"} is not undone.
                          </ControlRefusal>
                        )}

                        {changedRoutines.length > 0 && (
                          <Refusal>
                            Version {selected.version} would run against the CURRENT definition of{" "}
                            {changedRoutines.map((r) => r.routine).join(", ")} — a routine nobody
                            approved for this version. Old code bound to a new routine is the
                            failure this check exists for. Read{" "}
                            {changedRoutines.length === 1 ? "it" : "them"} before you publish.
                          </Refusal>
                        )}

                        {uncoveredRoutines.length > 0 && (
                          <ControlRefusal>
                            {uncoveredRoutines.map((r) => r.routine).join(", ")}{" "}
                            {uncoveredRoutines.length === 1 ? "is" : "are"} not covered by this
                            check: the server could not read a current digest, so the publication
                            cannot be fenced on{" "}
                            {uncoveredRoutines.length === 1 ? "it" : "them"}.
                          </ControlRefusal>
                        )}
                      </div>
                    )}

                    <label className="type-page-meta flex items-center gap-2 text-muted-foreground">
                      <input
                        type="checkbox"
                        className="h-3 w-3"
                        checked={reviewed && reviewedHead === state.publication_version}
                        onChange={(event) => {
                          setReviewed(event.target.checked)
                          setReviewedHead(state.publication_version)
                        }}
                      />
                      I reviewed version {selected.version} and trust its code.
                    </label>

                    {/* A changed routine does not pass silently. It is a
                        second, separate thing to attest to, because it is not
                        in the code the tick above is about. */}
                    {changedRoutines.length > 0 && (
                      <label className="type-page-meta flex items-center gap-2 text-muted-foreground">
                        <input
                          type="checkbox"
                          className="h-3 w-3"
                          checked={acknowledgedRoutines === selected.version}
                          onChange={(event) =>
                            setAcknowledgedRoutines(event.target.checked ? selected.version : null)
                          }
                        />
                        I understand version {selected.version} will run against the current{" "}
                        {changedRoutines.map((r) => r.routine).join(", ")}.
                      </label>
                    )}

                    <div>
                      <Button
                        size="sm"
                        className="h-7 px-2.5 text-xs"
                        disabled={
                          !reviewed ||
                          reviewedHead !== state.publication_version ||
                          busy ||
                          listFailed ||
                          source.data.git_commit !== selected.git_commit ||
                          (state.published && state.publication_version === selected.version) ||
                          // No snapshot, no fence, no publish. The two digests
                          // are required by the server and by the type, and a
                          // guess at either is worse than not publishing.
                          fence === null ||
                          blockers.length > 0 ||
                          (changedRoutines.length > 0 && acknowledgedRoutines !== selected.version)
                        }
                        onClick={() => setPublishTarget(selected)}
                      >
                        Publish this version
                      </Button>
                    </div>
                  </>
                )}
              </>
            )}
          </section>
        )}
      </div>

      <AlertDialog
        open={publishTarget != null}
        onOpenChange={(open) => !open && setPublishTarget(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="flex items-center gap-2 text-sm">
              <AlertTriangle className="h-4 w-4 text-destructive" />
              Publish version {publishTarget?.version}
            </AlertDialogTitle>
            <AlertDialogDescription className="text-xs">
              This makes a new live publication from retained version{" "}
              {publishTarget?.version}, replacing what viewers of{" "}
              <strong>{slug}</strong> run right now. The version counter moves forward — this does
              not rewind to an earlier moment, and completed routine actions are not undone. Your
              current source draft is untouched.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel className="h-7 text-xs">Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="h-7 text-xs"
              disabled={publish.isPending}
              onClick={() => {
                // Both fence values come off the snapshot that was rendered
                // above, never off a fresh read: the point of the fence is
                // that it is the value the human saw.
                if (!publishTarget || !state || !fence) return
                publish.mutate({
                  rollback_version: publishTarget.version,
                  expected_publication: state.publication_version,
                  reviewed_code: true,
                  expected_definition_digest: fence.expected_definition_digest,
                  expected_routine_digests: fence.expected_routine_digests,
                })
                setPublishTarget(null)
              }}
            >
              {publish.isPending && <Spinner className="mr-1.5 h-3 w-3" />}
              Publish this version
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SectionCard>
  )
}

// ── The section ─────────────────────────────────────────────────────────────

export function EditorHistorySection({
  workspaceId,
  slug,
  capabilities,
  onDirtyChange,
}: EditorSectionProps) {
  // Nothing here is held unwritten: every control on this section either
  // fires one request or does nothing at all.
  React.useEffect(() => {
    onDirtyChange(false)
  }, [onDirtyChange])

  return (
    <div data-slot="editor-section-history" className="flex w-full flex-col gap-4">
      <p className="type-page-meta text-muted-foreground">
        Three kinds of history, and each restores something different. None of them is a full
        restore of the Page: grants, producer tokens, public links and panel data are not part of
        any of these.
      </p>

      <PanelVersionsCard workspaceId={workspaceId} slug={slug} title="Panel definitions" />

      {/* Only a Page that HAS an application gets application headings. An
          ordinary panel Page shows panel definitions and stops. */}
      {capabilities.hasApplication &&
        (capabilities.mayViewSourceHistory ? (
          <>
            <SourceRevisionsCard workspaceId={workspaceId} slug={slug} />
            <PublicationsCard
              workspaceId={workspaceId}
              slug={slug}
              mayPublish={capabilities.mayPublishApplication}
            />
          </>
        ) : (
          // The headings stay, because this Page really does have an
          // application and hiding them would answer a question nobody
          // asked. What is missing is named, and no list is drawn — an empty
          // list here would say "no history", which is not what a refused
          // read means.
          <SectionCard
            data-slot="history-application-refused"
            title={<CardLabel icon={History}>Application source and publications</CardLabel>}
            actions={<CardAnswer>not yours to read</CardAnswer>}
            className="gap-4 py-4"
          >
            <ControlRefusal>
              This Page has a custom application, but its source revisions and publications are
              not yours to read. That takes the right to edit the application&rsquo;s spec. This
              is not a statement that there is no history.
            </ControlRefusal>
          </SectionCard>
        ))}
    </div>
  )
}
