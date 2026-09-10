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
 */

import * as React from "react"

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

import { usePageApplication } from "@/hooks/use-page-application"
import { usePageProjectHistory } from "@/hooks/use-page-project-history"
import { usePagePublications, type PublicationReceipt } from "@/hooks/use-page-publications"
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
  const [confirmedWithdrawal, setConfirmedWithdrawal] = React.useState<number | null>(null)
  const [publishTarget, setPublishTarget] = React.useState<PublicationReceipt | null>(null)

  const { query, source, withdraw } = usePagePublications(workspaceId, slug, selected?.source_revision)
  const { publish } = usePageApplication(workspaceId, slug)

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
        {publish.isError && (
          <Refusal>{errorMessage(publish.error, "Publication failed.")}</Refusal>
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
                          (state.published && state.publication_version === selected.version)
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
                if (!publishTarget || !state) return
                publish.mutate({
                  rollback_version: publishTarget.version,
                  expected_publication: state.publication_version,
                  reviewed_code: true,
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
    <div data-slot="editor-section-history" className="flex w-full max-w-3xl flex-col gap-4">
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
