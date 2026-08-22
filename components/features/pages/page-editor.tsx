"use client"

/**
 * The in-app page editor — PRD `docs/prd/pages.md` §10b.1.
 *
 * "**The editor already exists.** CodeMirror 6 is in `package.json:27-40` […]
 * and `components/features/routines/routine-editor-tab.tsx` already wires it up
 * for routines. The Pages editor is the same component with the YAML mode and a
 * linter fed by our own schema. Authoring is therefore: the CLI, the in-app
 * editor, or an agent — three doors onto one document."
 *
 * Three properties of this file carry the weight:
 *
 *  1. **One document, three doors.** What is in the buffer is byte-for-byte a
 *     document `crewship page create --file` would accept — `apiVersion` /
 *     `kind` / `metadata` / `spec`, nothing added and nothing renamed. An
 *     editor-only key would make the third door open onto a different room.
 *     The corollary is the one this file got wrong once and must not again: a
 *     key the document HAS is a key the wire body carries. `actions:`, `wake:`
 *     and `on_failure:` are the sensor half of a panel (§5, §8b.1, §4 rule 4);
 *     `PATCH` replaces `spec.panels` wholesale and reconciles the page's
 *     `automations` rows against what it was sent, so a field this translation
 *     drops is a gate the save DELETES. See `pageWritePanel` — every writable
 *     key is carried, and the pass-through half is carried verbatim, exactly as
 *     `cmd_page.go`'s `pageWritePanelJSON` carries it.
 *
 *  2. **The document shape is not the wire shape.** `POST /api/v1/pages` takes
 *     the FLAT `{slug, name, description, panels, owner}`
 *     (`internal/api/pages_handler.go` `pageWriteRequest`), and `sla: 30s` is
 *     YAML sugar the client converts to `sla_seconds: 30` (§11b decision 3).
 *     `cmd/crewship/cmd_page.go` `pageWriteFrom` is the same translation in Go,
 *     and this file is deliberately its mirror — including truncating the
 *     duration to whole seconds the way `int(sla.Seconds())` does.
 *
 *  3. **The gate is the server's.** §10b.1: authoring "validates the spec
 *     against the schema and checks that every declared producer and owner
 *     resolves". Only the server can do the second half, and its refusals are
 *     written to be read — the missing-SLA one quotes §4 at you. So a refusal is
 *     shown VERBATIM and the buffer is left exactly as typed (#1563 rule 3 —
 *     never destroy the state a retry needs). What this file lints is additive
 *     and never blocks Save: an inline warning is a hint, not a second gate that
 *     can disagree with the first.
 */

import * as React from "react"
import { AlertCircle, Loader2, Save, X } from "lucide-react"
import { toast } from "sonner"
import { linter, type Diagnostic } from "@codemirror/lint"
import type { Extension } from "@codemirror/state"
import type { EditorView } from "@codemirror/view"
import { isMap, isSeq, parseDocument, stringify as stringifyYaml, type Node } from "yaml"

import { Button } from "@/components/ui/button"
import { FileEditor } from "@/components/features/files/file-editor"
import { ApiMutationError, useApiMutation } from "@/hooks/use-api-mutation"
import { pagesKeys, type WirePage, type WirePanel } from "@/hooks/use-pages"
import { PANEL_SCHEMAS } from "@/components/features/pages/panels/types"

// ── The document ↔ wire translation (the CLI's, in TypeScript) ─────────────

/** One panel as `POST`/`PATCH` accept it — `pagePanelWire`'s writable half. */
export interface PageWritePanel {
  id: string
  schema: string
  title?: string
  owner: string
  producer: string
  /** §11b.3: an INTEGER on the wire. `sla: 30s` is sugar and lives in YAML
   *  only. Absent when the buffer's `sla` could not be read as a duration —
   *  the server then refuses with the §4 message, which is the right words in
   *  the right voice, and better than any wording invented here. */
  sla_seconds?: number
  span?: number
  public?: boolean
  /**
   * The two authored SCALARS that are not part of a panel's contract: its
   * glyph (internal/pages/icons.go) and the tab it renders under
   * (internal/pages/tabs.go). Neither has a column — both live in `spec_json`
   * and both come back on the read path — so both must make the round trip
   * here for the same reason the gates do: `PATCH` replaces the panel set
   * wholesale, and a field this file does not mention is a field the save
   * deletes. A page whose tabs vanished after a title edit is one long scroll
   * with nothing in a log to say why.
   */
  icon?: string
  tab?: string
  /**
   * The third authored scalar (§12 v1.1, internal/pages/refresh.go): the event
   * that RUNS this panel's producer, `on:wake` or `on:panels-changed`.
   *
   * A scalar and not a pass-through key, so it travels with the icon and the
   * tab rather than with the gates — but it is here for the gates' reason,
   * sharpened: a save that drops it deletes the `automations` row compiled from
   * it, and the page then looks exactly as it did while quietly never running
   * the producer again.
   */
  refresh?: string
  /**
   * The pass-through half — §8b.1 buttons, §5 wake gates, §4 rule 4's failure
   * route. Carried VERBATIM and typed loosely on purpose, the way
   * `cmd_page.go`'s `pageWritePanelJSON` carries `[]pages.PanelAction` without
   * looking inside it: the vocabulary is the server's, it validates `when:`
   * against the panel's schema and resolves the crews these name, and a second
   * copy of those shapes here would be a second grammar to keep in step.
   *
   * They are not decoration. `PATCH /api/v1/pages/{slug}` replaces the panel
   * set wholesale and reconciles `automations` in the same transaction, so a
   * panel sent without its gates is a panel whose gates are deleted — silently,
   * which §6 says is worse than failing.
   */
  actions?: unknown[]
  wake?: unknown[]
  on_failure?: Record<string, unknown>
}

/** The flat request body (`pageWriteRequest`). */
export interface PageWriteBody {
  slug: string
  name: string
  description?: string
  panels: PageWritePanel[]
  /** Accepted by `POST` (`owner: crew/<slug>` hands the page to a crew) but
   *  never sent from here: `pages.Metadata` has no owner field, so the
   *  document cannot express one, and inventing a key for it would make the
   *  editor's document something `crewship page create` would reject
   *  (`ParseDocument` decodes with `KnownFields(true)`). A page authored here
   *  is owned by its creator, exactly as one authored through the CLI is. */
  owner?: string
}

/**
 * The `PATCH` body, which is the write body with `panels` OPTIONAL.
 *
 * `pageWriteRequest.Panels` is a nil-able slice and the handler only replaces
 * the stored panel set `if req.Panels != nil` (`pages_handler.go:672-678`).
 * That is the difference between "these are the panels" and "I did not touch
 * the panels", and it is the only thing that makes a metadata-only save from
 * this editor lossless: the read path does not echo `actions` / `wake` /
 * `on_failure` (`pages_handler.go:132-149` says so in as many words), so a
 * panel list rebuilt from a GET cannot restate them, and sending that list back
 * would delete every gate on the page. Renaming a page must not disarm it.
 */
export type PagePatchBody = Omit<PageWriteBody, "panels"> & { panels?: PageWritePanel[] }

const UNITS: Record<string, number> = {
  ns: 1e-9,
  us: 1e-6,
  // Go accepts the micro sign as well as "us"; a duration pasted out of a Go
  // program should not stop being a duration on the way into the buffer.
  "µs": 1e-6,
  "μs": 1e-6,
  ms: 1e-3,
  s: 1,
  m: 60,
  h: 3600,
}

const DURATION_PART = /([0-9]+(?:\.[0-9]+)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h)/gy

/**
 * `sla: 5m` → 300. The reader for the sugar half of §11b decision 3.
 *
 * Mirrors Go's `time.ParseDuration` closely enough for anything a human
 * writes — a sequence of decimal-and-unit pairs, optionally signed — and then
 * TRUNCATES to whole seconds, which is what `cmd_page.go` does with
 * `int(sla.Seconds())`. Truncation matters at the bottom: `500ms` becomes `0`
 * here as it does through the CLI, and the server refuses both identically
 * rather than one of the two silently becoming a one-second SLA.
 *
 * Returns null for anything it cannot read, so the caller can OMIT the field
 * and let the server say why, rather than send a number nobody typed.
 */
export function slaSecondsFrom(value: unknown): number | null {
  if (typeof value === "number") {
    return Number.isFinite(value) ? Math.trunc(value) : null
  }
  if (typeof value !== "string") return null
  const raw = value.trim()
  if (raw === "") return null
  // "0" alone is the one unitless duration Go accepts.
  if (/^[-+]?0+$/.test(raw)) return 0

  let sign = 1
  let rest = raw
  if (rest[0] === "+" || rest[0] === "-") {
    if (rest[0] === "-") sign = -1
    rest = rest.slice(1)
  }
  if (rest === "") return null

  DURATION_PART.lastIndex = 0
  let seconds = 0
  let matched = 0
  for (;;) {
    const m = DURATION_PART.exec(rest)
    if (!m) break
    seconds += Number(m[1]) * UNITS[m[2]]
    matched = DURATION_PART.lastIndex
  }
  // Anything left over is not a duration — "5 minutes", "1hour", "5m junk".
  if (matched !== rest.length) return null
  return Math.trunc(sign * seconds)
}

/**
 * 300 → `5m`. The writer for the same sugar, used when a stored page is
 * rendered back into the document a human edits.
 *
 * Whole units only, largest first, so a round trip through the editor does not
 * quietly rewrite `1h` as `3600s`: an edit that changes bytes nobody touched
 * makes the version history unreadable (§10b.1 — every save is a version).
 */
export function formatSlaSeconds(seconds: number): string {
  const n = Math.trunc(seconds)
  if (!Number.isFinite(n) || n <= 0) return "0s"
  const h = Math.floor(n / 3600)
  const m = Math.floor((n % 3600) / 60)
  const s = n % 60
  let out = ""
  if (h > 0) out += `${h}h`
  if (m > 0) out += `${m}m`
  if (s > 0 || out === "") out += `${s}s`
  return out
}

export const PAGE_DOCUMENT_API_VERSION = "crewship/v1"
export const PAGE_DOCUMENT_KIND = "Page"

/** The starter document. Every field the authoring gate requires is present,
 *  and the ones the author has to decide are named in the comments, because a
 *  template that hid them would teach the reader that owner and producer are
 *  decoration.
 *
 *  The producer is `script/`, and that is the load-bearing choice rather than
 *  an arbitrary one. It used to be `routine/CHANGEME`, which could never be
 *  submitted: a routine producer must resolve to a routine that EXISTS, so
 *  pressing "Create page" on the untouched template answered
 *  `no such routine exists here` — a first run that fails on the default, and
 *  an error that teaches the template is broken rather than that the producer
 *  matters.
 *
 *  `script/` has no such lookup (there is no table of scripts, by design), and
 *  it is the one producer kind whose panel the page's own author may then
 *  write by hand. So the first page somebody makes is one they can immediately
 *  put a number on — with `crewship page set`, or from a script on their own
 *  machine — which is the whole of what a page is for. Until they do, the
 *  panel reads "never produced", which is the product's own words for "nobody
 *  wired this up" and is a better teacher than a 400. */
export function newPageTemplate(ownerRef?: string | null): string {
  const owner = ownerRef && ownerRef.startsWith("crew/") ? ownerRef : "crew/CHANGEME"
  return [
    "# A page renders the last payload a producer pushed. It holds no query, no",
    "# datasource and no credentials, so there are only three things to decide:",
    "#",
    "#   owner:     which crew may SEE a panel      (visibility)",
    "#   producer:  who may WRITE it                (authority)",
    "#   sla:       how long silence is allowed     (honesty)",
    "#",
    "# Everything below marked CHANGEME is yours to replace. Delete any comment",
    "# once you have decided — they are here to be read, not kept.",
    "",
    `apiVersion: ${PAGE_DOCUMENT_API_VERSION}`,
    `kind: ${PAGE_DOCUMENT_KIND}`,
    "metadata:",
    "  name: New page",
    "  slug: new-page",
    "  description: What this page is for",
    "spec:",
    "  panels:",
    "    - id: status",
    "",
    "      # WHAT SHAPE OF DATA. One of five, and the panel renders accordingly:",
    "      #   status.v1     a grid of named things, each ok | warning | critical",
    "      #   metric.v1     one number, with an optional delta and sparkline",
    "      #   series.v1     a chart; one unit for the whole panel",
    "      #   table.v1      declared columns and keyed rows",
    "      #   narrative.v1  typed prose blocks written by an agent or a script",
    "      schema: status.v1",
    "",
    "      title: Is it running?",
    "",
    "      # WHO MAY SEE IT. Always a crew — this is the permission anchor.",
    "      # A reader outside this crew gets a sealed placeholder, not an error,",
    "      # so the page has the same shape for everyone looking at it.",
    `      owner: ${owner}`,
    "",
    "      # WHO MAY WRITE IT. Four kinds; only two of them must already exist:",
    "      #   script/<any-name>   nothing has to exist. Anything holding a CLI",
    "      #                       token pushes it — a cron, your laptop, a CI",
    "      #                       step — and the page's OWNER may also write it",
    "      #                       by hand. Start here if you want to fill the",
    "      #                       page yourself.",
    "      #   webhook/<any-name>  nothing has to exist. Mint a token with",
    "      #                       `crewship page webhook create` and hand it to",
    "      #                       a system that cannot run the CLI.",
    "      #   routine/<slug>      the routine MUST exist. It writes the panel",
    "      #                       when it runs. A human cannot write it by hand.",
    "      #   agent/<slug>        the agent MUST exist. It pushes from inside",
    "      #                       its crew container, through the sidecar.",
    "      producer: script/CHANGEME.sh",
    "",
    "      # HOW LONG SILENCE IS ALLOWED. Past this the panel reads `stale` and",
    "      # says so. There is no value meaning `never mind`: a panel that cannot",
    "      # go stale is a panel that can show yesterday's number as today's.",
    "      sla: 5m",
    "",
    "      # Width on a 12-column grid. 12 is full width.",
    "      span: 12",
    "",
    "      # Optional, and worth knowing they exist:",
    "      #   icon: memory          a glyph, from a closed set of thirteen",
    "      #   tab: Overview         put this panel on a named tab; one word per",
    "      #                         panel, no `tabs:` block, first use sets the",
    "      #                         order. No tab anywhere means no tab bar.",
    "      #   public: true          include this one panel in a published link",
    "",
    "    # Add as many panels as the page needs, up to 24.",
    "",
    "# WHO ELSE REACHES THIS PAGE is not part of this document — it is decided",
    "# after the page exists, in Settings or with `crewship page grant`:",
    "#",
    "#   read     may reach the page, and sees the panels their crew already",
    "#            admits. It does NOT unseal a panel: visibility stays crew",
    "#            membership, and a grant never widens it.",
    "#   produce  may push payloads into NAMED panels and nothing else. It does",
    "#            NOT imply read — a subject can write a panel whose contents it",
    "#            may not look at.",
    "#   write    may edit this document: add, remove and rearrange panels.",
    "#",
    "# Each can go to a user, a crew or an agent. Only a human may issue one,",
    "# and only the page's owner or a workspace admin — an agent holding `write`",
    "# can rebuild the page but can never widen who reaches it.",
    "",
  ].join("\n")
}

/** A panel the viewer may not see (§11b decision 14). It arrives as a
 *  placeholder with no schema, no producer and no SLA — there is nothing to
 *  render into the document, and a `PATCH` built from a document missing it
 *  would DELETE another crew's panel. */
function isSealed(panel: WirePanel): boolean {
  return (panel as { sealed?: boolean }).sealed === true
}

/** How many of this page's panels are sealed. Zero is the only number the
 *  editor can safely save. */
export function sealedPanelCount(page: WirePage | null | undefined): number {
  if (!page || !Array.isArray(page.panels)) return 0
  return page.panels.filter(isSealed).length
}

/** A wire panel, plus the two authored keys `WirePanel` has no field for
 *  because today's read path never sends them. Read defensively rather than
 *  assumed absent: the day `GET /api/v1/pages/{slug}` echoes the authored half
 *  to a caller who may edit the spec, this editor round-trips it without a
 *  second change, and until then these are simply undefined. */
type WirePanelAuthored = WirePanel & { wake?: unknown; on_failure?: unknown }

/** The authored keys that ride through this file verbatim, in the order
 *  `pageWritePanelJSON` declares them. Enumerated ONCE: every place that has to
 *  know the list reads it from here, so a fourth key is one edit and not a
 *  scavenger hunt through the two translations. */
const PASSTHROUGH_KEYS = ["actions", "wake", "on_failure"] as const
type PassthroughKey = (typeof PASSTHROUGH_KEYS)[number]

/** `on_failure` is a mapping; the other two are lists. This is the ONLY thing
 *  either translation checks about them — the vocabulary inside is the
 *  server's, and re-deciding here what a valid `when:` is would be a second
 *  grammar that can disagree with the one that matters (§10b.1). */
const PASSTHROUGH_IS_LIST: Record<PassthroughKey, boolean> = {
  actions: true,
  wake: true,
  on_failure: false,
}

function passthroughShapeOK(key: PassthroughKey, value: unknown): boolean {
  if (value == null || typeof value !== "object") return false
  return PASSTHROUGH_IS_LIST[key] === Array.isArray(value)
}

/**
 * A stored page, rendered as the document a human edits.
 *
 * The key order is the PRD's example order (§6), not the wire's: a document
 * read top to bottom should say what the page is before it says how wide the
 * third panel is.
 */
export function pageDocumentText(page: WirePage): string {
  const panels = Array.isArray(page.panels) ? page.panels : []
  const doc: Record<string, unknown> = {
    apiVersion: PAGE_DOCUMENT_API_VERSION,
    kind: PAGE_DOCUMENT_KIND,
    metadata: {
      name: page.name ?? page.slug ?? "",
      slug: page.slug ?? "",
      ...(page.description ? { description: page.description } : {}),
    },
    spec: {
      panels: panels.filter((p) => !isSealed(p)).map((p) => {
        const sla = typeof p.sla_seconds === "number" ? p.sla_seconds : slaSecondsFrom(p.sla)
        const out: Record<string, unknown> = {
          id: p.id ?? "",
          schema: p.schema ?? "",
        }
        if (p.title) out.title = p.title
        // Before `owner`, because a document is read top to bottom and these
        // two say what the panel IS — its glyph and the screen it lives on —
        // rather than who may touch it.
        if (typeof p.icon === "string" && p.icon.trim() !== "") out.icon = p.icon.trim()
        if (typeof p.tab === "string" && p.tab.trim() !== "") out.tab = p.tab.trim()
        out.owner = p.owner ?? ""
        out.producer = p.producer ?? ""
        // Directly under `producer`, where the PRD's own example puts it (§6):
        // it names what runs that producer, and the two are one sentence.
        if (typeof p.refresh === "string" && p.refresh.trim() !== "") out.refresh = p.refresh.trim()
        out.sla = formatSlaSeconds(sla ?? 0)
        if (typeof p.span === "number" && p.span > 0) out.span = p.span
        if ((p as { public?: boolean }).public) out.public = true
        // The authored half, when the wire carried it. Rendered from the same
        // list the parser reads, so the document and the request body can never
        // know about different keys.
        for (const key of PASSTHROUGH_KEYS) {
          const value = (p as WirePanelAuthored as Record<string, unknown>)[key]
          if (passthroughShapeOK(key, value)) out[key] = value
        }
        return out
      }),
    },
  }
  return stringifyYaml(doc, { indent: 2, lineWidth: 0, blockQuote: "literal" })
}

export type PageBufferResult =
  | { ok: true; body: PageWriteBody }
  | { ok: false; message: string; line?: number }

function asString(value: unknown): string {
  if (typeof value === "string") return value
  if (typeof value === "number" || typeof value === "boolean") return String(value)
  return ""
}

/**
 * Buffer → request body.
 *
 * It fails on exactly the things that make the translation IMPOSSIBLE — YAML
 * that does not parse, an envelope that is not ours, a `spec.panels` that is
 * not a list. Everything else is forwarded and refused (or accepted) by the
 * server, which is the only party that can check that `crew/lookout` exists.
 *
 * The envelope check is not a client-side second opinion: `apiVersion` and
 * `kind` never reach the server, because the CLI and this editor both flatten
 * the document before sending it (§11b decision 2). Unchecked here they would
 * be silently ignored, so a routine DSL pasted into this editor would be sent
 * as a page. The wording is the server's own (`internal/pages/spec.go`).
 */
export function parsePageBuffer(text: string): PageBufferResult {
  const parsed = parseDocument(text, { prettyErrors: true })
  const fatal = parsed.errors[0]
  if (fatal) {
    return {
      ok: false,
      message: fatal.message,
      line: fatal.linePos?.[0]?.line,
    }
  }
  const value = parsed.toJS({ maxAliasCount: 100 }) as unknown
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return {
      ok: false,
      message:
        "a page document is a mapping with apiVersion, kind, metadata and spec — see docs/guides/pages.mdx",
    }
  }
  const root = value as Record<string, unknown>
  if (root.apiVersion !== PAGE_DOCUMENT_API_VERSION) {
    return {
      ok: false,
      message: `apiVersion ${JSON.stringify(root.apiVersion ?? "")}; want "${PAGE_DOCUMENT_API_VERSION}"`,
    }
  }
  if (root.kind !== PAGE_DOCUMENT_KIND) {
    return {
      ok: false,
      message: `kind ${JSON.stringify(root.kind ?? "")}; want "${PAGE_DOCUMENT_KIND}"`,
    }
  }
  const metadata = root.metadata
  if (metadata != null && (typeof metadata !== "object" || Array.isArray(metadata))) {
    return { ok: false, message: "metadata is a mapping of name, slug and description" }
  }
  const meta = (metadata ?? {}) as Record<string, unknown>

  const spec = root.spec
  if (spec != null && (typeof spec !== "object" || Array.isArray(spec))) {
    return { ok: false, message: "spec is a mapping whose only key is panels" }
  }
  const rawPanels = (spec as Record<string, unknown> | undefined)?.panels
  if (rawPanels != null && !Array.isArray(rawPanels)) {
    return { ok: false, message: "spec.panels is a list of panels" }
  }
  const list = (rawPanels ?? []) as unknown[]

  const panels: PageWritePanel[] = []
  for (let i = 0; i < list.length; i += 1) {
    const entry = list[i]
    if (!entry || typeof entry !== "object" || Array.isArray(entry)) {
      return { ok: false, message: `spec.panels[${i}] is not a panel mapping` }
    }
    const p = entry as Record<string, unknown>
    const panel: PageWritePanel = {
      id: asString(p.id),
      schema: asString(p.schema),
      owner: asString(p.owner),
      producer: asString(p.producer),
    }
    if (p.title != null && asString(p.title) !== "") panel.title = asString(p.title)
    // The two authored scalars, sent back exactly as they were rendered. Not
    // validated here: the icon set is closed server-side and the tab name has
    // its own refusals (blank, absurdly long, two that differ only by case),
    // and re-deciding either here would be a second grammar that can disagree
    // with the one that matters (§10b.1).
    if (asString(p.icon) !== "") panel.icon = asString(p.icon)
    if (asString(p.tab) !== "") panel.tab = asString(p.tab)
    // The refresh trigger, on the same terms: the set is closed server-side and
    // its refusals name the vocabulary, the producer kinds it can run and the
    // gate it needs to fire from. None of that is re-decided here.
    if (asString(p.refresh) !== "") panel.refresh = asString(p.refresh)
    // `sla_seconds` is honoured when someone writes the canonical form
    // directly — a document round-tripped out of `crewship page get -f json`
    // carries it — and `sla` is the sugar. Neither is invented when both are
    // missing: §4 says there is no SLA that means "never mind", and the
    // server is the one that gets to say so.
    const sla =
      p.sla_seconds !== undefined ? slaSecondsFrom(p.sla_seconds) : slaSecondsFrom(p.sla)
    if (sla !== null) panel.sla_seconds = sla
    if (typeof p.span === "number" && Number.isFinite(p.span)) panel.span = Math.trunc(p.span)
    if (p.public === true) panel.public = true
    // The sensor half and the buttons, forwarded verbatim (§5, §8b.1, §4
    // rule 4). Dropping them here is what made a title edit delete a page's
    // wake gates: the server replaces `spec.panels` with what it is sent and
    // reconciles `automations` against the result, so "not mentioned" reads as
    // "removed". The only thing checked is the container kind, because a
    // `wake:` that is a string cannot be translated at all, and §6 asks for a
    // loud failure rather than a quiet one.
    for (const key of PASSTHROUGH_KEYS) {
      const value = p[key]
      if (value == null) continue
      if (!passthroughShapeOK(key, value)) {
        return {
          ok: false,
          message: PASSTHROUGH_IS_LIST[key]
            ? `spec.panels[${i}].${key} is a list`
            : `spec.panels[${i}].${key} is a mapping`,
        }
      }
      // Assigned, not re-encoded: the vocabulary belongs to the server, and a
      // field-by-field copy here would be the place the two drift.
      ;(panel as unknown as Record<string, unknown>)[key] = value
    }
    panels.push(panel)
  }

  return {
    ok: true,
    body: {
      slug: asString(meta.slug),
      name: asString(meta.name),
      description: asString(meta.description),
      panels,
    },
  }
}

// ── "I did not touch the panels" ───────────────────────────────────────────

/** The stored page's panels, in the wire shape a save would send them in.
 *  Null when there is no stored page, or when the document rendered from it
 *  does not parse — in which case nothing can be proven and the panel list is
 *  sent, which is the status quo and the safe direction to be wrong in. */
function storedPanelsOf(page: WirePage | null | undefined): PageWritePanel[] | null {
  if (!page) return null
  const parsed = parsePageBuffer(pageDocumentText(page))
  return parsed.ok ? parsed.body.panels : null
}

/**
 * Would this save change the panel list at all?
 *
 * Compared as JSON, in the WIRE shape, because that is the only comparison that
 * answers the question being asked — "is the array I am about to send the array
 * the server already has?". Both sides come out of `parsePageBuffer`, so
 * cosmetic differences (key order, quoting, comments, `1h` vs `3600s`) are
 * normalised away before the comparison and do not count as a change.
 *
 * The failure direction matters: this can only ever report a change that is not
 * one, never miss one. A false "changed" costs a redundant panel write; a false
 * "unchanged" would skip a write the author asked for.
 */
export function panelsDifferFromStored(
  page: WirePage | null | undefined,
  panels: PageWritePanel[],
): boolean {
  const stored = storedPanelsOf(page)
  if (stored === null) return true
  return JSON.stringify(stored) !== JSON.stringify(panels)
}

/**
 * The body a `PATCH` should carry — the parsed buffer, minus `panels` when the
 * buffer's panels are the stored ones.
 *
 * This is the fix for the save that renamed a page and disarmed it. The editor
 * cannot restate a stored page's `wake:`, `actions:` or `on_failure:` — the
 * read path does not send them (`pages_handler.go:132-149`) — so the panel list
 * it can build is a LOSSY copy of the stored one. Sending a lossy copy of
 * something nobody edited is pure downside: `pages_handler.go:672-678` skips
 * the replacement entirely when `panels` is absent, and the stored spec, gates
 * and all, is what gets re-validated and re-versioned.
 *
 * When the panels DID change the list is sent, because that is what was asked
 * for — and the editor says out loud what that costs (see the banner below).
 */
export function pagePatchBody(page: WirePage | null | undefined, body: PageWriteBody): PagePatchBody {
  if (!panelsDifferFromStored(page, body.panels)) {
    const { panels: _panels, ...rest } = body
    return rest
  }
  return body
}

// ── Inline diagnostics (additive, never a gate) ────────────────────────────

export interface PageDiagnostic {
  /** 1-indexed. */
  line: number
  severity: "warning"
  message: string
}

function lineAt(text: string, offset: number): number {
  let line = 1
  const end = Math.min(offset, text.length)
  for (let i = 0; i < end; i += 1) if (text[i] === "\n") line += 1
  return line
}

function nodeLine(text: string, node: unknown, fallback: number): number {
  const range = (node as { range?: [number, number, number] } | null)?.range
  return range ? lineAt(text, range[0]) : fallback
}

const SCHEMA_SET: ReadonlySet<string> = new Set<string>(PANEL_SCHEMAS)
const PRODUCER_KINDS = ["routine", "script", "agent", "webhook"] as const

/**
 * What the buffer can be told about itself without asking the server.
 *
 * Every diagnostic here is a WARNING and none of them disables Save. The
 * server owns the verdict (§10b.1), and a client that refuses to send a
 * document the server would have accepted is a gate nobody voted for — the
 * closed schema set moves in a server release, and this list would be the last
 * thing to hear about it.
 */
export function pageDiagnostics(text: string): PageDiagnostic[] {
  const doc = parseDocument(text)
  if (doc.errors.length > 0) return []
  const out: PageDiagnostic[] = []
  const panels = doc.getIn(["spec", "panels"], true)
  if (!isSeq(panels)) return out

  panels.items.forEach((item: unknown) => {
    if (!isMap(item)) return
    const at = (key: string) => nodeLine(text, item.get(key, true) as Node | null, nodeLine(text, item, 1))
    const value = (key: string) => {
      const v = item.get(key, false)
      return v == null ? undefined : v
    }

    const schema = value("schema")
    if (typeof schema === "string" && schema !== "" && !SCHEMA_SET.has(schema)) {
      out.push({
        line: at("schema"),
        severity: "warning",
        message: `unknown schema "${schema}" — the set is closed: ${PANEL_SCHEMAS.join(", ")}`,
      })
    }

    const owner = value("owner")
    if (typeof owner === "string" && owner !== "" && !owner.startsWith("crew/")) {
      out.push({
        line: at("owner"),
        severity: "warning",
        message: `owner "${owner}" must be crew/<slug>: a panel's permission anchor is a crew`,
      })
    }

    const producer = value("producer")
    if (typeof producer === "string" && producer !== "") {
      const kind = producer.split("/")[0]
      if (!PRODUCER_KINDS.includes(kind as (typeof PRODUCER_KINDS)[number])) {
        out.push({
          line: at("producer"),
          severity: "warning",
          message: `producer kind "${kind}" is not one of ${PRODUCER_KINDS.join(", ")} — a page holds no query and no datasource`,
        })
      }
    }

    const sla = value("sla")
    const slaSeconds = value("sla_seconds")
    const resolved = slaSeconds !== undefined ? slaSecondsFrom(slaSeconds) : slaSecondsFrom(sla)
    if (resolved === null || resolved <= 0) {
      out.push({
        line: sla !== undefined || slaSeconds !== undefined ? at(sla !== undefined ? "sla" : "sla_seconds") : at("id"),
        severity: "warning",
        message:
          sla === undefined && slaSeconds === undefined
            ? "no sla — §4 makes it mandatory; there is no default that means 'never mind' (try 30s, 5m, 1h)"
            : `sla ${JSON.stringify(sla ?? slaSeconds)} is not a duration (try 30s, 5m, 1h)`,
      })
    }
  })
  return out
}

/** The linter, mapped onto whole lines — a squiggle under a guessed substring
 *  reads as precision the diagnostic does not have (the routine editor's
 *  `dslLinter` makes the same call). */
export function pageEditorExtensions(): Extension[] {
  return [
    linter((view: EditorView): Diagnostic[] => {
      const text = view.state.doc.toString()
      return pageDiagnostics(text).map((d) => {
        const lineNo = Math.min(Math.max(d.line, 1), view.state.doc.lines)
        const line = view.state.doc.line(lineNo)
        return { from: line.from, to: line.to, severity: d.severity, message: d.message }
      })
    }),
  ]
}

// ── The component ──────────────────────────────────────────────────────────

export type PageEditorMode = "create" | "edit"

export interface PageEditorProps {
  workspaceId: string
  mode: PageEditorMode
  /** The page being edited, as it came off the wire. Null when creating. */
  page?: WirePage | null
  /** Seeds the template's panel owner so the first page is one keystroke
   *  closer to valid. Ignored unless it is a `crew/<slug>` reference. */
  defaultOwner?: string | null
  onClose: () => void
  /** Fires only after the server accepted the write, with the saved slug. */
  onSaved?: (slug: string) => void
}

interface SaveVariables {
  method: "POST" | "PATCH"
  /** The page's address — the PATCH URL's slug, which is the page being
   *  edited and never the one in the buffer. Renaming through a PATCH is the
   *  server's refusal to make, and it makes it well. */
  slug: string
  /** `PageWriteBody` on create (`panels` is the page); `PagePatchBody` on
   *  edit, where an untouched panel list is omitted rather than resent. */
  body: PagePatchBody
}

export function PageEditor({
  workspaceId,
  mode,
  page,
  defaultOwner,
  onClose,
  onSaved,
}: PageEditorProps) {
  const sealed = sealedPanelCount(page)

  const initial = React.useMemo(() => {
    if (mode === "edit" && page) return pageDocumentText(page)
    return newPageTemplate(defaultOwner)
  }, [mode, page, defaultOwner])

  // `text` is what the editor is CONSTRUCTED from and must never track typing:
  // FileEditor rebuilds its EditorState when `code` changes, and a rebuild puts
  // the caret back at position 0. `liveText` mirrors the buffer for everything
  // derived from it. This is the shape routine-editor-remount.test.tsx pins,
  // and it is pinned because getting it wrong shreds a document in seconds.
  const [text, setText] = React.useState(initial)
  const [liveText, setLiveText] = React.useState(initial)
  const bufferRef = React.useRef(initial)
  const [editorKey, setEditorKey] = React.useState(0)
  const [dirty, setDirty] = React.useState(false)
  const saveRef = React.useRef<(() => void) | null>(null)

  // Re-seed when the editor is pointed at a different document. Keyed on the
  // rendered text rather than on `page` so a realtime refetch that changed
  // nothing does not blow away an edit in progress.
  React.useEffect(() => {
    setText(initial)
    setLiveText(initial)
    bufferRef.current = initial
    setDirty(false)
    setEditorKey((k) => k + 1)
  }, [initial])

  /** The server's refusal, shown in its own words. Cleared when the buffer
   *  changes, because a message about a document that no longer exists is
   *  worse than no message. */
  const [refusal, setRefusal] = React.useState<string | null>(null)

  const validation = React.useMemo(() => parsePageBuffer(liveText), [liveText])

  /** True when this save would REWRITE the stored panel list rather than leave
   *  it alone. Only then is the authored half the editor cannot see at risk. */
  const panelsRewritten = React.useMemo(
    () => mode === "edit" && validation.ok && panelsDifferFromStored(page, validation.body.panels),
    [mode, page, validation],
  )
  const extraExtensions = React.useMemo(() => pageEditorExtensions(), [])
  const warnings = React.useMemo(() => pageDiagnostics(liveText), [liveText])

  const save = useApiMutation<SaveVariables, { slug?: string } | undefined>({
    request: (v) => {
      const qs = `?workspace_id=${encodeURIComponent(workspaceId)}`
      const input =
        v.method === "POST"
          ? `/api/v1/pages${qs}`
          : `/api/v1/pages/${encodeURIComponent(v.slug)}${qs}`
      return {
        input,
        init: {
          method: v.method,
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(v.body),
        },
      }
    },
    // Only ever reached from a response the server accepted — useApiMutation
    // has no path from a refusal to this list (#1563 rules 1 and 3).
    invalidateKeys: [pagesKeys.all(workspaceId)],
    onOk: (data, v) => {
      const slug = (data && typeof data.slug === "string" && data.slug) || v.body.slug || v.slug
      toast.success(v.method === "POST" ? `Page ${slug} created` : `Page ${slug} saved`, {
        description: "Every save is a version — roll one back with crewship page rollback.",
      })
      setRefusal(null)
      setDirty(false)
      onSaved?.(slug)
      onClose()
    },
    onAlreadyRunning: (outcome) => {
      // Not a failure and not a save: nothing changed, so nothing closes.
      setRefusal(outcome.message)
    },
    onError: (err) => {
      // Rule 2: the server's own words. Rule 4: a transport failure is a
      // different sentence, because "failed to fetch" is not a refusal and
      // telling someone their spec was rejected when the network dropped
      // sends them to edit a document that was never read.
      const message =
        err instanceof ApiMutationError
          ? err.message
          : err instanceof Error
            ? `Could not reach the server: ${err.message}`
            : "Could not reach the server"
      setRefusal(message)
      toast.error(message)
      // Rule 3: the buffer is NOT touched. What was typed is what a retry
      // needs, and the editor stays open holding it.
    },
  })

  // Closing with unsaved work asks first. The rest of this file goes to some
  // length not to destroy a buffer a retry needs (#1563 rule 3); a stray click
  // on the backdrop would undo all of it, and the misclick is the likeliest
  // way to lose a spec that was never on disk.
  const [confirmDiscard, setConfirmDiscard] = React.useState(false)
  const requestClose = () => {
    // `dirty || save.isPending`, not `dirty && !save.isPending`.
    //
    // The guard was skipped during a save — the one moment it is most needed.
    // The Save button is disabled while pending, but the backdrop and Cancel
    // are not, so: type a spec, Save, click Cancel, and the editor unmounts
    // with the request still in flight. If the server then refuses it,
    // setRefusal runs on an unmounted component (a no-op) and the buffer the
    // author would have edited is gone — the #1563 rule this block cites.
    if (dirty || save.isPending) {
      setConfirmDiscard(true)
      return
    }
    onClose()
  }

  const handleDocChange = (next: string) => {
    bufferRef.current = next
    setLiveText(next)
    if (refusal) setRefusal(null)
  }

  const handleEditorSave = (next: string) => {
    bufferRef.current = next
    setLiveText(next)
  }

  const handleSave = () => {
    // Pull the latest doc out of CodeMirror first: FileEditor only hands its
    // buffer back through onSave, and reading React state here would send the
    // value from before the last keystroke.
    saveRef.current?.()
    const parsed = parsePageBuffer(bufferRef.current)
    if (!parsed.ok) {
      setRefusal(parsed.line ? `line ${parsed.line}: ${parsed.message}` : parsed.message)
      return
    }
    setRefusal(null)
    if (mode === "create") {
      save.mutate({ method: "POST", slug: parsed.body.slug, body: parsed.body })
      return
    }
    // §10b.1: every save is a version, and the server writes one on PATCH.
    // A delete-and-recreate would produce a page with no history and a new id,
    // and would drop every grant on it.
    //
    // `pagePatchBody` drops `panels` when nothing in the grid moved, so
    // renaming a page cannot delete its wake gates.
    save.mutate({
      method: "PATCH",
      slug: page?.slug ?? parsed.body.slug,
      body: pagePatchBody(page, parsed.body),
    })
  }

  const title = mode === "create" ? "New page" : `Edit ${page?.slug ?? "page"}`

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 md:p-8">
      <button
        type="button"
        aria-label="Close the page editor"
        onClick={requestClose}
        className="absolute inset-0 bg-background/70 backdrop-blur-md"
      />
      <div
        role="dialog"
        aria-label={title}
        className="relative flex h-full max-h-[92vh] w-full max-w-[1100px] flex-col overflow-hidden rounded-xl border border-border/60 bg-card shadow-2xl"
      >
        <div className="flex shrink-0 items-center justify-between gap-3 border-b border-border/60 bg-card/30 px-4 py-2.5">
          <div className="type-page-meta flex items-center gap-2.5 text-muted-foreground">
            <span className="font-medium text-foreground">{title}</span>
            <span className="opacity-60">·</span>
            {/* The document, named. Three doors onto it (§10b.1), and the other
                two are a file on disk — saying which document this is makes
                the CLI the obvious next step rather than a separate world. */}
            <span className="type-page-stamp">kind: Page</span>
            {dirty && (
              <span className="type-page-meta inline-flex items-center gap-1.5 rounded-full bg-warn/20 px-2.5 py-0.5 font-medium text-warn">
                <span className="h-1.5 w-1.5 rounded-full bg-current" />
                unsaved
              </span>
            )}
          </div>
          <div className="flex items-center gap-1.5">
            <Button
              size="sm"
              variant="ghost"
              onClick={requestClose}
              className="h-8 gap-1.5 px-2.5 text-xs"
            >
              <X className="h-3.5 w-3.5" />
              Cancel
            </Button>
            <Button
              size="sm"
              variant="default"
              onClick={handleSave}
              disabled={save.isPending || sealed > 0}
              className="h-8 gap-1.5 px-3 text-xs font-semibold"
              title={
                sealed > 0
                  ? "This page has panels you may not see; saving would delete them."
                  : "Save. The server validates the spec and checks that every producer and owner resolves."
              }
            >
              {save.isPending ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <Save className="h-3.5 w-3.5" />
              )}
              {save.isPending ? "Saving…" : mode === "create" ? "Create page" : "Save"}
            </Button>
          </div>
        </div>

        {/* §11b decision 14 in the one place it can cost data: the document
            cannot represent a sealed panel, so a PATCH built from it would
            delete another crew's panel without either of them being told. */}
        {sealed > 0 && (
          <div className="shrink-0 border-b border-destructive/30 bg-destructive/[0.06] px-4 py-2.5 type-page-value text-destructive">
            <div className="flex items-start gap-2">
              <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
              <span>
                {sealed} panel{sealed === 1 ? "" : "s"} on this page{" "}
                {sealed === 1 ? "is" : "are"} owned by a crew you are not in. Saving from here
                would delete {sealed === 1 ? "it" : "them"}, so this page is editable only by
                someone who can see all of it.
              </span>
            </div>
          </div>
        )}

        {/* The half of §11b decision 14's problem that has no placeholder to
            show for it. A panel's `actions:`, `wake:` and `on_failure:` are
            authored here but never echoed by the read path
            (pages_handler.go:132-149), so this document cannot restate them and
            cannot even tell you which panels have them. A save that leaves the
            panel list alone is lossless — pagePatchBody omits it — but one that
            rewrites it deletes whatever this buffer does not say, including the
            automations rows a wake gate compiles to. Said out loud rather than
            blocked: unlike a sealed panel, there is no evidence here to block
            ON, and a rule that fired on every grid edit of every page would
            embargo in-app editing over a risk most pages do not carry. */}
        {panelsRewritten && sealed === 0 && (
          <div
            data-testid="page-editor-panels-rewritten"
            role="status"
            className="shrink-0 border-b border-warn/30 bg-warn/[0.06] px-4 py-2.5 type-page-value text-warn"
          >
            <div className="flex items-start gap-2">
              <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
              <span>
                This save replaces the panel list. A panel&apos;s{" "}
                <span className="type-page-stamp">actions</span>,{" "}
                <span className="type-page-stamp">wake</span> and{" "}
                <span className="type-page-stamp">on_failure</span> are not sent back by the
                server when a page is read, so they are missing from this document unless you write
                them — and anything this buffer does not restate is deleted, along with the wake
                rules it compiled to. Editing only the name or description leaves the panels, and
                their gates, untouched.
              </span>
            </div>
          </div>
        )}

        {confirmDiscard && (
          <div
            role="alert"
            className="flex shrink-0 items-center gap-3 border-b border-warn/30 bg-warn/[0.06] px-4 py-2.5 type-page-value text-warn"
          >
            <span className="flex-1">
              This document has unsaved changes. Nothing has been sent to the server yet.
            </span>
            <Button
              size="sm"
              variant="ghost"
              className="h-7 px-2.5 text-xs"
              onClick={() => setConfirmDiscard(false)}
            >
              Keep editing
            </Button>
            <Button size="sm" variant="ghost" className="h-7 px-2.5 text-xs" onClick={onClose}>
              Discard
            </Button>
          </div>
        )}

        {/* The server's refusal, verbatim. Its wording is the product — the
            missing-SLA one quotes §4 — and paraphrasing it here would replace
            an explanation with a restatement. */}
        {refusal && (
          <div
            data-testid="page-editor-refusal"
            role="alert"
            className="shrink-0 border-b border-destructive/30 bg-destructive/[0.06] px-4 py-2.5 type-page-value text-destructive"
          >
            <div className="flex items-start gap-2">
              <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
              <span className="font-mono">{refusal}</span>
            </div>
          </div>
        )}

        <div className="flex-1 overflow-hidden">
          <FileEditor
            key={`page-editor-${editorKey}`}
            code={text}
            language="yaml"
            onSave={handleEditorSave}
            onDirtyChange={setDirty}
            saveRef={saveRef}
            onDocChange={handleDocChange}
            extraExtensions={extraExtensions}
          />
        </div>

        {/* One line, and only when it has something to say. The warnings are
            hints from the closed vocabulary; the verdict is the server's. */}
        {validation.ok && warnings.length > 0 && (
          <div className="shrink-0 border-t border-warn/30 bg-warn/[0.06] px-4 py-2 type-page-meta text-warn">
            {warnings.length} thing{warnings.length === 1 ? "" : "s"} the schema would question —
            hover the marked lines. The server has the final say.
          </div>
        )}
        {!validation.ok && (
          <div className="shrink-0 border-t border-destructive/30 bg-destructive/[0.06] px-4 py-2 type-page-stamp text-destructive">
            {validation.line ? `line ${validation.line}: ` : ""}
            {validation.message}
          </div>
        )}
      </div>
    </div>
  )
}
