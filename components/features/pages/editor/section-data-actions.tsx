"use client"

/**
 * Data & actions — where each panel's data comes from, and what it offers
 * (PRD `docs/prd/pages-settings-editor-review-proposal-2026-09-10.md` §4, and
 * the independent review §7).
 *
 * This section DESCRIBES a declaration; it does not run anything and it does
 * not write anything. Three refusals are the whole design:
 *
 *  · **No token form.** Issuing a producer credential is a permission, and a
 *    permission lives in Access — one place, one audit trail. This section
 *    links there and stops (independent review §7).
 *
 *  · **No global Save.** §4 forbids a single Save here that does not say
 *    whether it is writing the live panel definition or an application draft.
 *    The honest resolution is that this section writes neither: the panel
 *    document is saved in Content, the candidate is published from the review.
 *
 *  · **No action buttons.** Running an action is a real operation. The Page
 *    itself is where somebody runs one, having decided to; an editor showing
 *    the declaration must not be a place to fire it by accident.
 *
 * The banner at the top is the point of the section: what is described here is
 * the LIVE definition. When the Page also carries an application candidate,
 * that candidate can declare a different definition, and a reader who does not
 * know which one they are looking at is the failure §4 names.
 */

import * as React from "react"
import { useQuery } from "@tanstack/react-query"

import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { apiErrorMessage } from "@/lib/api-error"
import { formatDateTime } from "@/lib/time"
import { PAGE_STATE_META } from "@/components/features/pages/page-state"
import { formatSlaSeconds, slaSecondsFrom } from "@/components/features/pages/page-editor"
import type { PageAction } from "@/components/features/pages/panels/panel-actions"
import { toPanelView, type WirePanel } from "@/hooks/use-pages"
import { pageQueryString, type WirePageDetail } from "@/hooks/use-page-grants"
import type { EditorSectionProps } from "@/components/features/pages/editor/section-props"

export function EditorDataActionsSection({
  workspaceId,
  slug,
  page,
  capabilities,
  onNavigate,
  onDirtyChange,
}: EditorSectionProps) {
  // Nothing here is editable, so this section can never hold unsaved work.
  // Saying so on mount keeps a flag raised by another section from following
  // the reader into this one.
  React.useEffect(() => {
    onDirtyChange(false)
  }, [onDirtyChange])

  // Read-failure and emptiness are two different sentences, and rendering the
  // second for the first was F7: an empty list drawn as a claim about the Page
  // ("declares no panels") when the detail read had 403'd or 500'd.
  if (!capabilities.loaded || page == null) {
    return (
      <p role="status" className="type-page-value text-muted-foreground">
        This Page could not be read, so nothing here describes it. Its panels, their producers and their
        actions are unknown — which is not the same as none.
      </p>
    )
  }

  const panels = Array.isArray(page.panels) ? page.panels : []

  return (
    <div className="flex flex-col gap-5">
      <div>
        <h3 className="text-heading font-medium">Data &amp; actions</h3>
        <p className="type-page-meta text-muted-foreground">
          The producer behind each panel, when it last wrote, and the buttons the panel declares.
        </p>
      </div>

      <LiveDefinitionBanner
        workspaceId={workspaceId}
        slug={slug}
        page={page}
        hasApplication={capabilities.hasApplication}
        onNavigate={onNavigate}
      />

      {panels.length === 0 ? (
        <p className="type-page-value text-muted-foreground">
          This Page declares no panels, so there is no producer and no action to describe yet.
        </p>
      ) : (
        panels.map((raw, index) => (
          <PanelDataCard
            key={raw.id ?? raw.panel_id ?? index}
            raw={raw}
            index={index}
            onNavigate={onNavigate}
          />
        ))
      )}
    </div>
  )
}

// ── The banner: live definition, and whether a candidate disagrees ─────────

/**
 * The candidate's definition, compared with the live one.
 *
 * `unknown` is a state and is never rendered as "no changes": a comparison we
 * could not make is not a comparison that came back empty (V05). The read is
 * issued only when this Page actually has an application — an ordinary panel
 * Page fetches nothing to draw this section.
 */
type CandidateComparison =
  | { kind: "none" }
  | { kind: "loading" }
  | { kind: "same" }
  | { kind: "differs" }
  | { kind: "unknown"; reason: string }

/**
 * One panel's DECLARATION, flattened to a string.
 *
 * Compared this way rather than field by field because the question is binary
 * — "does the candidate declare something else?" — and because the two sides
 * arrive in two shapes: the live Page sends `sla_seconds`, the candidate's
 * document carries the Go duration string `sla` (`internal/pages/spec.go`).
 * Both go through `slaSecondsFrom`, so `1h` and `3600` are not a difference.
 *
 * The failure direction is chosen: this can report a difference that is only
 * cosmetic, and must never report sameness that is not real. A false "differs"
 * costs a reader one look at Content; a false "same" hides the change the
 * banner exists to announce.
 */
function declarationOf(raw: Record<string, unknown>): string {
  const actions = Array.isArray(raw.actions) ? raw.actions : []
  return JSON.stringify({
    id: raw.id ?? raw.panel_id ?? "",
    schema: raw.schema ?? "",
    title: raw.title ?? "",
    owner: raw.owner ?? "",
    producer: raw.producer ?? "",
    sla: slaSecondsFrom(raw.sla_seconds ?? raw.sla),
    span: raw.span ?? 0,
    public: raw.public === true,
    tab: raw.tab ?? "",
    refresh: raw.refresh ?? "",
    actions: actions.map((entry) => {
      const a = (entry ?? {}) as Record<string, unknown>
      return { id: a.id ?? "", kind: a.kind ?? "", label: a.label ?? "", routine: a.routine ?? "" }
    }),
  })
}

function definitionSignature(panels: readonly unknown[]): string {
  return panels.map((p) => declarationOf((p ?? {}) as Record<string, unknown>)).join("\n")
}

async function readCandidate(
  slug: string,
  workspaceId: string,
  signal: AbortSignal | undefined,
): Promise<CandidateComparison | { panels: unknown[] }> {
  const res = await apiFetch(
    `/api/v1/pages/${encodeURIComponent(slug)}/project${pageQueryString(workspaceId)}`,
    { signal },
  )
  // 404 is the ordinary answer for a Page whose application has no draft: the
  // live definition is the only definition, and there is nothing to warn about.
  if (res.status === 404) return { kind: "none" }
  if (!res.ok) {
    const body = await res.json().catch(() => null)
    return { kind: "unknown", reason: apiErrorMessage(body, `The server answered ${res.status}.`) }
  }
  const body = (await res.json().catch(() => null)) as
    | { definition?: { spec?: { panels?: unknown[] } } }
    | null
  const panels = body?.definition?.spec?.panels
  if (!Array.isArray(panels)) {
    return {
      kind: "unknown",
      reason: "The application draft carried no readable panel definition.",
    }
  }
  return { panels }
}

function LiveDefinitionBanner({
  workspaceId,
  slug,
  page,
  hasApplication,
  onNavigate,
}: {
  workspaceId: string
  slug: string
  page: WirePageDetail | null
  hasApplication: boolean
  onNavigate: EditorSectionProps["onNavigate"]
}) {
  const query = useQuery({
    queryKey: ["page-candidate-definition", workspaceId, { slug }],
    queryFn: ({ signal }) => readCandidate(slug, workspaceId, signal),
    enabled: hasApplication,
    retry: false,
    gcTime: 0,
  })

  const livePanels = page?.panels
  const comparison: CandidateComparison = React.useMemo(() => {
    const live = Array.isArray(livePanels) ? livePanels : []
    if (!hasApplication) return { kind: "none" }
    // `loading` is its own state, not an early `unknown`: the two read the
    // same to a person and completely differently to a test, and a banner that
    // announced an unavailable comparison while the request was still open
    // would cry wolf on every mount.
    if (query.isPending) return { kind: "loading" }
    if (query.isError) {
      return { kind: "unknown", reason: (query.error as Error).message }
    }
    const data = query.data
    if (!data) return { kind: "unknown", reason: "The application draft could not be read." }
    if (!("panels" in data)) return data
    return definitionSignature(data.panels) === definitionSignature(live)
      ? { kind: "same" }
      : { kind: "differs" }
  }, [hasApplication, livePanels, query.data, query.error, query.isError, query.isPending])

  return (
    <div
      role="note"
      data-slot="live-definition-banner"
      data-comparison={comparison.kind}
      className="flex flex-col gap-2 rounded-md border border-warn/40 bg-warn/[0.06] px-3 py-2.5"
    >
      <p className="type-page-value">
        This section describes the <strong>live</strong> panel definition — what this Page shows and does
        for everyone who can see it right now.
      </p>

      {comparison.kind === "loading" && (
        <p className="type-page-meta text-muted-foreground">Reading the application draft…</p>
      )}
      {comparison.kind === "differs" && (
        <p className="type-page-value">
          This Page also has an application candidate, and its definition is{" "}
          <strong>not the same as the live one</strong>. Nothing on this screen describes the candidate.
          Review it in Content before publishing.
        </p>
      )}
      {comparison.kind === "same" && (
        <p className="type-page-meta text-muted-foreground">
          This Page has an application candidate; its definition matches the live one described here.
        </p>
      )}
      {comparison.kind === "unknown" && (
        <p className="type-page-meta text-muted-foreground">
          {/* Never "no changes": a comparison that could not be made is not an
              empty one, and saying otherwise is what V05 recorded. */}
          The application candidate&apos;s definition could not be compared with the live one, so this
          section may not describe what a publication would produce. {comparison.reason}
        </p>
      )}

      {hasApplication && (
        <div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="coarse:min-h-11"
            onClick={() => onNavigate("content")}
          >
            Review the candidate in Content
          </Button>
        </div>
      )}
    </div>
  )
}

// ── One panel ──────────────────────────────────────────────────────────────

/**
 * The routine an action calls, read off the AUTHORED declaration.
 *
 * `normalizePanelActions` deliberately has nowhere to put it — §8b.2's whole
 * property is that a click posts an action id and the SERVER resolves what it
 * runs, so a `routine` must never travel back to the wire from the browser.
 * Reading it here is the other direction and is safe: the authored half is
 * echoed only to a caller who may edit the spec
 * (`internal/api/pages_authored_echo_test.go`), which is who is standing in
 * this editor, and it is displayed, never sent.
 */
function declaredRoutines(raw: WirePanel): Map<string, string> {
  const out = new Map<string, string>()
  const actions = (raw as { actions?: unknown }).actions
  if (!Array.isArray(actions)) return out
  for (const entry of actions) {
    if (!entry || typeof entry !== "object") continue
    const a = entry as Record<string, unknown>
    if (typeof a.id === "string" && typeof a.routine === "string" && a.routine.trim() !== "") {
      out.set(a.id, a.routine.trim())
    }
  }
  return out
}

function PanelDataCard({
  raw,
  index,
  onNavigate,
}: {
  raw: WirePanel
  index: number
  onNavigate: EditorSectionProps["onNavigate"]
}) {
  const view = toPanelView(raw, index)
  const state = view.state ? PAGE_STATE_META[view.state] : null
  const StateIcon = state?.icon
  // `PanelProvenance.produced_at` is typed `string | Date` for the panels'
  // own renderer; `formatDateTime` takes the wire's string.
  const producedAtRaw = view.snapshot.provenance?.produced_at ?? null
  const producedAt =
    producedAtRaw instanceof Date ? producedAtRaw.toISOString() : producedAtRaw
  const sla = view.spec.sla_seconds
  const actions: PageAction[] = view.actions
  const routines = declaredRoutines(raw)

  if (view.spec.sealed) {
    return (
      <section
        data-slot="panel-data"
        data-sealed="true"
        className="rounded-lg border border-border/60 bg-card p-4"
      >
        <h4 className="text-body font-medium">{view.spec.id}</h4>
        <p className="type-page-meta text-muted-foreground">
          Sealed · owned by {view.spec.owner_crew_name ?? "another crew"}. Its producer, its data and its
          actions are not yours to read.
        </p>
      </section>
    )
  }

  return (
    <section
      data-slot="panel-data"
      data-panel={view.spec.id}
      className="flex flex-col gap-3 rounded-lg border border-border/60 bg-card p-4"
    >
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h4 className="min-w-0 text-body font-medium break-words">
          {view.spec.title ?? view.spec.id}
        </h4>
        {state && (
          <span className={cn("inline-flex items-center gap-1.5 type-page-meta", state.tone)}>
            {StateIcon && <StateIcon className="h-3.5 w-3.5" aria-hidden />}
            {state.label}
          </span>
        )}
      </div>

      <dl data-slot="panel-source" className="flex flex-col gap-1">
        <SourceLine label="Producer">
          {view.producer ?? view.snapshot.provenance?.producer ?? "No producer declared"}
        </SourceLine>
        <SourceLine label="Last accepted">
          {producedAt ? (
            formatDateTime(producedAt)
          ) : (
            // Not a zero and not a dash beside a number: nothing has ever been
            // written here, and that is a different statement from a measured
            // value of nothing (§9b.4).
            <>Awaiting first data</>
          )}
        </SourceLine>
        <SourceLine label="Expected">
          {typeof sla === "number" && sla > 0
            ? // No em dash in this sentence: on this surface `—` means "no basis
              // to compute" and nothing else (§9b.4), and it must not turn up as
              // punctuation beside a number that IS declared.
              `Every ${formatSlaSeconds(sla)}, and silence past that is reported as stale`
            : "No SLA declared"}
        </SourceLine>
      </dl>

      <div data-slot="panel-actions-declared" className="flex flex-col gap-2 border-t border-border/40 pt-3">
        <span className="type-page-label text-muted-foreground-soft">Actions this panel offers</span>
        {actions.length === 0 ? (
          <p className="type-page-meta text-muted-foreground">This panel declares no actions.</p>
        ) : (
          <>
            <ul className="flex flex-col gap-2">
              {actions.map((action) => (
                <li key={action.id} data-slot="declared-action" data-action={action.id}>
                  <p className="type-page-value break-words">
                    <span className="font-medium">{action.label}</span>{" "}
                    <span className="type-page-stamp text-muted-foreground">{action.kind}</span>
                  </p>
                  <p className="type-page-meta text-muted-foreground break-words">
                    {routines.has(action.id)
                      ? `Calls routine ${routines.get(action.id)}`
                      : action.kind === "call"
                        ? "The routine it calls is resolved by the server from the stored declaration."
                        : action.kind === "toggle"
                          ? `Shows or hides ${(action.target ?? []).join(", ") || "panels on this Page"}`
                          : "Opens another place in Crewship."}
                  </p>
                  {action.confirm && (
                    <p className="type-page-meta text-muted-foreground break-words">
                      Confirms first: “{action.confirm.title}
                      {action.confirm.body ? ` — ${action.confirm.body}` : ""}”
                    </p>
                  )}
                </li>
              ))}
            </ul>
            <p className="type-page-meta text-muted-foreground">
              Running an action is a real operation, not a preview. It is run from the Page itself, not
              from this editor.
            </p>
          </>
        )}
      </div>

      <div className="flex flex-wrap items-center justify-between gap-2 border-t border-border/40 pt-3">
        {/* No token form here, by design: issuing a producer credential is a
            permission, and every permission on this Page is in one place. */}
        <p className="type-page-meta min-w-0 text-muted-foreground">
          Who may write to this panel is a permission, not a setting on the panel.
        </p>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="coarse:min-h-11"
          onClick={() => onNavigate("access")}
        >
          Manage producer access
        </Button>
      </div>
    </section>
  )
}

function SourceLine({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
      <dt className="type-page-label shrink-0 text-muted-foreground-soft">{label}</dt>
      <dd className="min-w-0 type-page-value break-words">{children}</dd>
    </div>
  )
}
