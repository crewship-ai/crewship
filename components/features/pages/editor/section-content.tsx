"use client"

/**
 * Content — the first of the editor's four sections (PRD
 * `docs/prd/pages-settings-editor-review-proposal-2026-09-10.md` §3).
 *
 * The P0 row reads *"Content — panelový obsah, aplikační kontrola, metadata"*:
 * all three, on every Page that has them. So this section is not a switch
 * between two screens. Every Page shows its own identity, facts and panels;
 * an application Page shows the review of a submitted change ABOVE them.
 *
 * Getting that wrong once cost real function (F1): routing an application
 * Page straight into the review left it with no way anywhere in the product to
 * be renamed, described, or opened as a document, because the review surface
 * has none of those.
 *
 * Whether the review appears at all is decided by the SERVER's review
 * snapshot, never by `has_application` (F2). §5 rule 6: a candidate that does
 * not exist, or one identical to what is live, does not open the review — the
 * reader goes straight to Content, and is told in one line why there is
 * nothing to review.
 *
 * The independent review's U02 is the other half of the acceptance test — *the
 * ordinary panel Page must be a complete first-class case, not Operations Lab
 * with features switched off* — so an ordinary Page renders no application
 * tab, no publication checklist, no permanently disabled Publish and no
 * greyed-out application heading. There is nothing here to switch off, because
 * none of it is built for that Page in the first place.
 *
 * Two properties worth stating, because both are easy to undo:
 *
 *  · **Metadata and the document are separate powers.** `PATCH /pages/{slug}`
 *    carries no panel list, so a Page holding a panel this viewer may not see
 *    can still be renamed — `mayEditDocument` closes the document editor and
 *    nothing else. Hiding the panel list along with it was V01.
 *
 *  · **Nothing is fetched to draw a panel list.** The panels are already on
 *    `props.page`. An ordinary Page issues no request at all from here: the
 *    review snapshot is read only when the Page has an application, and the
 *    application-hosting probe only when somebody opens that offer.
 *
 * Padding and the readable measure belong to the shell, which supplies them
 * for all four sections. A second `max-w-*` here would nest two measures and
 * make this section narrower than its neighbours for no reason.
 */

import * as React from "react"
import { Copy, Plus } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { Spinner } from "@/components/ui/spinner"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { apiErrorMessage } from "@/lib/api-error"
import { useQuery } from "@tanstack/react-query"
import { ApiMutationError, useApiMutation } from "@/hooks/use-api-mutation"
import { SAVE_EFFECT_NOTE, type ReviewSnapshotWire } from "@/lib/pages/editor-contract"
import { pagesKeys, toPanelView, type WirePanel } from "@/hooks/use-pages"
import { pageQueryString, type WirePageDetail } from "@/hooks/use-page-grants"
import { usePageReview } from "@/hooks/use-page-review"
import { PAGE_STATE_META } from "@/components/features/pages/page-state"
import { PageEditor } from "@/components/features/pages/page-editor"
// `PageFactsCard` is the derived-facts block (owner, panels, created, spec
// last changed) lifted out of the settings surface. It is exported from
// page-settings.tsx by another stream; this is the seam.
import { PageFactsCard } from "@/components/features/pages/page-settings"
import type { EditorSectionProps } from "@/components/features/pages/editor/section-props"

// Content on an application Page opens the review of the agent's change.
// S6 owns that surface; import it lazily so the ordinary panel Page never
// pays for it.
const EditorApplicationReview = React.lazy(async () => {
  const mod = await import("@/components/features/pages/editor/application-review")
  return { default: mod.EditorApplicationReview }
})

export function EditorContentSection(props: EditorSectionProps) {
  if (!props.capabilities.loaded || props.page == null) {
    return (
      <p role="status" className="type-page-value text-muted-foreground">
        This Page could not be read, so its content cannot be shown. This is not the same as a Page with
        nothing on it.
      </p>
    )
  }
  // `hasApplication` decides which Content this is. It never decides whether
  // the Page's own identity and panels are reachable — that was F1, and it
  // left an application Page with no way anywhere in the product to rename
  // itself or open its document.
  if (!props.capabilities.hasApplication) return <PanelPageContent {...props} />
  return <ApplicationPageContent {...props} />
}

// ── The ordinary panel Page ────────────────────────────────────────────────

function PanelPageContent(props: EditorSectionProps) {
  return (
    <div className="flex flex-col gap-5">
      <div>
        <h3 className="text-heading font-medium">Page content</h3>
        <p className="type-page-meta text-muted-foreground">
          This Page shows panels. Changes saved here update the live Page.
        </p>
      </div>

      <PageOwnContent {...props} onDirtyChange={props.onDirtyChange} />

      {/* Only where there is no application yet. Offering to add a second one
          to a Page that already has one would be nonsense. */}
      <AddApplicationOffer workspaceId={props.workspaceId} slug={props.slug} />
    </div>
  )
}

// ── The application Page ───────────────────────────────────────────────────

/**
 * What the authorized snapshot says there is to do here.
 *
 * The decision is taken from the SNAPSHOT and never from `has_application`
 * (F2). §5 rule 6 of the independent review is explicit: a candidate that does
 * not exist, or one identical to the live version, does not open the review
 * screen — it goes straight to Content. Routing on `has_application` opened an
 * empty review over a Page whose own content was then unreachable, which is
 * the same defect twice.
 */
type ReviewGate =
  | { kind: "loading" }
  | { kind: "review" }
  | { kind: "nothing"; sentence: string }
  | { kind: "unreadable"; reason: string }

function reviewGateOf(
  snapshot: ReviewSnapshotWire | undefined,
  pending: boolean,
  error: Error | null,
): ReviewGate {
  if (pending) return { kind: "loading" }
  if (error) return { kind: "unreadable", reason: error.message }
  if (!snapshot) {
    return { kind: "unreadable", reason: "The server sent no review of this application." }
  }
  if (!snapshot.candidate) {
    return {
      kind: "nothing",
      sentence: snapshot.baseline.published
        ? `This Page runs a custom application, published as version ${snapshot.baseline.publication_version}. No agent has submitted a change, so there is nothing to review.`
        : "This Page has a custom application, but nothing is published and no change has been submitted for review.",
    }
  }
  // The server's own words when it says the candidate adds nothing — it is the
  // one that compared the digests, and "identical" is its verdict, not ours.
  const identical = snapshot.blockers.find((b) => b.code === "candidate_matches_live")
  if (identical) {
    return {
      kind: "nothing",
      sentence:
        identical.message ||
        "The submitted candidate is identical to what is already live, so there is nothing to review.",
    }
  }
  return { kind: "review" }
}

function ApplicationPageContent(props: EditorSectionProps) {
  const { workspaceId, slug, onDirtyChange } = props
  const review = usePageReview(workspaceId, slug, true)
  const gate = reviewGateOf(
    review.snapshot.data,
    review.snapshot.isPending,
    (review.snapshot.error as Error | null) ?? null,
  )

  // Two children can hold work on this screen, and the shell asks the SECTION,
  // not each of them. Aggregating here is what keeps the review's "I hold
  // nothing" from clearing the guard over a half-typed rename underneath it.
  const parts = React.useRef({ review: false, content: false })
  const report = React.useCallback(
    (which: "review" | "content") => (dirty: boolean) => {
      parts.current[which] = dirty
      onDirtyChange(parts.current.review || parts.current.content)
    },
    [onDirtyChange],
  )
  const reportReview = React.useMemo(() => report("review"), [report])
  const reportContent = React.useMemo(() => report("content"), [report])

  return (
    <div className="flex flex-col gap-6">
      {gate.kind === "loading" && (
        <p role="status" className="flex items-center gap-2 type-page-value text-muted-foreground">
          <Spinner className="h-3.5 w-3.5" />
          Checking whether an agent has submitted a change to this Page…
        </p>
      )}

      {gate.kind === "unreadable" && (
        <p role="alert" data-slot="review-unreadable" className="type-page-value text-warn">
          This Page has a custom application, but its review could not be read, so this screen cannot say
          whether a change is waiting: {gate.reason} The Page&apos;s own content is below and unaffected.
        </p>
      )}

      {gate.kind === "nothing" && (
        <p role="status" data-slot="nothing-to-review" className="type-page-value text-muted-foreground">
          {gate.sentence}
        </p>
      )}

      {gate.kind === "review" && (
        <React.Suspense
          fallback={
            <p role="status" className="flex items-center gap-2 type-page-value text-muted-foreground">
              <Spinner className="h-3.5 w-3.5" />
              Opening the review of the submitted change…
            </p>
          }
        >
          <EditorApplicationReview {...props} onDirtyChange={reportReview} />
        </React.Suspense>
      )}

      <section aria-labelledby="page-own-content-heading" className="flex flex-col gap-5">
        <div className={cn(gate.kind === "review" && "border-t border-border/60 pt-6")}>
          <h3 id="page-own-content-heading" className="text-heading font-medium">
            {gate.kind === "review" ? "This Page itself" : "Page content"}
          </h3>
          <p className="type-page-meta text-muted-foreground">
            {/* Two definitions live on an application Page and they are not the
                same thing. Saying which one this half writes is the whole of §4. */}
            The Page&apos;s name, description and panels. Saved here they change the live Page — they are
            not part of an application publication.
          </p>
        </div>

        <PageOwnContent {...props} onDirtyChange={reportContent} />
      </section>
    </div>
  )
}

/** The Page's own identity and panels. One copy, mounted by both Contents. */
function PageOwnContent({
  workspaceId,
  slug,
  page,
  capabilities,
  onNavigate,
  onDirtyChange,
}: EditorSectionProps) {
  return (
    <>
      <PageIdentityCard
        workspaceId={workspaceId}
        slug={slug}
        page={page}
        mayEdit={capabilities.mayEditMetadata}
        onDirtyChange={onDirtyChange}
      />

      <PanelListCard
        workspaceId={workspaceId}
        slug={slug}
        page={page}
        mayEditDocument={capabilities.mayEditDocument}
        documentRefusal={capabilities.documentRefusal}
        onNavigate={onNavigate}
      />
    </>
  )
}

// ── 1. Identity ────────────────────────────────────────────────────────────

interface Metadata {
  name: string
  description: string
}

function metadataOf(page: WirePageDetail | null): Metadata {
  return { name: page?.name?.trim() ?? "", description: page?.description ?? "" }
}

function sameMetadata(a: Metadata, b: Metadata): boolean {
  return a.name === b.name && a.description === b.description
}

function PageIdentityCard({
  workspaceId,
  slug,
  page,
  mayEdit,
  onDirtyChange,
}: {
  workspaceId: string
  slug: string
  page: WirePageDetail | null
  mayEdit: boolean
  onDirtyChange: (dirty: boolean) => void
}) {
  const server = metadataOf(page)

  // Three values, not two. `form` is what is typed, `baseline` is what the
  // server last accepted, and the difference between them is the dirty flag.
  // A refused save moves NEITHER (#1563 rule 3: what was typed is what a retry
  // needs), which is the whole reason the accepted values are tracked here
  // rather than read back off `page` — that record does not change on a 403.
  const [form, setForm] = React.useState<Metadata>(server)
  const [baseline, setBaseline] = React.useState<Metadata>(server)
  const [refusal, setRefusal] = React.useState<string | null>(null)
  const [saved, setSaved] = React.useState(false)

  const dirty = !sameMetadata(form, baseline)
  const dirtyRef = React.useRef(dirty)
  dirtyRef.current = dirty

  // A later read of the same Page (a realtime invalidation, somebody else's
  // save) adopts the server's values only while nothing is typed here. Doing
  // it unconditionally would delete an edit in progress.
  React.useEffect(() => {
    if (dirtyRef.current) return
    setForm((cur) => (sameMetadata(cur, server) ? cur : server))
    setBaseline((cur) => (sameMetadata(cur, server) ? cur : server))
    // The two strings, not the object: `metadataOf` builds a fresh one each render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [server.name, server.description])

  // The shell guards Page switches, section switches and Back on this. It is
  // reported from an effect rather than from the change handler so the flag
  // can never disagree with what is on screen.
  React.useEffect(() => {
    onDirtyChange(dirty)
  }, [dirty, onDirtyChange])
  React.useEffect(() => () => onDirtyChange(false), [onDirtyChange])

  const save = useApiMutation<Metadata>({
    // PATCH /api/v1/pages/{slug} — `internal/api/pages_handler.go:776` (`Update`),
    // whose body is `pageWriteRequest` (:331). Every field is a pointer: an
    // omitted `panels` leaves the stored panel list, its gates and its
    // automations exactly as they are (:826), which is what makes renaming a
    // Page safe on a Page this viewer may not fully see.
    request: (v) => ({
      input: `/api/v1/pages/${encodeURIComponent(slug)}${pageQueryString(workspaceId)}`,
      init: {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: v.name, description: v.description }),
      },
    }),
    invalidateKeys: [pagesKeys.detail(workspaceId, slug), pagesKeys.list(workspaceId)],
    onOk: (_data, v) => {
      setBaseline(v)
      setForm(v)
      setRefusal(null)
      setSaved(true)
    },
    onAlreadyRunning: (outcome) => setRefusal(outcome.message),
    onError: (err) => {
      // Rule 2 and rule 4: the server's own sentence for a refusal, a
      // different one for a transport failure — and the typed values stay.
      setRefusal(
        err instanceof ApiMutationError
          ? err.message
          : err instanceof Error
            ? `Could not reach the server: ${err.message}`
            : "Could not reach the server",
      )
      setSaved(false)
    },
  })

  const nameEmpty = form.name.trim() === ""
  const submittable = mayEdit && dirty && !nameEmpty && !save.isPending

  const submit = (event: React.FormEvent) => {
    event.preventDefault()
    if (!submittable) return
    setSaved(false)
    save.mutate({ name: form.name.trim(), description: form.description })
  }

  const change = (next: Partial<Metadata>) => {
    setSaved(false)
    if (refusal) setRefusal(null)
    setForm((cur) => ({ ...cur, ...next }))
  }

  return (
    <form
      onSubmit={submit}
      data-slot="page-identity"
      // `touch-form` is the house rule for 44px targets under a coarse
      // pointer (app/globals.css) — the pointer decides, not the width.
      className="touch-form flex flex-col gap-4 rounded-lg border border-border/60 bg-card p-4"
    >
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="page-name">Page name</Label>
        <Input
          id="page-name"
          value={form.name}
          disabled={!mayEdit}
          onChange={(e) => change({ name: e.target.value })}
        />
        {nameEmpty && (
          <p className="type-page-meta text-muted-foreground">
            A Page needs a name. The server refuses a document without one.
          </p>
        )}
      </div>

      <div className="flex flex-col gap-1.5">
        <Label htmlFor="page-description">Description</Label>
        <Textarea
          id="page-description"
          rows={2}
          value={form.description}
          disabled={!mayEdit}
          onChange={(e) => change({ description: e.target.value })}
        />
      </div>

      <PageAddress slug={slug} />

      {/* Derived facts — owner, panels, created, spec last changed. Read-only
          by nature: none of them is something this form writes. */}
      <PageFactsCard slug={slug} page={page} />

      {!mayEdit && (
        <p role="note" className="type-page-value text-muted-foreground">
          You may not change this Page&apos;s name or description.
        </p>
      )}

      {refusal && (
        <p role="alert" className="type-page-value text-destructive">
          {refusal}
        </p>
      )}
      {saved && !dirty && (
        <p role="status" className="type-page-value text-success">
          Saved. The live Page now shows this name and description.
        </p>
      )}

      <div className="flex flex-wrap items-center justify-between gap-3">
        {/* The save's effect, stated at the control, from the one place the
            editor keeps that vocabulary. There is no global Save here and no
            autosave: every control says which of the five effects it is. */}
        <p className="type-page-meta min-w-0 text-muted-foreground">
          {SAVE_EFFECT_NOTE["live-definition"]}
        </p>
        <Button type="submit" size="sm" className="coarse:min-h-11" disabled={!submittable}>
          {save.isPending && <Spinner className="h-3.5 w-3.5" />}
          Save changes
        </Button>
      </div>
    </form>
  )
}

/** The address, as a fact. It is the Page's identity and its producers' target,
 *  so it is shown and copyable and never editable — the server refuses a slug
 *  change through PATCH for exactly that reason (`pages_handler.go:806`). */
function PageAddress({ slug }: { slug: string }) {
  const address = `/pages/${slug}`
  const [copied, setCopied] = React.useState<"idle" | "done" | "refused">("idle")

  const copy = async () => {
    // Three states, not two: a clipboard write can be refused (insecure
    // origin, denied permission) and saying "Copied" anyway is a lie the
    // reader only discovers when they paste.
    try {
      await navigator.clipboard.writeText(address)
      setCopied("done")
    } catch {
      setCopied("refused")
    }
  }

  return (
    <div className="flex flex-wrap items-center justify-between gap-2 border-t border-border/40 pt-3">
      <div className="min-w-0">
        <span className="type-page-label text-muted-foreground-soft">Address</span>
        <p className="type-page-stamp overflow-x-auto whitespace-nowrap text-foreground/85">{address}</p>
      </div>
      <div className="flex items-center gap-2">
        <span aria-live="polite" className="type-page-meta text-muted-foreground">
          {copied === "done" ? "Copied" : copied === "refused" ? "This browser refused the clipboard" : ""}
        </span>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="coarse:min-h-11"
          onClick={() => void copy()}
        >
          <Copy className="h-3.5 w-3.5" aria-hidden />
          Copy address
        </Button>
      </div>
    </div>
  )
}

// ── 2. Panels ──────────────────────────────────────────────────────────────

function panelsOf(page: WirePageDetail | null): WirePanel[] {
  return Array.isArray(page?.panels) ? page.panels : []
}

function PanelListCard({
  workspaceId,
  slug,
  page,
  mayEditDocument,
  documentRefusal,
  onNavigate,
}: {
  workspaceId: string
  slug: string
  page: WirePageDetail | null
  mayEditDocument: boolean
  documentRefusal: string | null
  onNavigate: (section: "content" | "data" | "access" | "history") => void
}) {
  const [editing, setEditing] = React.useState(false)
  const panels = panelsOf(page)

  return (
    <section
      aria-labelledby="page-panels-heading"
      data-slot="page-panels"
      className="flex flex-col gap-3 rounded-lg border border-border/60 bg-card p-4"
    >
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h4 id="page-panels-heading" className="text-body font-medium">
          Panels
        </h4>
        <span className="type-page-meta text-muted-foreground">
          {panels.length} {panels.length === 1 ? "panel" : "panels"}
        </span>
      </div>

      {panels.length === 0 ? (
        <p className="type-page-value text-muted-foreground">
          This Page declares no panels yet. Add one in the document, or push a first payload with{" "}
          <code className="type-page-stamp">crewship page set {slug}/&lt;panel&gt; --data -</code>.
        </p>
      ) : (
        <ul className="flex flex-col">
          {panels.map((raw, index) => (
            <PanelRow
              key={raw.id ?? raw.panel_id ?? index}
              raw={raw}
              index={index}
              mayEditDocument={mayEditDocument}
              onEdit={() => setEditing(true)}
            />
          ))}
        </ul>
      )}

      {/* The refusal sits WITH the control it refuses, and the list stays.
          A Page carrying a panel this viewer may not see cannot be saved as a
          document — the save would delete that panel — and that says nothing
          about reading the list or renaming the Page. */}
      {documentRefusal && !mayEditDocument && (
        <p role="note" data-slot="document-refusal" className="type-page-value text-warn">
          {documentRefusal}
        </p>
      )}

      <div className="flex flex-wrap items-center justify-between gap-2 border-t border-border/40 pt-3">
        <p className="type-page-meta min-w-0 text-muted-foreground">
          Panels are edited as one document. Where each panel&apos;s data comes from is in{" "}
          <button
            type="button"
            className="underline underline-offset-2 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            onClick={() => onNavigate("data")}
          >
            Data &amp; actions
          </button>
          .
        </p>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="coarse:min-h-11"
          disabled={!mayEditDocument}
          onClick={() => setEditing(true)}
        >
          Edit document
        </Button>
      </div>

      {editing && (
        <PageEditor
          workspaceId={workspaceId}
          mode="edit"
          page={page}
          onClose={() => setEditing(false)}
          onSaved={() => setEditing(false)}
        />
      )}
    </section>
  )
}

function PanelRow({
  raw,
  index,
  mayEditDocument,
  onEdit,
}: {
  raw: WirePanel
  index: number
  mayEditDocument: boolean
  onEdit: () => void
}) {
  const view = toPanelView(raw, index)
  const state = view.state ? PAGE_STATE_META[view.state] : null
  const StateIcon = state?.icon
  // The declared producer first, then whoever last actually wrote. They are
  // different claims — one is the spec, the other is the receipt — and the
  // declaration is what an editor is looking at.
  const producer = view.producer ?? view.snapshot.provenance?.producer ?? null

  if (view.spec.sealed) {
    return (
      <li
        data-slot="page-panel-row"
        data-sealed="true"
        className="flex flex-col gap-1 border-b border-border/40 py-2.5 last:border-b-0"
      >
        <span className="type-page-value font-medium">{view.spec.id}</span>
        <span className="type-page-meta text-muted-foreground">
          Sealed · owned by {view.spec.owner_crew_name ?? "another crew"} · not shown to you
        </span>
      </li>
    )
  }

  return (
    <li
      data-slot="page-panel-row"
      data-panel={view.spec.id}
      className="flex flex-wrap items-start justify-between gap-2 border-b border-border/40 py-2.5 last:border-b-0"
    >
      <div className="min-w-0 flex-1">
        <span className="type-page-value font-medium break-words">
          {view.spec.title ?? view.spec.id}
        </span>
        <p className="type-page-meta text-muted-foreground break-words">
          <span className="type-page-stamp">{view.spec.schema || "no schema declared"}</span>
          {" · "}
          {producer ? (
            <>Producer {producer}</>
          ) : (
            // Never a dash and never a zero here: "no producer declared" is a
            // fact about the spec, not a measurement (§9b.4).
            <>No producer declared</>
          )}
        </p>
      </div>
      <div className="flex shrink-0 items-center gap-3">
        {state ? (
          <span className={cn("inline-flex items-center gap-1.5 type-page-meta", state.tone)}>
            {StateIcon && <StateIcon className="h-3.5 w-3.5" aria-hidden />}
            {state.label}
          </span>
        ) : (
          <span className="type-page-meta text-muted-foreground">State unknown</span>
        )}
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="coarse:min-h-11"
          disabled={!mayEditDocument}
          onClick={onEdit}
        >
          Edit
        </Button>
      </div>
    </li>
  )
}

// ── 3. The offer of a custom application ───────────────────────────────────

/**
 * What this installation would let somebody do, in the server's own words.
 *
 * Read from `GET /pages/{slug}/project/preview`, which answers the question
 * exactly: it refuses with 403 before anything else when the caller may not
 * edit the Page (`pages_project.go:56`), with 503 when project storage
 * (`:59`) or the build worker (`pages_build.go:183`) is not configured or the
 * preview origin does not isolate this host (`:207`), and only then with a
 * 404 meaning "configured, and this Page has no draft yet".
 *
 * It is issued when the offer is opened, never on mount: an ordinary panel
 * Page must not pay a request to draw a panel list.
 */
type OfferState =
  | { kind: "available" }
  | { kind: "exists" }
  | { kind: "refused"; reason: string }
  | { kind: "unconfigured"; reason: string }
  | { kind: "unknown"; reason: string }

async function readOffer(
  slug: string,
  workspaceId: string,
  signal: AbortSignal | undefined,
): Promise<OfferState> {
  const res = await apiFetch(
    `/api/v1/pages/${encodeURIComponent(slug)}/project/preview${pageQueryString(workspaceId)}`,
    { signal },
  )
  if (res.ok) return { kind: "exists" }
  if (res.status === 404) return { kind: "available" }
  const body = await res.json().catch(() => null)
  const message = apiErrorMessage(body, `The server answered ${res.status}.`)
  if (res.status === 403) return { kind: "refused", reason: message }
  if (res.status === 503) return { kind: "unconfigured", reason: message }
  return { kind: "unknown", reason: message }
}

function AddApplicationOffer({ workspaceId, slug }: { workspaceId: string; slug: string }) {
  const [open, setOpen] = React.useState(false)

  const query = useQuery({
    queryKey: ["page-application-offer", workspaceId, { slug }],
    queryFn: ({ signal }) => readOffer(slug, workspaceId, signal),
    enabled: open,
    retry: false,
    gcTime: 0,
  })

  return (
    <section
      aria-labelledby="add-application-heading"
      data-slot="add-application"
      // Deliberately not a card and deliberately last: this is an optional
      // extra capability, not a missing part of the Page (independent review
      // §6). Giving it the panel list's visual weight is what turns an
      // ordinary Page into a demo with features switched off.
      className="rounded-md border border-dashed border-border/60 px-4 py-3"
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h4 id="add-application-heading" className="type-page-value font-medium text-muted-foreground">
            Need a custom interface?
          </h4>
          <p className="type-page-meta text-muted-foreground">
            Panels are a complete option. A custom application is an extra, not a missing half of this Page.
          </p>
        </div>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="coarse:min-h-11"
          aria-expanded={open}
          onClick={() => setOpen((v) => !v)}
        >
          <Plus className="h-3.5 w-3.5" aria-hidden />
          Add a custom application
        </Button>
      </div>

      {open && (
        <div className="mt-3 flex flex-col gap-2 border-t border-border/40 pt-3">
          {/* No one-click promise. Today this needs an authorized agent's MCP
              workflow, a build profile and a human publication, and saying
              otherwise would send somebody looking for a button that does not
              exist. */}
          <p className="type-page-value text-muted-foreground">
            This is not created from here in one click. An authorized agent writes the application&apos;s
            source over this Page&apos;s data through its MCP workflow, a build profile compiles that
            source into a preview, and a human reviews the build and publishes it. The panels stay either
            way — publishing an application does not delete them.
          </p>

          {query.isPending && (
            <p role="status" className="flex items-center gap-2 type-page-meta text-muted-foreground">
              <Spinner className="h-3.5 w-3.5" />
              Checking what this installation can host…
            </p>
          )}
          {query.isError && (
            <p role="alert" className="type-page-meta text-destructive">
              Could not check whether this installation can host an application:{" "}
              {(query.error as Error).message}
            </p>
          )}
          {query.data && <OfferAnswer state={query.data} />}
        </div>
      )}
    </section>
  )
}

function OfferAnswer({ state }: { state: OfferState }) {
  switch (state.kind) {
    case "available":
      return (
        <p role="status" data-slot="offer-state" className="type-page-meta text-muted-foreground">
          This installation can host one, and this Page has no application draft yet. Ask an agent with
          edit rights on this Page to write one.
        </p>
      )
    case "exists":
      return (
        <p role="status" data-slot="offer-state" className="type-page-meta text-muted-foreground">
          This Page already has an application draft. It appears here for review once its author submits a
          candidate.
        </p>
      )
    case "refused":
      // The concrete limitation, in the server's words rather than "you can't".
      return (
        <p role="note" data-slot="offer-state" className="type-page-meta text-warn">
          {state.reason}
        </p>
      )
    case "unconfigured":
      return (
        <p role="note" data-slot="offer-state" className="type-page-meta text-warn">
          {state.reason} Until an administrator configures the build worker and a separate runtime origin,
          no Page in this workspace can carry a custom application.
        </p>
      )
    case "unknown":
      return (
        <p role="note" data-slot="offer-state" className="type-page-meta text-muted-foreground">
          {state.reason}
        </p>
      )
  }
}
