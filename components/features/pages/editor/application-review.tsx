"use client"

import { useEffect, useMemo, useRef, useState } from "react"

import { Button } from "@/components/ui/button"
import { ReviewPreview } from "@/components/features/pages/editor/review-preview"
import type { EditorSectionProps } from "@/components/features/pages/editor/section-props"
import { usePageReview } from "@/hooks/use-page-review"
import { usePagePreview } from "@/hooks/use-page-preview"
import { compareDefinitions } from "@/lib/pages/definition-diff"
import { compareSources } from "@/lib/pages/source-diff"
import {
  SAVE_EFFECT_NOTE,
  SOURCE_DIFF_LIMITS,
  type DefinitionChangeTone,
  type PublishConflictWire,
  type ReviewActorWire,
  type ReviewBlockerWire,
  type ReviewRoutineWire,
  type ReviewSnapshotWire,
  type SourceDiff,
  type SourceFileChange,
  type SourceFileStatus,
} from "@/lib/pages/editor-contract"

/**
 * The screen where a human decides whether an agent's change goes live.
 *
 * Everything on it is derived. Nothing an agent wrote about its own change is
 * displayed as evidence, because a summary written by the author of the change
 * is marketing and the reviewer has no way to tell the two apart.
 *
 * Four properties are load-bearing and each has a test:
 *
 *   - Consent is bound to one candidate AND one set of bases. Anything moving
 *     under it clears the checkbox *visibly*, with the reason written out. A
 *     silent reset is worse than none: the person believes they consented.
 *   - Consent is not offered at all until every piece of evidence the decision
 *     rests on has arrived — and the definition it attests to is the one this
 *     screen rendered, because both came out of the same authorized read.
 *   - "Unknown" is never rendered as "unchanged" — not for a routine hash, not
 *     for a missing baseline, not for a field the comparator does not model.
 *   - There is no Reject. A rejection needs a recipient and this screen has
 *     none; the copy says so instead of pretending otherwise.
 */

/**
 * Candidate-controlled text, made safe to put in a sentence.
 *
 * `compareDefinitions` promises one line of plain text for `summary` and for
 * nothing else. `before`, `after` and every name in `unmodelled` are values
 * and keys lifted straight out of a document an agent wrote: a 400-character
 * title with newlines in it, a stringified `wake` or `confirm` blob, or a key
 * literally named "nothing — this candidate is unchanged". Rendered raw they
 * either wreck the layout of the list or read as this screen's own copy,
 * which in the block whose whole job is to say the review is INCOMPLETE is
 * the worst place in the product to be impersonated.
 *
 * Flattened to one line, control characters removed, hard length cap.
 */
function oneLine(value: string, max = 80): string {
  const flat = value.replace(/[\u0000-\u001F\u007F-\u009F]/g, " ").replace(/\s+/g, " ").trim()
  if (flat === "") return "(empty)"
  return flat.length <= max ? flat : `${flat.slice(0, max - 1)}…`
}

/** Names, capped in count as well as in length: an unmodelled list is evidence, not a dump. */
const UNMODELLED_SHOWN = 12

const TONE_MARK: Readonly<Record<DefinitionChangeTone, string>> = { add: "+", remove: "−", change: "~" }
const TONE_WORD: Readonly<Record<DefinitionChangeTone, string>> = { add: "Added", remove: "Removed", change: "Changed" }
const STATUS_MARK: Readonly<Record<SourceFileStatus, string>> = { added: "A", modified: "M", removed: "D" }
const STATUS_WORD: Readonly<Record<SourceFileStatus, string>> = { added: "Added", modified: "Modified", removed: "Removed" }

/**
 * The identity the server can actually prove. `page_project_revisions` stores
 * ids, not a snapshot of names (V03), so a missing label becomes the kind and
 * the raw id — never a guessed name, and never a link into a chat, because no
 * verified link between a revision and a conversation exists.
 */
export function reviewAuthorLabel(actor: ReviewActorWire | undefined): string {
  if (!actor) return "Unknown author"
  if (typeof actor.label === "string" && actor.label.trim() !== "") return actor.label.trim()
  if (actor.kind === "unknown" || !actor.id) return "Unknown author"
  return `${actor.kind} ${actor.id}`
}

function conflictSentence(conflict: PublishConflictWire): string {
  const named = conflict.routines && conflict.routines.length > 0 ? ` Re-review: ${conflict.routines.join(", ")}.` : ""
  switch (conflict.conflict) {
    case "definition":
      return `The live Page definition changed before this publication was applied. Your review consent was cleared; read the new definition before publishing.${named}`
    case "routines":
      return `A routine this application calls changed before this publication was applied. Your review consent was cleared; read the routine again before publishing.${named}`
    case "publication":
      return `The live publication changed before this one was applied. Your review consent was cleared; refresh the review and start again.${named}`
    case "draft":
      return `The candidate draft changed before this publication was applied. Your review consent was cleared; read the new draft before publishing.${named}`
    default:
      return `${conflict.error} Your review consent was cleared; refresh the review before publishing.${named}`
  }
}

/**
 * The live definition the review was issued against, and how much of it this
 * viewer may not see.
 *
 * The server reads both in the same statement, off the same row, as
 * `definition_digest` — which is the whole point. The document rendered here
 * and the digest the publish fence sends then come out of ONE authorized read
 * at ONE instant, so consenting to the comparison on screen is consenting to
 * the value the request attests to. Deriving the comparison from the Page
 * detail query instead is the hole R1 of the 2026-09-11 counter-review
 * reproduced: two endpoints, two cache entries, and a request that can carry a
 * digest for a document the screen never showed. No amount of re-clearing the
 * checkbox fixes that, because the two sides are never required to correspond.
 *
 * Written as an intersection because `editor-contract.ts` belongs to the
 * stream landing these wire types; when they arrive there this alias is a
 * no-op and goes away.
 */
type ReviewBaseline = ReviewSnapshotWire["baseline"] & {
  /** The live Page document, authorized for this viewer. */
  readonly definition?: unknown
  /** How many panels were withheld from `definition` because this viewer may not read them. */
  readonly excluded_panels?: number
}

/**
 * The live definition the snapshot carries, or `undefined` when it did not
 * arrive. Absent and null are the same fact to this screen — there is no
 * document to compare against — and neither may be rendered as "no changes".
 */
function liveDefinitionOf(snapshot: ReviewSnapshotWire | undefined): unknown {
  return (snapshot?.baseline as ReviewBaseline | undefined)?.definition ?? undefined
}

function excludedPanelsOf(snapshot: ReviewSnapshotWire | undefined): number {
  const count = (snapshot?.baseline as ReviewBaseline | undefined)?.excluded_panels
  return typeof count === "number" && Number.isFinite(count) && count > 0 ? Math.floor(count) : 0
}

/** One read this decision depends on, as the screen sees it. */
export interface EvidenceRead {
  /** The query answered, and the answer carries what this screen needs. */
  readonly arrived: boolean
  /** It answered with an error instead; the server's sentence, when there was one. */
  readonly error: string | null
}

export interface ReviewEvidence {
  /** Every piece of evidence this decision needs is here and corresponds. */
  readonly complete: boolean
  /** One sentence per piece that is not, in the order a reader should read them. */
  readonly missing: readonly string[]
}

/**
 * Whether the person is actually looking at what they would be attesting to.
 *
 * `blockers` is the server's list of refusals. It says nothing about whether
 * the CLIENT has the evidence in front of the human: the candidate's source
 * can be pending or errored and the server would still have no objection, so
 * without this the screen offers consent, unlocks Publish, and leaves the
 * source diff on "Reading…" for ever (R2). `candidateMoved` does not help —
 * it is false precisely when the data is absent.
 *
 * Correspondence is not checked here because it is not checkable here: it
 * holds by construction, because the definition compared on screen and the
 * digest the fence sends both come from `baseline` of this one snapshot.
 * What is checked is that each piece is present at all.
 *
 * `baselineSource` is null for the one legitimate absence: an initial
 * publication, where there is no previous source, and the case where the
 * retained source cannot be read at all — which is its own blocker and its
 * own sentence, and must not be reported twice.
 */
export function reviewEvidence(input: {
  readonly snapshotArrived: boolean
  readonly liveDefinitionArrived: boolean
  readonly candidateDefinitionArrived: boolean
  readonly candidateSource: EvidenceRead
  readonly baselineSource: EvidenceRead | null
}): ReviewEvidence {
  const missing: string[] = []
  if (!input.snapshotArrived) {
    missing.push("The review of this candidate has not been read yet.")
  }
  if (!input.liveDefinitionArrived) {
    missing.push(
      "The live Page definition this candidate is compared against did not arrive with the review, so the definition changes above are not a comparison with anything.",
    )
  }
  if (input.candidateSource.error !== null) {
    missing.push(`The candidate's source could not be read: ${input.candidateSource.error}`)
  } else if (!input.candidateSource.arrived) {
    missing.push("The candidate's source has not finished loading.")
  } else if (!input.candidateDefinitionArrived) {
    missing.push("The candidate's draft arrived without a definition, so there is nothing to compare with the live Page.")
  }
  if (input.baselineSource !== null) {
    if (input.baselineSource.error !== null) {
      missing.push(`The source behind the live publication could not be read: ${input.baselineSource.error}`)
    } else if (!input.baselineSource.arrived) {
      missing.push("The source behind the live publication has not finished loading.")
    }
  }
  return { complete: missing.length === 0, missing }
}

/** The exact values consent is bound to. A change in any of them ends it. */
interface ConsentBasis {
  readonly revision: string
  readonly build: string
  /**
   * The live definition's digest — the value the fence sends, and now also the
   * identity of the document the change list was computed from, because both
   * come from `baseline` of the same snapshot. There is no second key for "what
   * was rendered": a field promising that the rendered document matches the
   * fenced digest, while the two were read from different endpoints, promised
   * something it could not deliver.
   */
  readonly definition: string
  readonly publication: string
  readonly routines: string
}

function consentBasisOf(snapshot: ReviewSnapshotWire | undefined): ConsentBasis {
  return {
    revision: snapshot?.candidate ? `${snapshot.candidate.revision}:${snapshot.candidate.git_commit}:${snapshot.candidate.source_digest}` : "no-candidate",
    build: snapshot?.candidate?.build ? `${snapshot.candidate.build.id}:${snapshot.candidate.build.state}:${snapshot.candidate.build.artifact_digest}` : "no-build",
    definition: snapshot?.baseline.definition_digest ?? "no-definition",
    publication: `${snapshot?.baseline.publication_version ?? 0}:${snapshot?.baseline.published ?? false}`,
    routines: (snapshot?.routines ?? []).map(r => `${r.routine}=${r.current_digest ?? "?"}/${r.published_digest ?? "?"}/${r.in_candidate}`).join(","),
  }
}

function basisKey(basis: ConsentBasis): string {
  return [basis.revision, basis.build, basis.definition, basis.publication, basis.routines].join("|")
}

export function EditorApplicationReview(props: EditorSectionProps) {
  const { workspaceId, slug, page, capabilities, pane, onPaneChange, onDirtyChange } = props
  const review = usePageReview(
    workspaceId,
    slug,
    // Source exists, published or not. On the published flag a draft-only
    // Page disabled all three queries here and the screen only rendered
    // because its parent had filled the shared cache — and `gcTime: 0` puts
    // that one refactor away from reading nothing at all.
    capabilities.hasApplicationDraft,
  )
  const previewBuild = usePagePreview(workspaceId, slug)
  const snapshot = review.snapshot.data

  const [consent, setConsent] = useState(false)
  const [resetReason, setResetReason] = useState<string | null>(null)
  const heading = useRef<HTMLHeadingElement>(null)

  // This screen holds a decision, not unsaved edits. Saying so keeps the
  // shell's dirty guard from claiming there is work to lose.
  useEffect(() => {
    onDirtyChange(false)
    return () => onDirtyChange(false)
  }, [onDirtyChange])

  // The live definition comes off the snapshot, never off the Page detail
  // query: `ReviewBaseline` above says why, and it is the whole of R1's fix.
  const liveDefinition = useMemo(() => liveDefinitionOf(snapshot), [snapshot])
  const basis = useMemo(() => consentBasisOf(snapshot), [snapshot])
  const basisRef = useRef<ConsentBasis | null>(null)
  const published = review.publish.isSuccess
  useEffect(() => {
    if (!snapshot) return
    const previous = basisRef.current
    basisRef.current = basis
    if (!previous || basisKey(previous) === basisKey(basis)) return
    // A successful publish moves every one of these on purpose — the draft is
    // consumed and the snapshot comes back with no candidate. Announcing "the
    // candidate changed while this review was open, read the new candidate"
    // directly above "Published as version 4" is a false account of what the
    // person just did.
    if (published) return
    setConsent(false)
    if (previous.revision !== basis.revision) {
      setResetReason(`The candidate changed while this review was open. Your review consent was cleared; read the new candidate before publishing.`)
    } else if (previous.build !== basis.build) {
      setResetReason("A new build of this candidate landed. Your review consent was cleared; consent applies to one build.")
    } else if (previous.definition !== basis.definition || previous.publication !== basis.publication) {
      setResetReason("A base you were comparing against moved. Your review consent was cleared; read the new baseline before publishing.")
    } else {
      setResetReason("A routine this application calls changed while this review was open. Your review consent was cleared; read the dependency again before publishing.")
    }
  }, [basis, snapshot, published])

  // Returning from the preview ends consent: the person is coming back to make
  // a decision, not carrying one across a detour.
  const paneRef = useRef(pane)
  useEffect(() => {
    if (paneRef.current === "preview" && pane === "section") {
      setConsent(false)
      setResetReason("You returned from the preview. Your review consent was cleared; make the decision here.")
      heading.current?.focus()
    }
    paneRef.current = pane
  }, [pane])

  const conflict = review.conflict
  useEffect(() => {
    if (!conflict) return
    setConsent(false)
    setResetReason(conflictSentence(conflict))
  }, [conflict])

  const blockers = useMemo<readonly ReviewBlockerWire[]>(() => {
    if (!snapshot) return []
    const out: ReviewBlockerWire[] = [...snapshot.blockers]
    const has = (code: ReviewBlockerWire["code"]) => out.some(b => b.code === code)
    if (review.baselineUnavailable && !has("baseline_unavailable")) {
      out.push({ code: "baseline_unavailable", message: `The source behind the live publication cannot be read, so this change cannot be compared. ${review.baselineUnavailable} Publishing from this review is blocked.` })
    }
    if (review.candidateMoved && !has("build_stale")) {
      out.push({ code: "build_stale", message: "The application draft changed after this review was issued. Refresh the review so you approve the draft that would go live." })
    }
    if (!snapshot.capabilities.may_publish && !has("not_permitted")) {
      out.push({ code: "not_permitted", message: "You may read this review, but publishing this application needs the Page owner or a workspace administrator." })
    }
    if (conflict && !has("definition_moved")) {
      out.push({ code: "definition_moved", message: conflictSentence(conflict) })
    }
    return out
  }, [snapshot, review.baselineUnavailable, review.candidateMoved, conflict])

  const blocked = blockers.length > 0

  // Panels this viewer may not read were withheld from the live definition by
  // the server, which also says how many. Counting them here off the Page
  // detail would be a second source for a fact the compared document already
  // carries — and the two could disagree about the very document on screen.
  const excludedPanels = excludedPanelsOf(snapshot)
  const definitionDiff = useMemo(() => {
    if (liveDefinition === undefined) return null
    const candidateDefinition = review.candidate.data?.definition
    if (candidateDefinition === undefined) return null
    return compareDefinitions(liveDefinition, candidateDefinition)
  }, [review.candidate.data, liveDefinition])

  const initial = snapshot?.initial_publication === true
  const comparisonUnavailable = review.baselineUnavailable
  const sourceDiff = useMemo(() => {
    if (comparisonUnavailable) return null
    const candidateProject = review.candidate.data?.project
    if (candidateProject === undefined) return null
    // Initial publication compares against nothing, on purpose: `null` means
    // "there was no earlier source", which the comparator renders as an
    // all-new candidate. An empty project would render the same shape and
    // mean something entirely different.
    const baselineProject = initial ? null : (review.baseline.data?.project ?? undefined)
    if (baselineProject === undefined) return null
    return compareSources(baselineProject, candidateProject)
  }, [comparisonUnavailable, initial, review.candidate.data, review.baseline.data])

  // A read that FAILED is not a read that is still going. Left as "Reading…"
  // it never resolves and the screen keeps offering a decision over evidence
  // that will never appear (R2). Both are surfaced where the missing evidence
  // would have been, each with the control that can clear it.
  const candidateSourceError = review.candidate.isError ? (review.candidate.error?.message ?? "No reason was given.") : null
  const baselineSourceError = review.baseline.isError ? (review.baseline.error?.message ?? "No reason was given.") : null

  const evidence = useMemo(
    () =>
      reviewEvidence({
        snapshotArrived: snapshot !== undefined,
        liveDefinitionArrived: liveDefinitionOf(snapshot) !== undefined,
        candidateDefinitionArrived: review.candidate.data?.definition !== undefined,
        candidateSource: { arrived: review.candidate.data?.project != null, error: candidateSourceError },
        // The two legitimate absences, and only these: an initial publication
        // has no previous source, and a retained source that cannot be read is
        // already `baseline_unavailable` — a blocker with its own sentence.
        baselineSource: initial || comparisonUnavailable ? null : { arrived: review.baseline.data !== undefined, error: baselineSourceError },
      }),
    [snapshot, review.candidate.data, review.baseline.data, candidateSourceError, baselineSourceError, initial, comparisonUnavailable],
  )

  // Disabling the box is not the same as clearing it. A blocker that appears
  // and then goes away again — a transient `baseline.isError`, say — would
  // otherwise leave the box ticked from before and Publish live the moment the
  // blocker cleared, on a review nobody looked at again. The same is true of
  // evidence that arrives, vanishes on a refetch, and comes back.
  const evidenceComplete = evidence.complete
  useEffect(() => {
    if (blocked || !evidenceComplete) setConsent(false)
  }, [blocked, evidenceComplete])

  // The shell today gates on `capabilities.hasApplication`, and those
  // capabilities are derived from `detail.raw` while `page` arrives as
  // `detail.error ? null : detail.raw` (`pages-layout.tsx:263`) — the two can
  // disagree. The comparison itself no longer depends on this read: it is made
  // against the definition the snapshot carries. What still does is the
  // candidate's preview and the permission the shell read from the same
  // record, so a review is not offered over a Page that could not be loaded.
  if (!page) {
    return (
      <section className="flex flex-col gap-3">
        <h2 className="text-lg font-semibold">Review application changes</h2>
        <p role="alert" className="text-sm">
          This Page could not be loaded. Reviewing is not offered while it is missing: the candidate&apos;s preview renders with this Page&apos;s data, and the permission to
          publish it was read from the same record. Reload before reviewing.
        </p>
        <div>
          <Button variant="outline" className="min-h-11" onClick={review.refresh}>
            Try again
          </Button>
        </div>
      </section>
    )
  }

  if (pane === "preview") {
    return (
      <ReviewPreview
        workspaceId={workspaceId}
        slug={slug}
        page={page}
        candidateRevision={snapshot?.candidate?.revision ?? null}
        onReturn={() => onPaneChange("section")}
      />
    )
  }

  if (review.snapshot.isError) {
    return (
      <section className="flex flex-col gap-3">
        <h2 className="text-lg font-semibold">Review application changes</h2>
        <p role="alert" className="text-sm text-destructive">
          {review.snapshot.error.message}
        </p>
        <div>
          <Button variant="outline" className="min-h-11" onClick={review.refresh}>
            Try again
          </Button>
        </div>
      </section>
    )
  }

  if (!snapshot) {
    return (
      <section className="flex flex-col gap-3">
        <h2 className="text-lg font-semibold">Review application changes</h2>
        <p role="status" className="text-sm text-muted-foreground">
          Loading this application&apos;s review…
        </p>
      </section>
    )
  }

  // Independent review §5 rule 6: with no draft, or a draft identical to the
  // live publication, this screen must not present itself as a pending review.
  // The candidate query is never enabled in that state, so the two "Reading…"
  // placeholders below would sit there for ever.
  if (!snapshot.candidate) {
    return (
      <section className="flex flex-col gap-3" aria-labelledby="application-review-heading">
        <h2 id="application-review-heading" ref={heading} tabIndex={-1} className="text-lg font-semibold outline-none">
          {published ? "Application published" : "Nothing to review"}
        </h2>
        {published && review.publish.data ? (
          <p role="status" className="text-sm">
            Published as version {review.publish.data.version}. The draft that was reviewed is now the live application.
          </p>
        ) : (
          <p className="text-sm">
            This Page&apos;s application has no draft awaiting review: there is either no draft at all, or the draft is identical to the live publication. Nothing here needs a
            decision.
          </p>
        )}
        {snapshot.blockers.map(blocker => (
          <p key={blocker.code} className="text-sm text-muted-foreground" data-blocker={blocker.code}>
            {blocker.message}
          </p>
        ))}
        <div>
          <Button variant="outline" className="min-h-11" onClick={() => props.onNavigate("content")}>
            Go to Page content
          </Button>
        </div>
      </section>
    )
  }

  const candidate = snapshot.candidate
  const baseline = snapshot.baseline
  const withdrawn = !initial && !baseline.published && baseline.publication_version > 0
  const build = candidate?.build ?? null

  return (
    <section className="flex w-full min-w-0 flex-col gap-6" aria-labelledby="application-review-heading">
      {/* 1 — Header: the Page, the candidate, the provable author, the time. */}
      <header className="flex flex-col gap-1">
        <h2 id="application-review-heading" ref={heading} tabIndex={-1} className="text-lg font-semibold outline-none">
          Review application changes
        </h2>
        <p className="text-sm">
          {page?.name ?? slug}
          {candidate ? ` · Draft ${candidate.revision}` : " · No candidate"}
          {candidate ? ` · saved by ${reviewAuthorLabel(candidate.actor)}` : ""}
          {candidate ? ` · ${candidate.created_at === "" ? "saved at an unknown time" : candidate.created_at}` : ""}
        </p>
        {/* The two bases are different series and are stated separately: a
            source revision number and a publication number never line up. */}
        <p className="text-sm text-muted-foreground">
          {initial
            ? "Initial application publication · the definition is compared with the current live Page"
            : withdrawn
              ? "No live application: the last publication was withdrawn · the definition is compared with the current live Page"
              : `Source is compared with live publication ${baseline.publication_version}${baseline.source_revision !== null ? ` (source revision ${baseline.source_revision})` : ""} · the definition is compared with the current live Page`}
        </p>
        <p className="text-xs text-muted-foreground">
          Source revision numbers and publication numbers are different series and cannot be compared with each other. Author names and a link into a chat need verified
          metadata; this screen shows only the identity the server can prove.
        </p>
        {withdrawn && (
          <p role="note" className="text-sm">
            There is no live application right now. The receipt of the withdrawn publication is history, not a live basis for this comparison.
          </p>
        )}
        {initial && (
          <p role="note" className="rounded-md border border-dashed p-3 text-sm">
            <strong>Initial publication.</strong> No previous application exists. Review the whole candidate: every source file is new. The definition impact can still be
            compared against the live panel Page.
          </p>
        )}
      </header>

      {/* Conflict — a base moved. A fresh build does not fix this. */}
      {blockers.some(b => b.code === "definition_moved") && (
        <div role="alert" className="rounded-md border border-destructive p-3 text-sm">
          <strong>A base you reviewed moved</strong>
          <p className="mt-1">{blockers.find(b => b.code === "definition_moved")!.message}</p>
          <p className="mt-1 text-muted-foreground">Building the candidate again does not clear this. Refresh the review and read the new baseline.</p>
          <Button variant="outline" className="mt-2 min-h-11" onClick={review.refresh}>
            Refresh review
          </Button>
        </div>
      )}

      {/* 2 — Definition changes, derived. */}
      <div className="rounded-md border p-4">
        <h3 className="text-base font-semibold">Definition changes</h3>
        {candidateSourceError !== null ? (
          <div role="alert" className="mt-2 text-sm">
            <strong>The candidate&apos;s definition could not be read</strong>
            <p className="mt-1">{candidateSourceError}</p>
            <p className="mt-1">Nothing is compared here, because there is nothing truthful to compare. This is not a statement that the definition is unchanged.</p>
            <Button variant="outline" className="mt-2 min-h-11" onClick={review.refresh}>
              Try again
            </Button>
          </div>
        ) : liveDefinition === undefined ? (
          <div role="alert" className="mt-2 text-sm">
            <strong>The live definition did not arrive with this review</strong>
            <p className="mt-1">
              The review carries the live Page definition the candidate is compared against, alongside the digest the publication is checked with. This one did not, so there
              is nothing to compare the candidate with — and comparing it against the Page read elsewhere would mean approving a digest for a document this screen never
              showed you.
            </p>
            <Button variant="outline" className="mt-2 min-h-11" onClick={review.refresh}>
              Try again
            </Button>
          </div>
        ) : definitionDiff === null ? (
          <p className="mt-2 text-sm text-muted-foreground">Reading the candidate&apos;s definition…</p>
        ) : definitionDiff.identical && !definitionDiff.baselineMissing && definitionDiff.unmodelled.length === 0 ? (
          <p className="mt-2 text-sm">This candidate declares the same panels, producers and actions as the live Page.</p>
        ) : (
          <ul className="mt-2 flex flex-col gap-2">
            {definitionDiff.changes.map((change, index) => (
              <li key={`${change.kind}-${change.panelId ?? ""}-${change.actionId ?? ""}-${index}`} className="text-sm">
                <span aria-hidden="true" className="mr-2 font-mono">
                  {TONE_MARK[change.tone]}
                </span>
                <span className="mr-2 rounded border px-1 text-xs uppercase">{TONE_WORD[change.tone]}</span>
                {change.summary}
                {(change.before !== undefined || change.after !== undefined) && (
                  <span className="ml-1 text-muted-foreground">
                    ({change.before === undefined ? "not set" : oneLine(change.before)} → {change.after === undefined ? "not set" : oneLine(change.after)})
                  </span>
                )}
              </li>
            ))}
          </ul>
        )}
        <p className="mt-3 text-xs text-muted-foreground">
          Derived from comparing the two definitions. Source code may change behaviour this list cannot see.
        </p>
        {definitionDiff !== null && definitionDiff.baselineMissing && (
          <p role="note" className="mt-2 rounded-md border border-dashed p-3 text-sm">
            <strong>There was no live definition to compare against.</strong> Every panel and action above therefore reads as added. That is true of a first publication; it
            is not a statement that nothing else changed.
          </p>
        )}
        {excludedPanels > 0 && (
          <p role="note" className="mt-2 text-sm">
            {excludedPanels} panel{excludedPanels === 1 ? "" : "s"} on this Page {excludedPanels === 1 ? "is" : "are"} not visible to you, so the server withheld{" "}
            {excludedPanels === 1 ? "it" : "them"} from the definition this comparison was made against. This review does not cover {excludedPanels === 1 ? "it" : "them"}, and
            a panel the candidate declares that you may not read appears above as added.
          </p>
        )}
        {definitionDiff !== null && definitionDiff.unmodelled.length > 0 && (
          <div role="note" className="mt-3 rounded-md border border-dashed p-3 text-sm">
            <strong>
              Fields this comparison does not model: {definitionDiff.unmodelled.slice(0, UNMODELLED_SHOWN).map(name => oneLine(name, 60)).join(", ")}
              {definitionDiff.unmodelled.length > UNMODELLED_SHOWN ? ` and ${definitionDiff.unmodelled.length - UNMODELLED_SHOWN} more` : ""}
            </strong>
            <p className="mt-1">A field that is not modelled has not been checked. It must not be read as unchanged — read the raw definition diff below.</p>
            <pre className="mt-2 max-h-64 overflow-auto whitespace-pre-wrap break-words rounded bg-muted p-2 text-xs">{definitionDiff.raw}</pre>
          </div>
        )}
      </div>

      {/* 3 — Execution dependencies, at the moment of approval. */}
      <div className="rounded-md border p-4">
        <h3 className="text-base font-semibold">Execution dependencies</h3>
        {snapshot.routines.length === 0 ? (
          <p className="mt-2 text-sm text-muted-foreground">This application&apos;s definition names no routines.</p>
        ) : (
          <ul className="mt-2 flex flex-col gap-3">
            {snapshot.routines.map(routine => (
              <RoutineRow key={routine.routine} routine={routine} />
            ))}
          </ul>
        )}
      </div>

      {/* 4 — Source changes. */}
      <div className="rounded-md border p-4">
        <h3 className="text-base font-semibold">Source changes</h3>
        {comparisonUnavailable ? (
          <div role="alert" className="mt-2 text-sm">
            <strong>Comparison unavailable</strong>
            <p className="mt-1">{comparisonUnavailable}</p>
            <p className="mt-1">This does not mean there are no changes. No diff is shown because there is nothing truthful to show, and publishing from this review is blocked.</p>
            {/* Only when the read FAILED. A retained source that was pruned is
                gone; a Try again there is an invitation to click for ever. */}
            {baselineSourceError !== null && (
              <Button variant="outline" className="mt-2 min-h-11" onClick={review.refresh}>
                Try again
              </Button>
            )}
          </div>
        ) : candidateSourceError !== null ? (
          <div role="alert" className="mt-2 text-sm">
            <strong>The candidate&apos;s source could not be read</strong>
            <p className="mt-1">{candidateSourceError}</p>
            <p className="mt-1">This does not mean there are no changes. No diff is shown because there is nothing truthful to show, and publishing from this review is blocked.</p>
            <Button variant="outline" className="mt-2 min-h-11" onClick={review.refresh}>
              Try again
            </Button>
          </div>
        ) : sourceDiff === null ? (
          <p className="mt-2 text-sm text-muted-foreground">Reading the source of both sides…</p>
        ) : (
          <SourceChanges diff={sourceDiff} initial={initial} />
        )}
      </div>

      {/* 5 — Candidate build. */}
      <div className="rounded-md border p-4">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <h3 className="text-base font-semibold">Candidate build</h3>
            <p className="mt-1 text-sm">
              {/* The states the database actually allows. `interrupted` is not
                  a compiler error: the build never reached a verdict, there is
                  no log to print, and the only honest instruction is to run it
                  again. Folding it into `failed` would send the reviewer
                  hunting for a reason that was never written. */}
              {build === null
                ? "Not built"
                : build.state === "running"
                  ? "Building…"
                  : build.state === "failed"
                    ? `Failed — ${build.error ?? "the compiler gave no reason."}`
                    : build.state === "interrupted"
                      ? "Interrupted — this build stopped before it finished, so there is no result and no build log. Build the candidate again."
                      : `Ready — a build of draft ${candidate?.revision} exists`}
            </p>
            <p className="mt-1 text-xs text-muted-foreground">
              &quot;Ready&quot; means a build of this candidate exists. It is not automated verification of the actions. {SAVE_EFFECT_NOTE.build}
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button
              variant="outline"
              className="min-h-11"
              disabled={!candidate || previewBuild.build.isPending || build?.state === "running"}
              onClick={() => candidate && previewBuild.build.mutate(candidate.revision)}
            >
              Build preview
            </Button>
            <Button variant="outline" className="min-h-11" disabled={build?.state !== "ready"} onClick={() => onPaneChange("preview")}>
              Open preview
            </Button>
          </div>
        </div>
        {build?.state === "failed" && build.error && (
          <pre role="alert" className="mt-3 max-h-48 overflow-auto whitespace-pre-wrap break-words rounded bg-muted p-3 text-xs">
            {build.error}
          </pre>
        )}
        {previewBuild.build.error && (
          <p role="alert" className="mt-2 text-sm text-destructive">
            {previewBuild.build.error.message}
          </p>
        )}
      </div>

      {/* 6 — Publish. */}
      <div className="rounded-md border p-4">
        <h3 className="text-base font-semibold">Publish</h3>
        <p className="mt-1 text-sm text-muted-foreground">{SAVE_EFFECT_NOTE.publication}</p>

        {resetReason && (
          <p role="status" className="mt-3 rounded-md border border-dashed p-3 text-sm">
            {resetReason}
          </p>
        )}

        {blocked && (
          <div className="mt-3">
            <h4 className="text-sm font-semibold">Publishing is blocked</h4>
            <ul className="mt-1 flex list-disc flex-col gap-1 pl-5">
              {blockers.map(blocker => (
                <li key={blocker.code} className="text-sm" data-blocker={blocker.code}>
                  {blocker.message}
                </li>
              ))}
            </ul>
          </div>
        )}

        {/* Not a grey checkbox with no account of itself: the evidence that is
            missing is named, and the control that can fetch it is here. */}
        {!evidence.complete && (
          <div className="mt-3" data-evidence="incomplete">
            <h4 className="text-sm font-semibold">Not everything this decision rests on is here</h4>
            <ul className="mt-1 flex list-disc flex-col gap-1 pl-5">
              {evidence.missing.map(sentence => (
                <li key={sentence} className="text-sm" data-missing-evidence="">
                  {sentence}
                </li>
              ))}
            </ul>
            <p className="mt-1 text-sm text-muted-foreground">
              Consent is not offered until all of it is on screen. Nothing above says this candidate is safe or unsafe; it says this review is incomplete.
            </p>
            <Button variant="outline" className="mt-2 min-h-11" onClick={review.refresh}>
              Try again
            </Button>
          </div>
        )}

        <label className="mt-3 flex min-h-11 items-start gap-2 text-sm">
          <input
            type="checkbox"
            className="mt-1 size-4"
            checked={consent}
            disabled={blocked || !evidence.complete}
            onChange={event => {
              setConsent(event.target.checked)
              if (event.target.checked) setResetReason(null)
            }}
          />
          <span>
            I reviewed this candidate&apos;s code, definition changes and behaviour.
            {candidate ? ` This applies to draft ${candidate.revision}${build ? ` and build ${build.id}` : ""}, compared with publication ${baseline.publication_version}.` : ""}
          </span>
        </label>

        <div className="mt-3 flex flex-wrap items-center gap-3">
          <Button className="min-h-11" disabled={!consent || blocked || !evidence.complete || review.publish.isPending} onClick={() => review.publish.mutate()}>
            {review.publish.isPending ? "Publishing…" : "Publish application"}
          </Button>
        </div>
        {review.publish.isError && !conflict && (
          <p role="alert" className="mt-2 text-sm text-destructive">
            {review.publish.error.message}
          </p>
        )}
        {review.publish.isSuccess && (
          <p role="status" className="mt-2 text-sm">
            Published as version {review.publish.data.version}.
          </p>
        )}
      </div>

      {/* 7 — Close without publishing. There is no Reject: it has no recipient. */}
      <div className="rounded-md border p-4">
        <h3 className="text-base font-semibold">Close without publishing</h3>
        <p className="mt-1 text-sm">
          The draft is retained and the live application does not change. No message is sent to its author. To disagree, tell the agent in chat.
        </p>
        {/* No Reject button. A rejection needs a recipient, and this screen has
            none: there is no verified channel from a review back to the agent
            that wrote the revision. Naming a button "Reject" would promise one. */}
        {/* The sentence above is the whole message, and it is on screen
            *before* the click: leaving the editor is the last thing that
            happens, so there is no panel afterwards left to read it in. */}
        <Button
          variant="outline"
          className="mt-3 min-h-11"
          onClick={() => {
            setConsent(false)
            setResetReason(null)
            props.onLeaveEditor()
          }}
        >
          Close without publishing
        </Button>
      </div>
    </section>
  )
}

/**
 * One execution dependency.
 *
 * Two separate facts live on this row and they are not the same fact:
 *
 *   - whether the routine's DEFINITION moved since the live publication
 *     (`state`), which is what the reviewer is being asked to look at, and
 *   - whether the publish fence COVERS it, which is decided by whether the
 *     snapshot carried a current hash at all.
 *
 * A routine with no current hash cannot be fenced — `usePageReview` leaves it
 * out of `expected_routine_digests` rather than inventing an empty string to
 * fence against — so publishing will not fail if that routine changes between
 * this screen and the transaction. That is the honest handling of a missing
 * hash, and it is also a gap in the guarantee, so the row says so in words
 * instead of leaving it in a design note nobody reading this screen can see.
 */
function RoutineRow({ routine }: { routine: ReviewRoutineWire }) {
  const hasDigest = typeof routine.current_digest === "string" && routine.current_digest !== ""
  const fenced = routine.in_candidate && hasDigest
  // Two different reasons a row is outside the fence, and they are not the
  // same news. A routine the candidate no longer calls is outside it because
  // it is being dropped — that is the point. A routine the candidate DOES call
  // is outside it only because its hash could not be read, which is a gap.
  const gap = fenced ? null : !routine.in_candidate ? (
    <p className="mt-1">
      The candidate does not call this routine; it is listed because the live publication calls it. It is not part of the publish check, and publishing this candidate stops
      calling it.
    </p>
  ) : (
    <p className="mt-1">
      This dependency is not covered by the publish check: no current hash was recorded for it, so publishing will not be refused if this routine changes between now and
      the publication.
    </p>
  )
  if (routine.state === "changed") {
    return (
      <li className="rounded-md border border-dashed p-3 text-sm" data-routine-state="changed">
        <strong>Routine definition changed: {routine.routine}</strong>
        <p className="mt-1">The application does not pin the scripts a routine calls. Review this dependency before publishing — publishing pins the interface and the declaration, not the routine&apos;s implementation.</p>
        {gap}
      </li>
    )
  }
  if (routine.state === "unknown") {
    return (
      <li className="rounded-md border border-dashed p-3 text-sm" data-routine-state="unknown">
        <strong>Routine {routine.routine}: earlier definition hash unavailable</strong>
        <p className="mt-1">The hash recorded for the live publication could not be read, so this dependency could not be compared. Treat it as not reviewed.</p>
        {gap}
      </li>
    )
  }
  return (
    <li className="text-sm" data-routine-state="unchanged">
      Routine {routine.routine}: definition unchanged since the live publication. The scripts it calls are still not pinned by this application.
      {gap}
    </li>
  )
}

function SourceChanges({ diff, initial }: { diff: SourceDiff; initial: boolean }) {
  const [selected, setSelected] = useState(0)
  const files = diff.files
  // `truncated` means a body was CUT. A binary file is not cut — nothing of it
  // was ever renderable — so `compareSources` leaves it `truncated: false`.
  // A screen that reads only `truncated` therefore shows a changed binary as
  // fully reviewed, which is the exact false "everything reviewed" this
  // section exists to avoid. Binary is counted and stated on its own.
  const binaryCount = files.filter(file => file.binary).length
  // A file list can be replaced under a stale index by a realtime refetch.
  const active: SourceFileChange | undefined = files[selected] ?? files[0]

  useEffect(() => {
    setSelected(0)
  }, [files])

  if (files.length === 0) {
    return <p className="mt-2 text-sm">No source file differs between the live publication and this candidate.</p>
  }

  return (
    <div className="mt-2 flex flex-col gap-3">
      {initial && (
        <p className="text-sm">
          All application source is new. Review the full candidate; there is no earlier application source to compare it with.
        </p>
      )}
      <p className="text-sm text-muted-foreground">
        {diff.filesAdded} added · {diff.filesModified} modified · {diff.filesRemoved} removed. A rename is reported as a removal plus an addition, because guessing at a rename
        hides a rewrite inside what reads as a move.
      </p>
      {binaryCount > 0 && (
        <p role="note" className="rounded-md border border-dashed p-3 text-sm">
          {binaryCount} changed file{binaryCount === 1 ? " is" : "s are"} binary or undecodable. {binaryCount === 1 ? "Its" : "Their"} bytes were not shown here and have not
          been reviewed — the change is described, never rendered and never executed.
        </p>
      )}
      {diff.truncated && (
        <p role="note" className="rounded-md border border-dashed p-3 text-sm">
          This comparison is shortened: not every change is rendered here. Reviewing what is shown is not the same as reviewing the whole change.
        </p>
      )}
      {/* Stacks at 360px; two columns from `lg`. `min-w-0` on both is what
          keeps a long diff line inside its own scroller instead of widening
          the document. */}
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-[minmax(0,16rem)_minmax(0,1fr)]">
        <ul className="flex min-w-0 flex-col gap-1" aria-label="Changed files">
          {files.map((file, index) => (
            <li key={file.path}>
              <button
                type="button"
                aria-current={file === active ? "true" : undefined}
                className="flex min-h-11 w-full items-center gap-2 rounded-md border px-2 py-1 text-left text-sm aria-[current]:border-primary"
                onClick={() => setSelected(index)}
              >
                <span className="font-mono text-xs" aria-hidden="true">
                  {STATUS_MARK[file.status]}
                </span>
                <span className="sr-only">{STATUS_WORD[file.status]}</span>
                <span className="min-w-0 flex-1 truncate">{file.path}</span>
                {file.binary ? (
                  <span className="whitespace-nowrap text-xs">Binary · not shown</span>
                ) : (
                  <span className="whitespace-nowrap font-mono text-xs">
                    +{file.added} −{file.removed}
                  </span>
                )}
              </button>
            </li>
          ))}
        </ul>
        {active && <FileDiff file={active} />}
      </div>
    </div>
  )
}

/**
 * One file at a time. Rendering every file's body at once is what stalls the
 * tab on a 256-file project; the list above is cheap and the body below is
 * bounded by `SOURCE_DIFF_LIMITS.linesPerFile`.
 *
 * Text only. Never `dangerouslySetInnerHTML`: source and definition content
 * are attacker-influenced input on this screen by construction.
 */
function FileDiff({ file }: { file: SourceFileChange }) {
  const shown = file.lines.slice(0, SOURCE_DIFF_LIMITS.linesPerFile)
  const cut = file.truncated || file.lines.length > shown.length
  return (
    <div className="min-w-0">
      <p className="text-sm font-semibold">
        <span className="mr-2 rounded border px-1 text-xs uppercase">{STATUS_WORD[file.status]}</span>
        {file.path}
      </p>
      {file.binary ? (
        <p className="mt-2 text-sm">
          {STATUS_WORD[file.status]}: binary or undecodable file. Its bytes were not shown and have not been reviewed. Line counts do not apply, and the content is never
          rendered and never executed.
        </p>
      ) : (
        <>
          <pre className="mt-2 max-h-[28rem] overflow-auto rounded bg-muted p-2 text-xs leading-5">
            {shown.map((line, index) => (
              <span key={index} className="block whitespace-pre">
                <span aria-hidden="true">{line.kind === "add" ? "+" : line.kind === "del" ? "−" : line.kind === "hunk" ? "@" : " "}</span>
                {line.text}
              </span>
            ))}
          </pre>
          {cut && (
            <p role="note" className="mt-2 text-sm">
              This file&apos;s diff is shortened after {shown.length} lines. The rest is not shown; it has not been reviewed.
            </p>
          )}
        </>
      )}
    </div>
  )
}
