/**
 * The Pages editor's shared vocabulary.
 *
 * Five streams build the editor against this file and nothing else, so it is
 * deliberately types-and-constants only: no React, no fetch, no DOM. If a
 * change here would break a section, it breaks it at `tsc` rather than in the
 * browser, which is the whole reason the contract is written down before the
 * screens are.
 *
 * Three of these vocabularies exist because the design review found the
 * previous shape of them untrue rather than merely inconvenient:
 *
 *   - `PageCapabilities` is per-section, because the old document-wide
 *     `canEdit` (a page with zero sealed panels) hid Access and History from
 *     someone the server would happily have served (V01).
 *   - `DefinitionChange` is *derived*, never authored. An agent writing its own
 *     change summary is marketing, not review, and the reviewer would have no
 *     way to tell the two apart.
 *   - `ReviewSnapshot` carries the digests the human actually reviewed, so the
 *     publish call can be fenced against them. Refetching in the UI does not
 *     give that property, which is what V04 established.
 */

// ── Navigation ───────────────────────────────────────────────────────────────

/**
 * Four sections. `data` is spelled short in the URL and long in the product;
 * `&section=data%20%26%20actions` is not a URL anybody pastes into Slack.
 */
export type EditorSection = "content" | "data" | "access" | "history"

export const EDITOR_SECTIONS: readonly EditorSection[] = ["content", "data", "access", "history"]

export const EDITOR_SECTION_LABEL: Readonly<Record<EditorSection, string>> = {
  content: "Content",
  data: "Data & actions",
  access: "Access",
  history: "History",
}

/**
 * What each section is for, in one line, for the mobile picker and for the
 * empty states. Product copy is English throughout the editor.
 */
export const EDITOR_SECTION_HINT: Readonly<Record<EditorSection, string>> = {
  content: "This Page's name, its panels, and any custom application awaiting review",
  data: "Where each panel's data comes from, and the actions it offers",
  access: "Who reads this Page, who may send data to it, and public links",
  history: "Panel versions, application source revisions and publications",
}

export type EditorMode = "view" | "edit"

/**
 * The review screen opens the candidate's build in a workspace of its own
 * rather than squeezing a form, a diff and a live iframe into three narrow
 * columns. It is a *third* place, not a section, so it gets its own URL key.
 */
export type EditorPane = "section" | "preview"

export interface EditorRoute {
  /** null on `/pages` — the overview, where the editor cannot be open. */
  readonly slug: string | null
  readonly mode: EditorMode
  readonly section: EditorSection
  readonly pane: EditorPane
}

export const DEFAULT_EDITOR_SECTION: EditorSection = "content"

export function isEditorSection(value: unknown): value is EditorSection {
  return typeof value === "string" && (EDITOR_SECTIONS as readonly string[]).includes(value)
}

/**
 * Parse the editor's state out of a location. Unknown values fall back rather
 * than 404, because a section name is a UI detail and a stale bookmark should
 * still land on the right Page.
 */
export function readEditorRoute(pathname: string, search: string): EditorRoute {
  const match = /^\/pages\/([^/]+)\/?$/.exec(pathname)
  // `decodeURIComponent` throws on a malformed percent sequence — `/pages/%`
  // is a URL a browser will happily be pointed at, and an unguarded throw
  // takes the whole route parse down with it.
  //
  // The undecodable segment is kept rather than dropped. Returning the
  // overview instead would leave the address saying `/pages/%` while the
  // screen shows the index — the address and the screen disagreeing is the
  // failure this module exists to prevent. Kept, it addresses a Page that
  // does not exist, the detail read answers 404, and the surface says so
  // by name. A wrong address should say it is wrong, not quietly become a
  // different page.
  let slug: string | null = null
  if (match) {
    try {
      slug = decodeURIComponent(match[1])
    } catch {
      slug = match[1]
    }
  }
  const params = new URLSearchParams(search)
  const section = isEditorSection(params.get("section")) ? (params.get("section") as EditorSection) : DEFAULT_EDITOR_SECTION
  // Edit mode without a page is not a state; it would render a header for
  // nothing. The overview wins.
  const mode: EditorMode = slug !== null && params.get("mode") === "edit" ? "edit" : "view"
  // The preview is a workspace inside Content, so `pane=preview` anywhere
  // else is an address claiming a state the screen does not have.
  const pane: EditorPane =
    mode === "edit" && section === "content" && params.get("pane") === "preview" ? "preview" : "section"
  return { slug, mode, section, pane }
}

/**
 * The address for a route. Only non-default keys are written, so viewing a
 * Page keeps the plain `/pages/<slug>` link people already share.
 */
export function editorRouteHref(route: EditorRoute, currentSearch = ""): string {
  if (route.slug === null) return "/pages"
  const path = `/pages/${encodeURIComponent(route.slug)}`
  // Everything the editor does not own is carried through. The panel tab bar
  // writes `?tab=` with its own replaceState, and rebuilding the query from
  // scratch dropped it — so opening a Page on its third tab and clicking Edit
  // came back to the first one.
  const params = new URLSearchParams(currentSearch)
  for (const key of ["mode", "section", "pane"]) params.delete(key)
  if (route.mode === "edit") {
    params.set("mode", "edit")
    if (route.section !== DEFAULT_EDITOR_SECTION) params.set("section", route.section)
    if (route.pane === "preview") params.set("pane", "preview")
  }
  const query = params.toString()
  return query === "" ? path : `${path}?${query}`
}

// ── Capabilities ─────────────────────────────────────────────────────────────

/**
 * What this viewer may do, section by section.
 *
 * These are *optimistic*: the server is the authority on every one of them and
 * re-checks it on the request. Their job is to avoid offering a control that
 * will certainly fail, and — more importantly — to stop one section's refusal
 * from hiding the other three. A refusal that does arrive is rendered where the
 * action was, never swallowed.
 */
export interface PageCapabilities {
  /** The Page's detail loaded; without it no section has anything to draw. */
  readonly loaded: boolean
  /** Name and description may be submitted (`PATCH /pages/{slug}`). */
  readonly mayEditMetadata: boolean
  /**
   * The whole panel document may be replaced. False while the Page carries a
   * panel this viewer may not see: saving the document would delete it.
   */
  readonly mayEditDocument: boolean
  /** Why `mayEditDocument` is false, in a sentence, or null when it is true. */
  readonly documentRefusal: string | null
  /** Grants, producer tokens and public links (`mayAdministerGrants` server-side). */
  readonly mayManageAccess: boolean
  /**
   * This Page has a **published** application. `has_application` on the wire
   * is `EXISTS(page_project_live WHERE published=1)`, so it is false for a
   * Page whose application has only ever been a draft.
   */
  readonly hasApplication: boolean
  /**
   * This Page has application source at all — a draft, published or not.
   *
   * The review has to open for a first publication, which is the one case
   * `hasApplication` cannot see: there is no live publication yet, and the
   * whole candidate is what needs reading. Gating the review on the
   * published flag hid exactly that screen.
   */
  readonly hasApplicationDraft: boolean
  /** A publication may be switched (owner or workspace admin). */
  readonly mayPublishApplication: boolean
  /** Source revisions and publications may be listed (`mayEditSpec` server-side). */
  readonly mayViewSourceHistory: boolean
}

export const NO_PAGE_CAPABILITIES: PageCapabilities = {
  loaded: false,
  mayEditMetadata: false,
  mayEditDocument: false,
  documentRefusal: null,
  mayManageAccess: false,
  hasApplication: false,
  hasApplicationDraft: false,
  mayPublishApplication: false,
  mayViewSourceHistory: false,
}

// ── Definition comparison ────────────────────────────────────────────────────

export type DefinitionChangeKind =
  | "page-renamed"
  | "page-described"
  /**
   * The slug is the Page's address. A publication that moves it breaks every
   * link anyone has shared, which is a change a reviewer has to be told about
   * in words rather than left to spot in a raw diff.
   */
  | "page-slug-changed"
  /** apiVersion or kind. Rare, and never something to discover afterwards. */
  | "document-version-changed"
  | "panel-added"
  | "panel-removed"
  | "panel-retitled"
  | "panel-schema-changed"
  | "panel-owner-changed"
  | "panel-producer-changed"
  | "panel-visibility-changed"
  | "panel-sla-changed"
  | "panel-tab-changed"
  | "panel-span-changed"
  | "panel-icon-changed"
  /**
   * `on_failure` is the declaration that turns a panel going quiet into work
   * for a human. A candidate removing it makes the Page fail silently, which
   * is exactly the change a reviewer must be shown rather than left to find.
   */
  | "panel-failure-handling-changed"
  | "panel-refresh-changed"
  | "panel-wake-changed"
  | "action-added"
  | "action-removed"
  | "action-routine-changed"
  /**
   * `call` runs a routine on the server; `custom` and `link` do not. Turning
   * one into the other changes what a button does, so it is a named change
   * and not a field buried in the raw diff.
   */
  | "action-kind-changed"
  | "action-relabelled"
  | "action-confirm-changed"

/** Colour is never the only carrier: every change also has a text mark. */
export type DefinitionChangeTone = "add" | "remove" | "change"

export interface DefinitionChange {
  readonly kind: DefinitionChangeKind
  readonly tone: DefinitionChangeTone
  readonly panelId?: string
  readonly actionId?: string
  /**
   * One derived English sentence. Derived means computed from the two
   * documents by `compareDefinitions` — never a string that travelled with the
   * candidate.
   */
  readonly summary: string
  readonly before?: string
  readonly after?: string
}

export interface DefinitionDiff {
  readonly changes: readonly DefinitionChange[]
  /**
   * Keys present in either document that the comparator does not model. They
   * are named so the reviewer knows the derived list is not the whole story,
   * and they are why `raw` is always produced.
   */
  readonly unmodelled: readonly string[]
  /** Unified diff of both documents pretty-printed as JSON. Text, never HTML. */
  readonly raw: string
  readonly identical: boolean
  /**
   * There was no baseline document to compare against. Every panel and action
   * therefore reads as added, which is true of an initial publication and is
   * NOT the same statement as "nothing else changed". The consumer must say
   * which of the two it is instead of rendering the added list on its own.
   */
  readonly baselineMissing: boolean
}

// ── Source comparison ────────────────────────────────────────────────────────

/**
 * A rename is reported conservatively as a removal plus an addition. Guessing
 * at renames means guessing at similarity, and a wrong guess hides a rewrite
 * inside what reads as a move.
 */
export type SourceFileStatus = "added" | "modified" | "removed"

export type SourceDiffLineKind = "hunk" | "context" | "add" | "del"

export interface SourceDiffLine {
  readonly kind: SourceDiffLineKind
  readonly text: string
  readonly oldLine: number | null
  readonly newLine: number | null
}

export interface SourceFileChange {
  readonly path: string
  readonly status: SourceFileStatus
  readonly added: number
  readonly removed: number
  /**
   * Not decodable as text, or declared base64. The change is described; the
   * bytes are never rendered and never executed.
   */
  readonly binary: boolean
  readonly lines: readonly SourceDiffLine[]
  /** The rendered hunks stop short of the whole change. Say so; never imply "all reviewed". */
  readonly truncated: boolean
}

export interface SourceDiff {
  readonly files: readonly SourceFileChange[]
  readonly filesAdded: number
  readonly filesModified: number
  readonly filesRemoved: number
  /** At least one file's body was cut, or files themselves were dropped. */
  readonly truncated: boolean
}

/**
 * Display ceilings for the browser. The transport already caps a project at
 * 256 files / 512 KiB per file / 2 MiB total (`internal/pages/project.go`);
 * these are smaller because rendering, not transport, is what stalls a tab.
 */
export const SOURCE_DIFF_LIMITS = {
  /** Lines rendered per file before the rest is summarised. */
  linesPerFile: 800,
  /** Files whose bodies are diffed; the remainder are listed with a status only. */
  filesWithBodies: 60,
  /** A file larger than this is listed but not line-diffed. */
  bytesPerFile: 192 * 1024,
} as const

// ── The authorized review snapshot (wire shapes, snake_case) ────────────────

/**
 * Actor identity as the server can actually prove it. `page_project_revisions`
 * stores ids in `actor_json`, not a snapshot of names, so a header reading
 * "agent ops-writer · crew Ops" would be invented (V03). `label` is present
 * only when the server resolved it at read time and says so.
 */
export interface ReviewActorWire {
  readonly kind: "user" | "agent" | "crew" | "unknown"
  readonly id: string
  /** Resolved now, not at the time of the change. Absent when unresolvable. */
  readonly label?: string
}

export interface ReviewCandidateWire {
  readonly revision: number
  readonly git_commit: string
  readonly source_digest: string
  /**
   * From the draft's revision row. Empty only when a draft has no matching
   * revision, which hand-edited data can produce; render it as unknown rather
   * than as an epoch.
   */
  readonly created_at: string
  /**
   * The document this candidate would make live, authorized for this viewer.
   *
   * It comes from the same handler as `baseline.definition` and is filtered
   * by the same rule, which is the whole point: the screen never compares a
   * document from one endpoint against a digest from another. Reading the
   * candidate from its own query — as this screen used to — is the
   * cross-endpoint gap that let a person approve a comparison the screen
   * never showed them.
   *
   * It is **not** a database snapshot, and this file used to say it was.
   * `ReviewProject` issues about ten separate autocommit statements and a
   * concurrent write can land between any two of them — an interleaving test
   * produces a snapshot reporting one publication number beside the live
   * declaration that preceded it, a pair that existed at no instant. Each
   * document is consistent with the statement that read it; the two are not a
   * joint view. What keeps consent honest is that the digests the screen
   * sends back are re-checked inside the publishing transaction, so a stale
   * comparison produces a 409 rather than a publication.
   *
   * Null only when the stored bytes do not parse as a Page document. That is
   * no basis for a comparison: say so, and do not offer consent.
   */
  readonly definition: unknown | null
  readonly actor: ReviewActorWire
  /**
   * The build of exactly this revision, when one exists.
   *
   * The states are the ones the database actually allows
   * (`page_project_builds`' CHECK): there is no `queued`, and `interrupted`
   * is real — `recoverPageBuilds` sets it after a restart, and so does a
   * build whose artifact could not be persisted. It is treated as a failure
   * to publish from, but it is not the same message as a compiler error and
   * must not be shown as one.
   */
  readonly build: {
    readonly id: string
    readonly state: "running" | "ready" | "failed" | "interrupted"
    readonly artifact_digest: string
    readonly error?: string
  } | null
}

/**
 * The two bases a review compares against, and they are different series: an
 * application's source revision number and its publication number never line
 * up, and the panel definition living on the Page is a third thing again.
 */
export interface ReviewBaselineWire {
  /** Current live publication number; 0 when nothing has ever been published. */
  readonly publication_version: number
  readonly published: boolean
  /**
   * sha256 of the Page's live `spec_json` — the definition fence value, over
   * the **full** stored bytes, whatever this viewer may see of them.
   */
  readonly definition_digest: string
  /**
   * The live Page document, authorized for this viewer, from the same read as
   * the digest above. The screen's comparison is derived from this and never
   * from the Page detail query.
   *
   * Null only when the stored bytes do not parse as a Page document.
   */
  readonly definition: unknown | null
  /**
   * Panels withheld from this comparison because the viewer may not read
   * them, counted once across both documents.
   *
   * They are **removed** from both sides rather than stubbed, so no phantom
   * addition or removal can appear. The cost is that a change confined to a
   * withheld panel is invisible here — including a panel being re-pointed
   * between a crew the viewer can see and one they cannot. The screen has to
   * say the comparison is partial; silent and partial beats loud and wrong,
   * but only if it admits which one it is.
   */
  readonly excluded_panels: number
  /**
   * At least one panel withheld from this comparison **differs** between the
   * live definition and the candidate.
   *
   * The count alone was not a policy. A reviewer who cannot see a panel can
   * still be the person who may publish — a Page owner who is not a workspace
   * admin sees only the panels of crews they belong to, and
   * `mayAdministerGrants` does not ask about panels at all. Withholding the
   * panel keeps the comparison from leaking it and from inventing a phantom
   * addition, but the consent underneath still said the whole change had been
   * reviewed, which was not true of the part that was hidden.
   *
   * So the server answers the only question that settles it, using both full
   * documents, which it has and the client does not: did anything the reader
   * cannot see actually change? False means their comparison covers
   * everything that moves, and consent can be honest about that scope. True
   * means it cannot, and this review may not complete the publication.
   */
  readonly withheld_changed: boolean
  /**
   * The live Page definition and the definition the live publication shipped
   * with have drifted apart — somebody changed panels outside the application
   * flow.
   *
   * It is a **statement, not a refusal**. The comparison on screen is derived
   * from the current live definition and the candidate, so it is complete and
   * correct regardless; what has drifted is a third thing, and the design asks
   * for that divergence to be shown rather than for the publication to be
   * blocked. Blocking it was worse than useless: publishing the candidate is
   * exactly what brings the two back into agreement, and refusing left no way
   * out of the editor at all — the live tester had to escape through the CLI
   * three times.
   *
   * Not to be confused with the `definition_moved` conflict, which means the
   * fence tripped: a base moved between the render and the click, the snapshot
   * is stale, and refreshing is genuinely the cure.
   */
  readonly definition_diverged: boolean
  readonly source_revision: number | null
  readonly git_commit: string | null
  /**
   * The retained source for the live publication is still readable. False means
   * "comparison unavailable" — which is NOT "no changes" (V05).
   */
  readonly source_available: boolean
  readonly source_unavailable_reason: string | null
}

/** `unknown` is a real state and must never be rendered as `unchanged` (V06). */
export type ReviewRoutineState = "unchanged" | "changed" | "unknown"

export interface ReviewRoutineWire {
  readonly routine: string
  readonly published_digest: string | null
  readonly current_digest: string | null
  readonly state: ReviewRoutineState
  /**
   * This routine is declared by the candidate being published, so it is part
   * of the key set the server recomputes and compares.
   *
   * The list is a union: it also carries routines the *live publication*
   * called, so a reviewer can see one being dropped. Sending a dropped
   * routine in `expected_routine_digests` produces a 409 naming a routine
   * nobody moved — and no amount of refetching clears it, because the
   * snapshot says the same thing again. Only rows with this flag belong in
   * the fence.
   */
  readonly in_candidate: boolean
}

/**
 * Machine-readable reasons publishing is refused from this review. The UI shows
 * the message; tests assert on the code.
 */
export type ReviewBlockerCode =
  | "no_candidate"
  | "candidate_matches_live"
  | "build_missing"
  | "build_failed"
  | "build_stale"
  | "baseline_unavailable"
  /**
   * The candidate declares an action calling a routine that no longer
   * resolves. Publishing refuses it with a 422, so the review has to as
   * well — reporting the routine as merely `unknown` told the reviewer that
   * publishing was available when it was not.
   */
  | "routine_unresolved"
  /**
   * Part of what this publication changes is not visible to this reviewer, so
   * they cannot attest to it. Neutral by construction: it says that such a
   * part exists, never what or whose it is, and it does not hand anybody a
   * new permission or a delegation button the product does not have.
   */
  | "withheld_change"
  /**
   * Reserved for the publish fence tripping, not for a standing divergence
   * between the live definition and the published one — that is
   * `baseline.definition_diverged`, and it is advisory.
   */
  | "definition_moved"
  | "not_permitted"
  | "storage_unavailable"

export interface ReviewBlockerWire {
  readonly code: ReviewBlockerCode
  readonly message: string
}

export interface ReviewSnapshotWire {
  readonly issued_at: string
  readonly candidate: ReviewCandidateWire | null
  readonly baseline: ReviewBaselineWire
  readonly routines: readonly ReviewRoutineWire[]
  readonly capabilities: {
    readonly may_edit_spec: boolean
    readonly may_publish: boolean
  }
  readonly blockers: readonly ReviewBlockerWire[]
  /** True when there is no previous publication at all: review the whole candidate. */
  readonly initial_publication: boolean
}

/**
 * The fenced publish request. `expected_definition_digest` and
 * `expected_routine_digests` are what the human reviewed; the server compares
 * them with the current values inside the publishing transaction and refuses
 * with 409 when either moved. Both are required — an optional fence is a fence
 * a caller forgets, and the point of P0 was that the UI's own refetch cannot
 * provide this property.
 */
export interface FencedPublishRequest {
  readonly build_id?: string
  readonly expected_revision?: number
  readonly expected_publication: number
  readonly reviewed_code: true
  readonly rollback_version?: number
  readonly expected_definition_digest: string
  readonly expected_routine_digests: Readonly<Record<string, string>>
  /**
   * States that the caller is publishing although the retained source behind
   * the live publication cannot be read, so no comparison with what is
   * running was possible. Without it the server answers 409 with
   * `conflict: "baseline"`, and the publication's receipt records which of
   * the three it was.
   *
   * **The review screen must never send this.** The design blocks publishing
   * from a review whose baseline is unavailable and says the alternative to
   * a full review has to be approved by the product rather than assumed;
   * until it is, the escape lives at the API and the CLI, where a person
   * states it deliberately and it is written down. A checkbox next to a
   * Publish button is the opposite of that.
   */
  readonly acknowledged_unavailable_baseline?: boolean
}

/**
 * 409 body when the fence trips. `conflict` names which base moved, so the UI
 * can say what to re-review rather than "something changed".
 */
export type PublishConflictKind = "definition" | "routines" | "publication" | "draft" | "baseline"

export interface PublishConflictWire {
  readonly error: string
  readonly conflict?: PublishConflictKind
  readonly routines?: readonly string[]
}

// ── Save semantics ───────────────────────────────────────────────────────────

/**
 * There is no global Save in this editor, and no autosave. Every control says
 * which of these it is, because "Save" over a live panel definition and "Save"
 * over an application draft are not the same promise.
 */
export type SaveEffect =
  | "live-definition"
  | "application-draft"
  | "build"
  | "publication"
  | "immediate-grant"

export const SAVE_EFFECT_NOTE: Readonly<Record<SaveEffect, string>> = {
  "live-definition": "Saving changes the live Page for everyone who can see it.",
  "application-draft": "Saving keeps a draft. The published application does not change.",
  build: "Building produces a preview of the candidate. It does not publish anything.",
  publication: "Publishing replaces the live application and its definition.",
  "immediate-grant": "This is written on its own, immediately. It is never part of a publication.",
}
