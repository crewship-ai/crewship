/**
 * The submission envelope — what a form submit leaves behind besides the
 * message.
 *
 * ─── What this is, and what it deliberately is not (audit P0.6) ───────────
 *
 * Submitting a form sends an ORDINARY USER MESSAGE. That is what makes forms
 * work against every CLI adapter without teaching any of them a new shape, and
 * it is not reopened here. The envelope is ADDITIONAL METADATA carried with
 * that message, never a replacement payload: strip it and the conversation
 * still reads exactly as it did.
 *
 * What it buys is the thing the audit found missing. Before it, the answers
 * were local component state and the "via Add a receipt" badge was an
 * in-memory map keyed by the rendered message CONTENT. Content is not an
 * identity — two identical submissions collide, and the second silently
 * relabels the first — and a reload lost the form, the field/value
 * relationship, and which upload answered which question, even when the
 * message itself survived.
 *
 * ─── Where it rides ────────────────────────────────────────────────────────
 *
 * `conversation.Message` already carries `Metadata any`, written into a
 * schemaless JSONL line and unmarshalled straight back on read
 * (internal/conversation/store.go), and this file's Go twin —
 * internal/askforms/envelope.go — puts the envelope in that map under the same
 * key. No migration, no new column, and a message written before any of this
 * reads back exactly as before.
 *
 * ─── The rule about secrets ────────────────────────────────────────────────
 *
 * A field whose type fails closed (lib/ask-validate.ts) has NO entry here. Not
 * a redacted one, not an empty one — none. The envelope outlives the tab; "we
 * stored it but marked it" is the wrong shape of answer for something durable.
 */

import type { AskForm, AskValues } from "@/lib/ask-template"
import { currencyPlaceholder } from "@/lib/ask-template"
import { isAskAttachmentType, isSafeAskFieldType } from "@/lib/ask-validate"

/** The key the envelope occupies in a message's metadata map. Spelled once
 *  here and once in internal/askforms/envelope.go; a second spelling is a
 *  badge that silently stops rendering. */
export const ASK_SUBMISSION_METADATA_KEY = "ask_submission"

/** One answered form. Field names are snake_case because this crosses the
 *  wire into Go and back out of a JSONL line — the shape is the contract, not
 *  the local naming convention. */
export interface AskSubmissionEnvelope {
  /** The identity content could not be: minted once per press of Send, so two
   *  identical answers to the same form are two records rather than one
   *  overwritten one. */
  submission_id: string
  form_id: string
  /** The chip's text at the moment of submitting. Carried because a transcript
   *  is read long after the definition may have been renamed or deleted, and
   *  "via receipt" is a slug rather than an answer to what the user clicked. */
  form_label: string
  /** The author's declared revision (`version` on the definition), 1 when
   *  unset — every form stored before that field existed is its own first
   *  version. */
  form_version: number
  /** The typed answers, keyed by field name. Upload fields are NOT here. */
  values: Record<string, string | string[] | number | boolean>
  /** Which uploads answered which field.
   *
   *  The values are the durable handles the platform has TODAY: the
   *  agent-visible relative paths the upload endpoint returns
   *  (`attachments/<chatId>/<file>`). When canonical attachment identity lands
   *  (audit P0.2/P0.3 — `att_…` ids with a content hash) the ids change and
   *  this shape does not, which is why the field is named for identity rather
   *  than for paths. */
  field_attachment_ids?: Record<string, string[]>
  /** The message this envelope belongs to, as the form rendered it. The
   *  tie-break for a reader holding both. */
  rendered_text: string
}

/** A fresh submission id. `crypto.randomUUID` where it exists (every browser
 *  this app supports, and jsdom/happy-dom in tests); the fallback is only for
 *  an insecure context, where the id still only has to be unique within one
 *  conversation. */
export function newAskSubmissionId(): string {
  const uuid =
    typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
      ? crypto.randomUUID()
      : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`
  return `sub_${uuid}`
}

export interface BuildAskEnvelopeArgs {
  form: AskForm
  submissionId: string
  /** The answers as the RENDERER received them — the same map, so the
   *  envelope cannot describe a different submission from the one that was
   *  sent. */
  values: AskValues
  /** Per-field upload handles, keyed by field name. */
  attachmentsByField: Record<string, string[]>
  renderedText: string
}

/**
 * Build the record for one submission.
 *
 * Upload fields are moved out of `values` and into `field_attachment_ids`, so
 * a file appears once and under the question it answers — the same rule that
 * stopped one upload from satisfying every upload field. Anything the
 * field-type rule fails closed on is dropped rather than carried.
 */
export function buildAskEnvelope({
  form,
  submissionId,
  values,
  attachmentsByField,
  renderedText,
}: BuildAskEnvelopeArgs): AskSubmissionEnvelope {
  const envelope: AskSubmissionEnvelope = {
    submission_id: submissionId,
    form_id: form.id,
    form_label: form.label,
    form_version: askFormVersion(form),
    values: {},
    rendered_text: renderedText,
  }

  for (const field of form.fields ?? []) {
    if (isAskAttachmentType(field.type) || !isSafeAskFieldType(field.type)) continue
    const v = values[field.name]
    if (v === undefined || v === null) continue
    envelope.values[field.name] = v
    if (field.type === "money") {
      // A money field's currency is a second answer under a derived name, not
      // a field of its own.
      const cur = values[currencyPlaceholder(field.name)]
      if (cur !== undefined && cur !== null) {
        envelope.values[currencyPlaceholder(field.name)] = cur
      }
    }
  }

  for (const field of form.fields ?? []) {
    if (!isAskAttachmentType(field.type)) continue
    const ids = attachmentsByField[field.name] ?? []
    if (ids.length === 0) continue
    envelope.field_attachment_ids ??= {}
    envelope.field_attachment_ids[field.name] = [...ids]
  }

  return envelope
}

/** The declared revision, or 1. Read through a cast because `version` is an
 *  additive field on the stored definition: a console older than the server
 *  simply sees a form at version 1, which is the honest reading. */
export function askFormVersion(form: AskForm): number {
  const declared = (form as { version?: unknown }).version
  return typeof declared === "number" && Number.isInteger(declared) && declared >= 1 ? declared : 1
}

/**
 * Read an envelope back out of a message's metadata.
 *
 * Tolerant by construction: metadata arrives from a JSONL line written by an
 * older build or from a WS frame, and every shape that is not an envelope
 * simply means "this message did not come out of a form". There is no caller
 * for whom a malformed badge is worth an error path.
 */
export function askEnvelopeFromMetadata(metadata: unknown): AskSubmissionEnvelope | null {
  if (!metadata || typeof metadata !== "object") return null
  const raw = (metadata as Record<string, unknown>)[ASK_SUBMISSION_METADATA_KEY]
  if (!raw || typeof raw !== "object") return null
  const candidate = raw as Partial<AskSubmissionEnvelope>
  if (typeof candidate.submission_id !== "string" || candidate.submission_id === "") return null
  if (typeof candidate.form_id !== "string" || candidate.form_id === "") return null
  return {
    submission_id: candidate.submission_id,
    form_id: candidate.form_id,
    form_label: typeof candidate.form_label === "string" ? candidate.form_label : "",
    form_version:
      typeof candidate.form_version === "number" && candidate.form_version >= 1
        ? candidate.form_version
        : 1,
    values:
      candidate.values && typeof candidate.values === "object"
        ? (candidate.values as AskSubmissionEnvelope["values"])
        : {},
    field_attachment_ids:
      candidate.field_attachment_ids && typeof candidate.field_attachment_ids === "object"
        ? (candidate.field_attachment_ids as Record<string, string[]>)
        : undefined,
    rendered_text: typeof candidate.rendered_text === "string" ? candidate.rendered_text : "",
  }
}

/** What the transcript calls the form a turn came out of. */
export function askEnvelopeLabel(envelope: AskSubmissionEnvelope): string {
  return envelope.form_label.trim() !== "" ? envelope.form_label : envelope.form_id
}
