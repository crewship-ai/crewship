/**
 * Chat-surface interaction events — the measurement the `/chat` surface
 * shipped without.
 *
 * ## This is NOT crash reporting
 *
 * `crewship telemetry on|off`, `docs/guides/telemetry` and
 * `sentry.client.config.ts` are about anonymous **crash reports**, which
 * leave the machine (consent-gated, off by default on stable builds).
 * Same English word, different concern — see `docs/guides/chat-telemetry`.
 *
 * This module has **no network transport of its own**. It records into a
 * bounded in-memory ring buffer and, if a host has registered one, hands
 * each event to a sink. Nothing here calls `fetch`. That is deliberate:
 * `PRIVACY.md` promises "no usage analytics or product metrics" leave the
 * install, and the cheapest way to keep a promise is to make the code
 * incapable of breaking it.
 *
 * ## Why not journal entries
 *
 * The journal (`internal/journal`) is written exclusively by Go server
 * code; the browser can only read `/api/v1/journal`. Routing UI
 * impressions into the audit backbone would mean letting the client
 * assert rows into the table the product audits through, at one durable,
 * FTS-indexed, SSE-broadcast, backed-up row per chip *impression*. A chip
 * being displayed is not an audited fact. See the guide for the full
 * argument.
 *
 * ## The privacy guarantee is structural
 *
 * A payload value can only be a number, a boolean, a member of a declared
 * enum, or an id-shaped token (no whitespace, ≤ 64 chars). There is no
 * "free string" field kind, so there is no shape in which a message, a
 * form answer, a filename or a search query can be expressed. `emitChatEvent`
 * drops anything that does not fit rather than passing it through.
 * `lib/__tests__/telemetry.test.ts` sweeps the whole vocabulary and proves it.
 *
 * ## Reading what was recorded
 *
 * A buffer nobody can read answers no question, so in development this
 * module binds a reader to `window.__CREWSHIP_CHAT_TELEMETRY__` — see
 * `installChatTelemetryDevBridge` below and `docs/guides/chat-telemetry`.
 * It is absent from production builds, it hands out copies of the same
 * sanitised events, and it adds no transport: the vocabulary sweep is run
 * a second time through it in `lib/__tests__/telemetry-dev-bridge.test.ts`.
 */

import { devWarn } from "@/lib/client-log"

/* ------------------------------------------------------------------ *
 *  Field kinds — the only four shapes a payload value may take
 * ------------------------------------------------------------------ */

export type FieldSpec =
  /** An opaque identifier: `^[A-Za-z0-9_.:-]{1,64}$`. No spaces, so prose cannot pass. */
  | { kind: "id"; optional?: true }
  /** A finite number: counts, durations, byte sizes, positions. */
  | { kind: "num"; optional?: true }
  | { kind: "bool"; optional?: true }
  /** A closed set written down here. Anything else is dropped. */
  | { kind: "enum"; values: readonly string[]; optional?: true }

type EventSpec = Record<string, FieldSpec>

// `@` is deliberately absent. It was in this class for one draft, and the
// vocabulary sweep in lib/__tests__/telemetry.test.ts immediately smuggled
// "jana@example.com" through an id field: an email has no whitespace and is
// well under 64 characters, so it is perfectly id-shaped. No identifier in
// this product needs an `@`, and a charset that cannot express an address is
// worth more than a rule saying nobody should put one there.
const ID_RE = /^[A-Za-z0-9_.:-]{1,64}$/

/* ------------------------------------------------------------------ *
 *  The vocabulary — one place owns it
 * ------------------------------------------------------------------ */

/**
 * Shared enums, named once so two events cannot drift apart on the same
 * concept.
 */
const CHIP_KIND = ["question", "form"] as const
const CHIP_SOURCE = ["pack", "fallback", "followup"] as const
const ATTACHMENT_SOURCE = ["picker", "drop", "paste", "camera"] as const
const MIME_KIND = ["image", "pdf", "text", "audio", "archive", "other"] as const
const APPROVAL_KIND = ["approval", "escalation"] as const

export const CHAT_EVENT_SCHEMA = {
  /* ---- Ask chips (PRD §12) ------------------------------------- */

  /** A chip was rendered. Deduped per session+chip via `emitChatEventOnce`. */
  ask_chip_shown: {
    session_id: { kind: "id", optional: true },
    agent_id: { kind: "id", optional: true },
    chip_id: { kind: "id" },
    /** question vs form — the distinction the whole feature turns on. */
    chip_kind: { kind: "enum", values: CHIP_KIND },
    position: { kind: "num" },
    source: { kind: "enum", values: CHIP_SOURCE },
  },

  ask_chip_clicked: {
    session_id: { kind: "id", optional: true },
    agent_id: { kind: "id", optional: true },
    chip_id: { kind: "id" },
    chip_kind: { kind: "enum", values: CHIP_KIND },
    position: { kind: "num" },
    source: { kind: "enum", values: CHIP_SOURCE },
  },

  /* ---- Ask forms ----------------------------------------------- */

  ask_form_opened: {
    session_id: { kind: "id", optional: true },
    agent_id: { kind: "id", optional: true },
    template_id: { kind: "id" },
    field_count: { kind: "num" },
  },

  ask_form_submitted: {
    session_id: { kind: "id", optional: true },
    template_id: { kind: "id" },
    field_count: { kind: "num" },
    /** How many fields had *something* in them. Never what. */
    filled_count: { kind: "num" },
    attachment_count: { kind: "num", optional: true },
    duration_ms: { kind: "num", optional: true },
  },

  ask_form_abandoned: {
    session_id: { kind: "id", optional: true },
    template_id: { kind: "id" },
    field_count: { kind: "num" },
    filled_count: { kind: "num" },
    /**
     * The *identifier* of the last field the user touched — the schema key
     * the pack author wrote, never the value the user typed. This is where
     * a bad form shows itself: everyone stops on the same field.
     */
    last_field_id: { kind: "id", optional: true },
    reason: { kind: "enum", values: ["dismissed", "cancelled", "navigated"] },
    duration_ms: { kind: "num", optional: true },
  },

  /* ---- Attachments --------------------------------------------- */

  attachment_uploaded: {
    session_id: { kind: "id", optional: true },
    /** Coarse class derived from the MIME type. Never the filename. */
    mime_kind: { kind: "enum", values: MIME_KIND },
    size_bytes: { kind: "num" },
    // Optional: a RETRY re-runs an upload whose original control is no longer
    // known, and inventing "picker" for it would quietly bias the split.
    source: { kind: "enum", values: ATTACHMENT_SOURCE, optional: true },
    duration_ms: { kind: "num", optional: true },
  },

  attachment_upload_failed: {
    session_id: { kind: "id", optional: true },
    mime_kind: { kind: "enum", values: MIME_KIND },
    size_bytes: { kind: "num" },
    source: { kind: "enum", values: ATTACHMENT_SOURCE, optional: true },
    /**
     * A classification, not the server's error body. Error bodies can echo
     * paths and SQL — the reason a failure is surfaced without naming
     * anything (see the composer's failure tests).
     */
    reason: {
      kind: "enum",
      values: ["http_error", "network", "too_large", "unsupported_type", "rate_limited", "unknown"],
    },
    status: { kind: "num", optional: true },
  },

  /* ---- Sessions ------------------------------------------------- */

  chat_session_created: {
    session_id: { kind: "id" },
    agent_id: { kind: "id", optional: true },
    source: {
      kind: "enum",
      values: ["sidebar", "chip", "palette", "composer", "home", "deeplink"],
    },
  },

  /** A session got a title. The title itself is derived from the first
   *  message, so it is content and it is not recorded. */
  chat_session_titled: {
    session_id: { kind: "id" },
    source: { kind: "enum", values: ["auto", "manual"] },
  },

  /* ---- ⌘K conversation search ----------------------------------- */

  /**
   * A search ran. Nothing about the search *terms* is recorded — not the
   * text, not its length. Result count and whether anything matched are
   * enough to see people searching and finding nothing.
   */
  conversation_search_run: {
    result_count: { kind: "num" },
    has_results: { kind: "bool" },
    source: { kind: "enum", values: ["palette", "sidebar"] },
  },

  conversation_search_result_opened: {
    session_id: { kind: "id", optional: true },
    /** Rank of the chosen result, 0-based. Tells you if ranking is wrong. */
    position: { kind: "num" },
    result_count: { kind: "num" },
    source: { kind: "enum", values: ["palette", "sidebar"] },
  },

  /* ---- Approvals and escalations in chat ------------------------ */

  chat_approval_shown: {
    session_id: { kind: "id", optional: true },
    approval_id: { kind: "id" },
    approval_kind: { kind: "enum", values: APPROVAL_KIND },
  },

  chat_approval_decided: {
    session_id: { kind: "id", optional: true },
    approval_id: { kind: "id" },
    approval_kind: { kind: "enum", values: APPROVAL_KIND },
    decision: { kind: "enum", values: ["approved", "denied", "dismissed"] },
    /** Time from the card appearing to the decision. */
    latency_ms: { kind: "num", optional: true },
  },
} as const satisfies Record<string, EventSpec>

/**
 * Emission order is the funnel order: chips → forms → attachments →
 * sessions → search → approvals. Keep it stable; the docs table and the
 * vocabulary test both read top to bottom.
 */
export const CHAT_EVENT_NAMES = Object.keys(CHAT_EVENT_SCHEMA) as readonly ChatEventName[]

export type ChatEventName = keyof typeof CHAT_EVENT_SCHEMA

type ValueOf<F> = F extends { kind: "id" }
  ? string
  : F extends { kind: "num" }
    ? number
    : F extends { kind: "bool" }
      ? boolean
      : F extends { kind: "enum"; values: readonly (infer V)[] }
        ? V
        : never

type RequiredKeys<S> = { [K in keyof S as S[K] extends { optional: true } ? never : K]: ValueOf<S[K]> }
type OptionalKeys<S> = { [K in keyof S as S[K] extends { optional: true } ? K : never]?: ValueOf<S[K]> }

export type ChatEventPayload<E extends ChatEventName> = RequiredKeys<(typeof CHAT_EVENT_SCHEMA)[E]> &
  OptionalKeys<(typeof CHAT_EVENT_SCHEMA)[E]>

export interface ChatEvent {
  name: ChatEventName
  /** Always flat and always primitive — enforced by `sanitize`. */
  payload: Record<string, string | number | boolean>
  /** `Date.now()` at emit. */
  ts: number
}

export type ChatTelemetrySink = (event: ChatEvent) => void

/* ------------------------------------------------------------------ *
 *  Runtime
 * ------------------------------------------------------------------ */

/** Bounded so a long-lived tab cannot grow without limit. */
const BUFFER_LIMIT = 500

/** Same, for the impression-dedupe keys. */
const DEDUPE_LIMIT = 2000

let sink: ChatTelemetrySink | null = null
let buffer: ChatEvent[] = []
let seenOnce = new Set<string>()

/**
 * Register the destination for events. Left unset, events only reach the
 * in-memory buffer — which is what ships today, and what Playwright reads.
 * A host that wants durability registers a sink here; that is the one line
 * that has to change, and it is deliberately the only place a network call
 * could ever be introduced.
 */
export function setChatTelemetrySink(next: ChatTelemetrySink | null): void {
  sink = next
}

/** Drop the sink, the buffer and the impression dedupe state. Tests use this. */
export function resetChatTelemetry(): void {
  sink = null
  buffer = []
  seenOnce = new Set<string>()
}

/** Read the buffer without clearing it. */
export function peekChatEvents(): readonly ChatEvent[] {
  return buffer.slice()
}

/** Read and clear the buffer. */
export function drainChatEvents(): ChatEvent[] {
  const out = buffer
  buffer = []
  return out
}

/* ------------------------------------------------------------------ *
 *  The reader
 * ------------------------------------------------------------------ */

/**
 * `peekChatEvents` and `drainChatEvents` were exports attached to nothing:
 * no console, no CLI and no UI could read a single event, which is how the
 * gap was found — somebody tried to demonstrate the funnel and had nowhere
 * to look.
 *
 * The reader is this object, bound to `window.__CREWSHIP_CHAT_TELEMETRY__`
 * in development. Both people who need to read events are sitting at the
 * machine that produced them — a developer demonstrating or debugging the
 * funnel, and a maintainer asking whether a surface is used at all — so
 * neither needs a request, a page or a build flag. The browser console is
 * already open, and `json()` is the pasteable form for a bug report.
 *
 * It reads the same sanitised buffer `emitChatEvent` writes, and hands out
 * copies: a console session cannot write back into it.
 */
export interface ChatTelemetryDevBridge {
  /** Everything buffered, oldest first. Copies — editing them changes nothing. */
  peek(): ChatEvent[]
  /** Same, and empties the buffer. */
  drain(): ChatEvent[]
  /** Forget the buffer and the impression dedupe — "clear, then click". */
  reset(): void
  /**
   * A count per declared event, zeros included. "Is this surface used at
   * all" is the question; an absent key would make *never used* and *never
   * declared* look the same.
   */
  summary(): Record<ChatEventName, number>
  /** The buffer as pasteable JSON. */
  json(): string
  names: readonly ChatEventName[]
  /** The vocabulary, so the console can say what a field is allowed to be. */
  schema: typeof CHAT_EVENT_SCHEMA
  /** Printed when the object is inspected — the reader documents itself. */
  help: string
}

declare global {
  interface Window {
    /**
     * Dev-only reader for the chat event buffer. Absent from production
     * builds — `installChatTelemetryDevBridge` returns before creating it
     * and the bundler folds the branch away.
     */
    __CREWSHIP_CHAT_TELEMETRY__?: ChatTelemetryDevBridge
  }
}

/** Anything the bridge can be attached to. `window`, in practice. */
type ChatTelemetryBridgeHost = { __CREWSHIP_CHAT_TELEMETRY__?: ChatTelemetryDevBridge }

const HELP = [
  "window.__CREWSHIP_CHAT_TELEMETRY__ — chat interaction events, development only.",
  "  .peek()     every buffered event, oldest first",
  "  .summary()  count per declared event, zeros included",
  "  .json()     the buffer as pasteable JSON",
  "  .drain()    peek + empty",
  "  .reset()    forget everything (buffer and impression dedupe)",
  "  .schema     what each payload field is allowed to be",
  "Nothing here leaves the machine: this module has no network transport.",
  "See docs/guides/chat-telemetry.",
].join("\n")

/** Copy an event so the caller holds no reference into the ring buffer. */
function copyEvent(e: ChatEvent): ChatEvent {
  return { name: e.name, ts: e.ts, payload: { ...e.payload } }
}

/**
 * Attach the reader to `host` (default: `window`).
 *
 * Returns whether it attached. Two ways it does not:
 *
 *  - **production.** The guard is a bare `process.env.NODE_ENV` comparison
 *    so Next inlines it and drops the branch — in a production bundle the
 *    binding does not exist, which is a stronger statement than a binding
 *    that exists and says it should not be used. Same mechanism as
 *    `devWarn` (lib/client-log.ts).
 *  - **no window.** Client components are rendered on the server too; the
 *    module-scope call below must be a no-op there, not a crash.
 */
export function installChatTelemetryDevBridge(host?: ChatTelemetryBridgeHost | null): boolean {
  if (process.env.NODE_ENV === "production") return false
  // `undefined` means "use the default host"; an explicit `null` means there
  // is no host, which is what a server render hands in.
  const target =
    host === undefined ? (typeof window === "undefined" ? null : (window as ChatTelemetryBridgeHost)) : host
  if (!target) return false
  target.__CREWSHIP_CHAT_TELEMETRY__ = {
    peek: () => buffer.map(copyEvent),
    drain: () => drainChatEvents().map(copyEvent),
    // Not `resetChatTelemetry`: that also drops the sink, and a console
    // command meaning "clear the screen" must not silently unregister the
    // host's destination.
    reset: () => {
      buffer = []
      seenOnce = new Set<string>()
    },
    summary: () => {
      const out = {} as Record<ChatEventName, number>
      for (const name of CHAT_EVENT_NAMES) out[name] = 0
      for (const e of buffer) if (e.name in out) out[e.name] += 1
      return out
    },
    json: () => JSON.stringify(buffer.map(copyEvent), null, 2),
    names: CHAT_EVENT_NAMES,
    schema: CHAT_EVENT_SCHEMA,
    help: HELP,
  }
  return true
}

// Attaching at import is what makes the reader discoverable without anybody
// remembering to mount anything: the module is imported by every instrumented
// surface, so opening the console on a chat page is enough.
installChatTelemetryDevBridge()

function sanitizeValue(field: FieldSpec, raw: unknown): string | number | boolean | undefined {
  switch (field.kind) {
    case "id":
      return typeof raw === "string" && ID_RE.test(raw) ? raw : undefined
    case "num":
      return typeof raw === "number" && Number.isFinite(raw) ? raw : undefined
    case "bool":
      return typeof raw === "boolean" ? raw : undefined
    case "enum":
      // Exact membership. No trimming, no case folding — padding a valid
      // value with a path is a documented attempt, and it is rejected.
      return typeof raw === "string" && field.values.includes(raw) ? raw : undefined
  }
}

function sanitize(spec: EventSpec, raw: unknown): Record<string, string | number | boolean> {
  const out: Record<string, string | number | boolean> = {}
  if (raw === null || typeof raw !== "object") return out
  const input = raw as Record<string, unknown>
  // Iterate the SCHEMA, never the input: an undeclared key has no way in.
  for (const [key, field] of Object.entries(spec)) {
    if (!(key in input)) continue
    const value = sanitizeValue(field, input[key])
    if (value !== undefined) out[key] = value
  }
  return out
}

/**
 * Record one interaction.
 *
 * Never throws and never returns a value the caller has to handle: a
 * telemetry failure must not be able to block a send, a click, or an
 * upload. A throwing sink is caught here and reported to the dev console
 * only.
 */
export function emitChatEvent<E extends ChatEventName>(name: E, payload: ChatEventPayload<E>): void {
  try {
    const spec = CHAT_EVENT_SCHEMA[name] as EventSpec | undefined
    if (!spec) {
      devWarn(`[chat-telemetry] unknown event ${String(name)} — not emitted`)
      return
    }
    const event: ChatEvent = { name, payload: sanitize(spec, payload), ts: Date.now() }
    buffer.push(event)
    if (buffer.length > BUFFER_LIMIT) buffer = buffer.slice(buffer.length - BUFFER_LIMIT)
    sink?.(event)
  } catch (err) {
    devWarn("[chat-telemetry] emit failed", err)
  }
}

/**
 * Emit at most once per `dedupeKey` for the lifetime of the page.
 *
 * Impressions (`ask_chip_shown`) fire on render, and React renders a lot.
 * Without this the numerator of "chips shown vs clicked" is a render
 * count, which is not a number anybody can reason about.
 *
 * The key is caller-chosen and never leaves this module — it is not part
 * of any payload — but keep it id-shaped anyway out of habit.
 */
export function emitChatEventOnce<E extends ChatEventName>(
  dedupeKey: string,
  name: E,
  payload: ChatEventPayload<E>,
): void {
  try {
    const key = `${String(name)}::${dedupeKey}`
    if (seenOnce.has(key)) return
    // Same reason as the buffer cap: a tab left open for a day, walking many
    // agents and many sessions, must not accumulate keys forever. Past the
    // cap the oldest keys are forgotten, so a chip may be counted twice after
    // thousands of distinct impressions — a far better failure than a leak.
    if (seenOnce.size >= DEDUPE_LIMIT) seenOnce = new Set<string>()
    seenOnce.add(key)
  } catch (err) {
    devWarn("[chat-telemetry] dedupe failed", err)
    return
  }
  emitChatEvent(name, payload)
}

/* ------------------------------------------------------------------ *
 *  Small helpers used at the call sites, kept here so two components
 *  cannot classify the same thing two ways.
 * ------------------------------------------------------------------ */

/**
 * Coarse class for a MIME type. Deliberately lossy: `image/heic` and
 * `image/png` are both `image`, and nothing derived from the filename is
 * ever consulted.
 */
export function mimeKind(mime: string | undefined | null): (typeof MIME_KIND)[number] {
  const m = (mime ?? "").toLowerCase()
  if (m.startsWith("image/")) return "image"
  if (m.startsWith("audio/")) return "audio"
  if (m === "application/pdf") return "pdf"
  if (m.startsWith("text/") || m === "application/json" || m === "application/xml") return "text"
  if (m === "application/zip" || m === "application/gzip" || m === "application/x-tar") return "archive"
  return "other"
}

/**
 * A stable, content-free id for something whose only natural identifier is its
 * own text — a suggested question, which is a string an author wrote and which
 * the product never gave a row of its own.
 *
 * FNV-1a, hex, prefixed. This is a fingerprint, not a hash of a secret: it
 * makes two renderings of the same question the same chip, and it keeps the
 * question itself out of the event. Anyone holding the pack can re-hash it and
 * match — that is the intended use, and these events do not leave the install.
 */
export function hashedId(prefix: string, text: string): string {
  let h = 0x811c9dc5
  for (let i = 0; i < text.length; i++) {
    h ^= text.charCodeAt(i)
    h = Math.imul(h, 0x01000193)
  }
  return `${prefix}_${(h >>> 0).toString(16).padStart(8, "0")}`
}

/** Map an upload failure to one of the declared reasons. */
export function uploadFailureReason(status?: number): (typeof CHAT_EVENT_SCHEMA)["attachment_upload_failed"]["reason"]["values"][number] {
  if (status === undefined || status === 0) return "network"
  if (status === 413) return "too_large"
  if (status === 415) return "unsupported_type"
  if (status === 429) return "rate_limited"
  if (status >= 400) return "http_error"
  return "unknown"
}
