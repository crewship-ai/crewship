"use client"

/**
 * Effective access — who reaches this Page, and by which paths.
 *
 * The fourth card of the Access section (PRD pages-collections-access-analysis
 * §3 S-6, §5/10; #2528). The three cards above it are the ACL a reader can
 * change: grants, producer tokens, public links. This one is the ANSWER to
 * the question those cards only partly pose — given the role, the ownership,
 * the crews that own panels and the grants in force, who actually gets in —
 * and it is computed on the server, once, from the same facts the server
 * enforces. Nothing here is derived client-side from the grant list: a card
 * that re-derived reach from the ACL it can see would be a second copy of the
 * rule, wrong the moment a panel's crew or a member's role changed.
 *
 * Read-only, by design and by vocabulary. A path is one of the server's own
 * words — `owner`, `role`, `crew:<slug>`, `panel_crew:<slug>`,
 * `grant:page:<level>`, `panel_crew:withheld` — rendered here as a sentence a
 * person can read ("reaches through crew ops and a read grant") without the
 * card ever inventing a path the server did not send. The word `withheld` is
 * the server declining to name a crew the reader cannot see, and the sentence
 * says exactly that: which crew it is, is not this reader's to know.
 *
 * Two rules, shared with the rest of the section:
 *
 *  1. A 403 is an answer, not a failure. Reading a Page's access takes the
 *     same right as reading its grants, and a reader without it is told so in
 *     the server's words, in place — the card stays on screen with the
 *     refusal where its rows would be. A card that vanished would send the
 *     reader to the CLI to find out why.
 *  2. There is nothing to click. No grant is issued here and no row is a
 *     control; the People and crews card above is where access is changed.
 */

import * as React from "react"
import { useQuery } from "@tanstack/react-query"
import { EyeOff, Route } from "lucide-react"

import { apiFetch } from "@/lib/api-fetch"
import { apiErrorMessage } from "@/lib/api-error"
import { DetailCard, EntityChip } from "@/components/ui/detail"
import { Spinner } from "@/components/ui/spinner"
import { EmptyState } from "@/components/layout/empty-state"
import { Refusal, pageSubjectIcon } from "@/components/features/pages/page-settings"
import { PagesRequestError } from "@/hooks/use-pages"
import { gateOf, pageQueryString } from "@/hooks/use-page-grants"

// ── The wire (mirrors internal/api/pages_access.go) ────────────────────────

export interface WireEffectiveAccessSubject {
  subject_type?: string | null
  subject_id?: string | null
  /** Absent on the anonymous row that stands for the crews the caller cannot see. */
  label?: string | null
  paths?: string[] | null
}

export interface WireEffectiveAccess {
  page?: string | null
  subjects?: WireEffectiveAccessSubject[] | null
  next_cursor?: string | null
}

/** The server's marker for a crew it will not name (`pageAccessWithheld`). */
export const WITHHELD = "withheld"

export interface EffectiveAccessSubject {
  kind: "user" | "crew" | "agent" | "unknown"
  id: string
  label: string | null
  paths: string[]
  /** The anonymous row: `subject_id` is `withheld` and there is no label. */
  withheld: boolean
}

function trimmed(value: unknown): string | null {
  return typeof value === "string" && value.trim() !== "" ? value.trim() : null
}

export function toEffectiveAccessSubject(raw: WireEffectiveAccessSubject): EffectiveAccessSubject {
  const kindRaw = trimmed(raw.subject_type)
  const kind = kindRaw === "user" || kindRaw === "crew" || kindRaw === "agent" ? kindRaw : "unknown"
  const id = trimmed(raw.subject_id) ?? ""
  return {
    kind,
    id,
    label: trimmed(raw.label),
    paths: Array.isArray(raw.paths)
      ? raw.paths.map((p) => trimmed(p)).filter((p): p is string => p !== null)
      : [],
    withheld: id === WITHHELD,
  }
}

export function normalizeEffectiveAccess(body: unknown): {
  subjects: EffectiveAccessSubject[]
  more: boolean
} {
  if (body && typeof body === "object") {
    const rec = body as WireEffectiveAccess
    return {
      subjects: Array.isArray(rec.subjects) ? rec.subjects.map(toEffectiveAccessSubject) : [],
      more: trimmed(rec.next_cursor) !== null,
    }
  }
  return { subjects: [], more: false }
}

// ── Sentences ──────────────────────────────────────────────────────────────
//
// One phrase per path, in the server's order, joined into one clause. The
// vocabulary is closed on the server (pageReach, pages_authz.go), so a path
// this build does not recognise is printed as the server sent it rather than
// dropped — dropping it would make the sentence claim less reach than the
// server reported.

interface Phrase {
  text: string
  /** Prefixed with "through " when it opens a run of such phrases. */
  through: boolean
}

function phraseFor(path: string, kind: EffectiveAccessSubject["kind"]): Phrase {
  if (path === "owner") {
    return kind === "crew" ? { text: "owns this Page", through: false } : { text: "as the owner", through: false }
  }
  if (path === "role") return { text: "the workspace role", through: true }
  if (path.startsWith("crew:")) return { text: `crew ${path.slice("crew:".length)}`, through: true }
  if (path === `panel_crew:${WITHHELD}`) return { text: "a crew you cannot see", through: true }
  if (path.startsWith("panel_crew:")) {
    const slug = path.slice("panel_crew:".length)
    return kind === "crew"
      ? { text: "owns a panel on it", through: false }
      : { text: `a panel owned by crew ${slug}`, through: true }
  }
  if (path.startsWith("grant:page:")) {
    const level = path.slice("grant:page:".length)
    return kind === "user" ? { text: `a ${level} grant`, through: true } : { text: `holds a ${level} grant`, through: false }
  }
  return { text: path, through: true }
}

function joinClauses(parts: string[]): string {
  if (parts.length <= 1) return parts.join("")
  return `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1]}`
}

/**
 * "reaches as the owner, through crew ops and a read grant" — the verb is the
 * caller's to put in front. For a crew or an agent the phrases are already
 * verbs ("owns this Page", "holds a read grant") and no lead is needed.
 */
export function effectiveAccessPhrase(subject: EffectiveAccessSubject): string {
  const phrases = subject.paths.map((p) => phraseFor(p, subject.kind))
  const parts = phrases.map((phrase, i) => {
    const prev = phrases[i - 1]
    const lead = phrase.through && !(prev && prev.through) ? "through " : ""
    return lead + phrase.text
  })
  return joinClauses(parts)
}

/**
 * Everything after the "·": "reaches through crew ops and a read grant". The
 * card draws the subject as a chip and this as the text beside it, so the two
 * halves are built apart and joined once, by `effectiveAccessSentence`.
 */
export function effectiveAccessClause(subject: EffectiveAccessSubject): string {
  if (subject.withheld) return "owns a panel on this Page. Which crew it is, is withheld from you."
  const clause = effectiveAccessPhrase(subject)
  return subject.kind === "user" ? `reaches ${clause}` : clause
}

/** The whole sentence: "ada@example.com · reaches through crew ops and a read grant". */
export function effectiveAccessSentence(subject: EffectiveAccessSubject): string {
  return `${effectiveAccessLabel(subject)} · ${effectiveAccessClause(subject)}`
}

export function effectiveAccessLabel(subject: EffectiveAccessSubject): string {
  if (subject.withheld) return "A crew you cannot see"
  const name = subject.label ?? subject.id
  if (subject.kind === "crew") return `crew ${name}`
  if (subject.kind === "agent") return `agent ${name}`
  return name
}

// ── Reading ────────────────────────────────────────────────────────────────

export const effectiveAccessKey = (workspaceId: string, slug: string) =>
  ["page-access", workspaceId, { slug }] as const

async function readEffectiveAccess(workspaceId: string, slug: string, signal: AbortSignal | undefined) {
  const res = await apiFetch(
    `/api/v1/pages/${encodeURIComponent(slug)}/access${pageQueryString(workspaceId)}&limit=200`,
    { signal },
  )
  if (!res.ok) {
    const body = await res.json().catch(() => null)
    // The server's own sentence: its 403 says which right is missing.
    throw new PagesRequestError(res.status, apiErrorMessage(body, `access: ${res.status}`))
  }
  return normalizeEffectiveAccess(await res.json())
}

// ── The card ───────────────────────────────────────────────────────────────

export function EffectiveAccessCard({ workspaceId, slug }: { workspaceId: string; slug: string }) {
  const on = Boolean(workspaceId) && Boolean(slug)
  const query = useQuery({
    queryKey: effectiveAccessKey(workspaceId, slug),
    queryFn: ({ signal }) => readEffectiveAccess(workspaceId, slug, signal),
    enabled: on,
    retry: false,
  })
  const { loading, refusal, error } = gateOf(query.error, query.isPending, on)
  const subjects = query.data?.subjects ?? []
  const more = query.data?.more ?? false

  const answer = refusal
    ? "not yours to read"
    : loading
      ? "loading"
      : subjects.length === 0
        ? "nobody"
        : `${subjects.length}${more ? "+" : ""} ${subjects.length === 1 && !more ? "subject" : "subjects"}`

  // `DetailCard` has no `data-slot` of its own, and the tests (and the
  // section) address this card by that slot — including its title band, so
  // the slot has to enclose the whole card. `contents` keeps the wrapper out
  // of the layout: the card stays the section's flex item.
  return (
    <div data-slot="page-effective-access" className="contents">
      <DetailCard
        title="Effective access"
        icon={Route}
        subtitle={answer}
        footer="Computed by the server from the workspace roles, the owner, the crews that own its panels and the grants in force. Read-only — change access in the cards above."
      >
      <div className="flex flex-col gap-3">
        {refusal && <Refusal>{refusal}</Refusal>}
        {error && <Refusal>{error}</Refusal>}

        {loading && !refusal && (
          <div className="type-meta flex items-center gap-2 py-4 text-muted-foreground">
            <Spinner className="h-3.5 w-3.5" />
            Working out who reaches this Page…
          </div>
        )}

        {!loading && !refusal && !error && subjects.length === 0 && (
          <EmptyState
            size="inline"
            icon={Route}
            title="Nobody reaches this Page"
            description="The server found no owner, no administrator, no crew owning a panel and no live grant."
          />
        )}

        {!refusal && subjects.length > 0 && (
          <ul data-slot="page-effective-access-rows" className="flex flex-col gap-1.5">
            {subjects.map((s) => (
              <li
                key={`${s.kind}:${s.id}`}
                data-slot="page-effective-access-row"
                data-withheld={s.withheld ? "true" : undefined}
                className="type-page-value flex flex-wrap items-center gap-1.5 text-foreground"
              >
                {/* The subject as a chip, the paths as the sentence beside
                    it. The " · " is a real text node so the row still READS
                    as `effectiveAccessSentence` — the sentence the tests and
                    the CLI both print — and not as two fragments. A chip with
                    no onClick and no href is a span: nothing here is a
                    control (rule 2 in the file comment). */}
                <EntityChip
                  icon={s.withheld ? EyeOff : pageSubjectIcon(s.kind)}
                  label={effectiveAccessLabel(s)}
                />
                <span className="text-muted-foreground">
                  {" · "}
                  {effectiveAccessClause(s)}
                </span>
              </li>
            ))}
          </ul>
        )}

        {more && (
          <p className="type-meta text-muted-foreground">
            More subjects reach this Page than are shown here. The complete list is{" "}
            <code className="font-mono">crewship page access {slug}</code>.
          </p>
        )}
      </div>
      </DetailCard>
    </div>
  )
}
