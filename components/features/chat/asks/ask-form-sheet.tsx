"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { ChevronRight, ClipboardList, X } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import {
  useComposerStore,
  attachmentsForOwner,
  messageOwnAttachments,
  type ComposerAttachment,
} from "@/stores/composer-store"
import {
  composeMessageWithAttachments,
  hasPendingUploads,
  isSendableAttachment,
} from "@/lib/attachment-message"
import { emitChatEvent } from "@/lib/telemetry"
import { cn } from "@/lib/utils"

import { currencyPlaceholder } from "@/lib/ask-template"
import { classifyAskFieldType, validateAskAnswers } from "@/lib/ask-validate"

import {
  buildAskEnvelope,
  newAskSubmissionId,
  type AskSubmissionEnvelope,
} from "./ask-envelope"
import { recordAskSubmission } from "./ask-provenance"
import { AskFieldAttachments, AskMessageAttachments } from "./field-attachments"
import { FormField, fieldLabelText, splitMoney } from "./form-field"
import {
  fieldType,
  isAttachmentField,
  type AskForm,
  type AskFormField,
  type AskValues,
  type RenderAskTemplate,
} from "./types"

/**
 * The questionnaire sheet (PRD §5.2).
 *
 * It grows **above the composer, inside the same column**, capped at 560px,
 * with the conversation still visible above it. It is deliberately NOT the
 * centred `Dialog` that `composer/slash-action-modal.tsx` uses, for two
 * concrete reasons:
 *
 *   · **Drag and drop.** A centred modal over a chat is a drop target sitting
 *     on top of the thing the user was dragging from.
 *   · **Phones.** A centred modal hides the keyboard-adjacent composer, which
 *     is the one piece of UI a phone user needs to stay oriented. On mobile
 *     this becomes a bottom sheet at 90vh instead — same component, different
 *     host, per `compact`.
 *
 * Only the *host* differs from the slash modal. The field renderer is the
 * shared one (./form-field.tsx), so there is a single mapping from schema to
 * inputs in the product and a new field type appears in both places at once.
 *
 * Three things this component refuses to do:
 *
 *   1. **Render the template itself.** That is lib/ask-template.ts, injected
 *      as `renderTemplate`, and it is the same renderer the server and the
 *      CLI preview with — both pinned to testdata/ask-templates.json. Two
 *      renderers that can silently disagree about what the user is sending is
 *      the defect that fixture exists to prevent.
 *   2. **Upload anything itself.** `file` / `photo` fields route through the
 *      composer's own `useAttachmentUpload` — the same endpoint, the same
 *      25 MB guard, the same failure toast, the same Retry (./field-attachments
 *      .tsx). What the sheet does own is WHICH upload answers which question:
 *      each field reads only the attachments stamped with its own name, so a
 *      contract cannot satisfy a request for an identity photo. While this
 *      sheet shows an upload control it is also the only place chips are drawn
 *      — the composer hides its own list, because two views of one list read
 *      as two attachments.
 *   3. **Send anything itself.** It hands the rendered text to `onSubmit`,
 *      which is the composer's own `useMessageSubmit` path — so the size
 *      guard, the still-uploading refusal and the draft-survival rules all
 *      still apply to a form exactly as they do to something typed.
 *
 * What it DOES own, and did not before (audit P0.6 and P0.7):
 *
 *   · **The rules at submit.** A definition may state `min`, `max`, `pattern`
 *     and `multiple`; until now only `required` was checked here, so the rest
 *     were promises the form made and nothing kept. They are applied by
 *     lib/ask-validate.ts, the same module internal/askforms/answers.go is
 *     pinned to, and every refusal names the field.
 *   · **Failing closed on a field it cannot render honestly.** An unrecognised
 *     type still becomes a text input — that is what lets the server ship a
 *     type without a frontend release — but one that NAMES A SECRET renders no
 *     input at all and blocks the submit. Such a definition cannot be saved
 *     any more either; this is for the row that predates the rule.
 *   · **The submission envelope.** The message stays an ordinary message; the
 *     structure (form id, version, answers, which upload answered which field)
 *     rides beside it (./ask-envelope.ts) and is recorded durably before the
 *     send is attempted.
 */

export interface AskFormSheetProps {
  /** The open form. `null` renders nothing at all. */
  form: AskForm | null
  agentId: string
  sessionId: string
  /** Sends the rendered text as an ordinary message. Resolves `true` when the
   *  message actually went out — a size-guard refusal resolves `false` and the
   *  sheet stays open with everything the user typed still in it.
   *
   *  The third argument is the submission envelope (./ask-envelope.ts): the
   *  structured record of what was answered, to be carried as metadata ON the
   *  message rather than inside it. It is optional to RECEIVE — every existing
   *  caller keeps compiling — and always passed, so the send path can attach
   *  it the moment it is able to. Until then the envelope is still recorded
   *  locally and durably, which is what a reload used to lose. */
  onSubmit: (form: AskForm, text: string, envelope: AskSubmissionEnvelope) => Promise<boolean>
  /** The `{{field}}` renderer — `lib/ask-template.ts`, injected from the top
   *  of the feature (chat-panel.tsx) rather than imported here. There is still
   *  exactly one renderer in the product; taking it as a parameter is what
   *  keeps the fact visible where the feature is assembled, and lets the sheet
   *  be tested for wiring rather than for the renderer's own rules. */
  renderTemplate: RenderAskTemplate
  onClose: () => void
  /** Bottom sheet at 90vh instead of an in-column card. */
  compact?: boolean
  /** Streaming / disconnected — the composer's own submit is unavailable. */
  disabled?: boolean
}

const NO_ATTACHMENTS: ComposerAttachment[] = []

export function AskFormSheet(props: AskFormSheetProps) {
  if (!props.form) return null
  // Keyed on the form id so switching forms rebuilds the values map from that
  // form's defaults rather than carrying the previous form's answers across.
  return <Sheet key={props.form.id} {...props} form={props.form} />
}

function Sheet({
  form,
  agentId,
  sessionId,
  onSubmit,
  renderTemplate,
  onClose,
  compact,
  disabled,
}: AskFormSheetProps & { form: AskForm }) {
  // One string per field name, which is the shape FormField takes and the
  // shape the slash modal already used. The richer `AskValues` the renderer
  // wants — arrays, booleans, a money field's two entries — is derived below,
  // not stored, so there is one source of truth for what the user typed.
  const [values, setValues] = useState<Record<string, string>>(() => {
    const seed: Record<string, string> = {}
    for (const f of form.fields) seed[f.name] = defaultString(f)
    return seed
  })
  const [previewOpen, setPreviewOpen] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)

  const attachments = useComposerStore((s) => s.attachments[sessionId]) ?? NO_ATTACHMENTS
  const clearFormAttachments = useComposerStore((s) => s.clearFormAttachments)

  // Every upload field's own list, keyed by field name. This is the fix: the
  // store holds one list per session, but each attachment now says which field
  // it answers, so "the contract" and "the identity photo" are two answers and
  // not two views of one.
  const byField = useMemo(() => {
    const out: Record<string, ComposerAttachment[]> = {}
    for (const f of form.fields) {
      if (isAttachmentField(f)) {
        out[f.name] = attachmentsForOwner(attachments, { formId: form.id, field: f.name })
      }
    }
    return out
  }, [form.id, form.fields, attachments])

  // A `file` / `photo` field's value is not typed, it is uploaded. What the
  // template sees is the agent-visible relative path the upload already
  // returned; the renderer passes a value that is already prefixed straight
  // through, so the path in the template and the path in the attachment block
  // are the same string (PRD §7.4). Only a FINISHED upload has one — a refused
  // one leaves its field empty, which is what makes it fail a `required` check
  // instead of quietly sending a path to a file that is not on the agent.
  const pathsByField = useMemo(() => {
    const out: Record<string, string[]> = {}
    for (const [name, list] of Object.entries(byField)) {
      out[name] = list.filter(isSendableAttachment).map((a) => a.path!)
    }
    return out
  }, [byField])

  // Attachments that answer no question: the composer's paperclip, or a file
  // dropped on the conversation while this sheet was open. They stay the
  // MESSAGE's — named by the appended block, never by a field — and the sheet
  // shows them (below) because the composer hides its own list while an upload
  // control is on screen.
  const messageAttachments = useMemo(() => messageOwnAttachments(attachments), [attachments])
  const hasUploadField = useMemo(() => form.fields.some(isAttachmentField), [form.fields])

  const renderValues = useMemo<AskValues>(
    () => toAskValues(form.fields, values, pathsByField),
    [form.fields, values, pathsByField],
  )

  const rendered = useMemo(() => {
    try {
      return renderTemplate(form, renderValues, sessionId)
    } catch {
      // The renderer has no error path by design — every way a definition
      // could be broken is refused when the form is SAVED. This catch is for
      // a row that predates that validator: a preview that throws would take
      // the whole sheet down, and the author must be able to see and fix it.
      return form.template
    }
  }, [renderTemplate, form, renderValues, sessionId])

  // What will ACTUALLY go: the rendered template plus the attachment block the
  // composer appends. The preview is only worth having if it is the whole
  // message, not the half of it this component happens to own.
  const preview = useMemo(
    () => composeMessageWithAttachments(rendered, attachments),
    [rendered, attachments],
  )

  /* ---------------------------------------------------------------- *
   *  Measurement (lib/telemetry.ts)
   *
   *  A questionnaire is either finished or abandoned, and when it is
   *  abandoned the only fact worth having is WHERE. So the sheet keeps
   *  three things: when it opened, which field was touched last, and
   *  whether it has already reported an outcome — exactly one terminal
   *  event per sheet, whichever of the five exits was taken.
   *
   *  Field IDENTIFIERS only. `values` is what the user typed and it is
   *  never read by any of this except to be counted.
   * ---------------------------------------------------------------- */
  const openedAtRef = useRef(Date.now())
  const lastFieldRef = useRef<string | null>(null)
  const terminalRef = useRef(false)
  const valuesRef = useRef(values)
  useEffect(() => {
    valuesRef.current = values
  }, [values])

  const filledCount = useCallback(
    () => Object.values(valuesRef.current).filter((v) => String(v).trim() !== "").length,
    [],
  )

  useEffect(() => {
    emitChatEvent("ask_form_opened", {
      session_id: sessionId,
      agent_id: agentId,
      template_id: form.id,
      field_count: form.fields.length,
    })
    // Once per sheet. The component is keyed on the form id upstream, so a
    // different form is a different mount and gets its own open.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const abandon = useCallback(
    (reason: "dismissed" | "cancelled" | "navigated") => {
      if (terminalRef.current) return
      terminalRef.current = true
      emitChatEvent("ask_form_abandoned", {
        session_id: sessionId,
        template_id: form.id,
        field_count: form.fields.length,
        filled_count: filledCount(),
        last_field_id: lastFieldRef.current ?? undefined,
        reason,
        duration_ms: Date.now() - openedAtRef.current,
      })
    },
    [sessionId, form.id, form.fields.length, filledCount],
  )

  /** Close, and say which of the four exits was taken. */
  const closeWith = useCallback(
    (reason: "dismissed" | "cancelled") => {
      abandon(reason)
      onClose()
    },
    [abandon, onClose],
  )

  // A sheet that simply went away — a session swap, a route change — is an
  // abandonment too, and it is the one nobody would otherwise count. Held in a
  // ref so the cleanup runs on unmount only, and not every time a dependency
  // of `abandon` gets a new identity.
  const abandonRef = useRef(abandon)
  useEffect(() => {
    abandonRef.current = abandon
  }, [abandon])
  useEffect(() => () => abandonRef.current("navigated"), [])

  const setField = useCallback(
    (name: string) => (e: { target: { value: string } }) => {
      lastFieldRef.current = name
      setValues((prev) => ({ ...prev, [name]: e.target.value }))
    },
    [],
  )

  const handleSubmit = useCallback(async () => {
    if (submitting || disabled) return

    // An upload still in flight is not a violated rule, it is a "not yet", so
    // it is answered before the rules and per field: an upload that has not
    // landed has no path, and a refused one never will — neither can answer
    // the question, and neither may be answered by the file the field NEXT to
    // this one got.
    for (const f of form.fields) {
      if (isAttachmentField(f) && hasPendingUploads(byField[f.name] ?? [])) {
        toast.error(`${fieldLabelText(f)} is still uploading — wait for it to finish.`)
        return
      }
    }

    // Every rule the definition states, applied where the answer is given.
    // One module, shared with internal/askforms via testdata/ask-field-types
    // .json, so the CLI preview and the sheet refuse the same answers with the
    // same words — and every refusal names the field, because "something is
    // missing" over six inputs is not a message.
    const problems = validateAskAnswers(form, renderValues)
    if (problems.length > 0) {
      toast.error(problems[0].message)
      return
    }

    // The form-level policy is about the message carrying a document at all,
    // so anything that can actually go counts: a file answering one of this
    // form's own fields, or one attached to the message beside it.
    if (
      form.attachment === "required" &&
      !attachments.some(
        (a) => isSendableAttachment(a) && (!a.owner || a.owner.formId === form.id),
      )
    ) {
      toast.error(
        `“${form.label}” needs a document — attach a file or take a photo before sending.`,
      )
      return
    }

    if (rendered.trim() === "") {
      toast.error("This form renders an empty message — fill something in first.")
      return
    }

    // The record of what was answered, minted before the send is attempted and
    // stored durably (./ask-provenance.ts).
    //
    // Before the send on purpose, and it is safe to be: the envelope describes
    // a SUBMISSION, not a delivery. It claims the user answered this form this
    // way, which is true the moment they press Send, and it is bound to a
    // message by its own id and text rather than by asserting that one exists.
    // The old map made the other claim — "this text on screen came from that
    // form" — which is why recording it before the send could label a message
    // that never went.
    const envelope = buildAskEnvelope({
      form,
      submissionId: newAskSubmissionId(),
      values: renderValues,
      attachmentsByField: pathsByField,
      renderedText: rendered,
    })
    recordAskSubmission(sessionId, envelope)

    setSubmitting(true)
    try {
      const sent = await onSubmit(form, rendered, envelope)
      if (sent) {
        // Only a message that actually went is a completed form. A refused
        // send leaves the sheet open with everything still in it, and counting
        // it here is how "≥ 70 % completion once opened" would quietly inflate.
        terminalRef.current = true
        emitChatEvent("ask_form_submitted", {
          session_id: sessionId,
          template_id: form.id,
          field_count: form.fields.length,
          filled_count: filledCount(),
          attachment_count: Object.values(pathsByField).reduce((n, l) => n + l.length, 0),
          duration_ms: Date.now() - openedAtRef.current,
        })
        onClose()
      }
    } finally {
      setSubmitting(false)
    }
  }, [
    filledCount,
    submitting,
    disabled,
    form,
    byField,
    pathsByField,
    renderValues,
    attachments,
    rendered,
    sessionId,
    onSubmit,
    onClose,
  ])

  // Escape closes and sends nothing. Handled on the root rather than on
  // `document`, so a Radix Select open inside a field (which portals out of
  // this tree and handles its own Escape) closes the select and not the sheet.
  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") {
      e.stopPropagation()
      closeWith("dismissed")
    }
  }

  // A form's uploads are its answers, and they do not outlive it: closing the
  // sheet — sent, cancelled or escaped — drops them, exactly as the typed
  // answers above them are dropped. Leaving them in the store would mean an
  // invisible file (the composer draws only the message's own chips) riding
  // along in a session it was never meant for. The message's own attachments
  // are untouched: nobody asked this sheet about them.
  useEffect(
    () => () => clearFormAttachments(sessionId, form.id),
    [clearFormAttachments, sessionId, form.id],
  )

  // Focus lands on the first control, so a keyboard user who clicked a chip is
  // already in the form. Not a focus TRAP: the conversation behind the sheet
  // stays reachable on purpose — that is the whole reason this is not a modal.
  useEffect(() => {
    const first = rootRef.current?.querySelector<HTMLElement>(
      "input:not([type='hidden']), textarea, [role='combobox']",
    )
    first?.focus()
  }, [])

  const body = (
    <div
      ref={rootRef}
      data-testid="ask-sheet"
      role="dialog"
      aria-label={form.label}
      onKeyDown={handleKeyDown}
      className={cn(
        "flex flex-col overflow-hidden rounded-xl border bg-background shadow-lg",
        compact ? "h-[90vh] rounded-b-none" : "max-h-[560px]",
      )}
    >
      <header className="flex shrink-0 items-center gap-2 border-b px-4 py-2.5">
        <ClipboardList className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
        <h2 className="min-w-0 flex-1 truncate text-sm font-medium">{form.label}</h2>
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          className="h-7 w-7"
          data-testid="ask-close"
          onClick={() => closeWith("dismissed")}
        >
          <X className="h-3.5 w-3.5" />
          <span className="sr-only">Close</span>
        </Button>
      </header>

      <form
        className="min-h-0 flex-1 space-y-4 overflow-y-auto px-4 py-3"
        onSubmit={(e) => {
          e.preventDefault()
          void handleSubmit()
        }}
      >
        {form.fields.map((f) =>
          classifyAskFieldType(fieldType(f)).verdict === "unsafe" ? (
            <BlockedField key={f.name} field={f} />
          ) : (
            <FormField
              key={f.name}
              field={f}
              value={values[f.name] ?? ""}
              onChange={setField(f.name)}
              idPrefix={`ask-${form.id}-`}
              testIdPrefix="ask-field"
              attachmentSlot={
                isAttachmentField(f) ? (
                  <AskFieldAttachments
                    agentId={agentId}
                    sessionId={sessionId}
                    formId={form.id}
                    field={f}
                  />
                ) : undefined
              }
            />
          ),
        )}

        {/* A hidden submit so Enter inside a text field sends the form, the
            same reflex the composer's textarea has. */}
        <button type="submit" className="hidden" tabIndex={-1} aria-hidden="true" />
      </form>

      <div className="shrink-0 space-y-2 border-t px-4 py-2">
        {/* Only while this sheet is the one drawing chips — a form with no
            upload control never hides the composer's own list, so repeating it
            here would be the double chip this all exists to stop. */}
        {hasUploadField && (
          <AskMessageAttachments
            agentId={agentId}
            sessionId={sessionId}
            attachments={messageAttachments}
          />
        )}
        <button
          type="button"
          data-testid="ask-preview-toggle"
          aria-expanded={previewOpen}
          className="flex w-full items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
          onClick={() => setPreviewOpen((v) => !v)}
        >
          <ChevronRight
            className={cn("h-3 w-3 transition-transform", previewOpen && "rotate-90")}
            aria-hidden="true"
          />
          Preview message
        </button>
        {previewOpen && (
          // Submitting sends an ORDINARY message. The user is entitled to read
          // it first, verbatim — and an author meets a broken template here,
          // while writing it, instead of in somebody's transcript.
          <pre
            data-testid="ask-preview"
            className="mt-2 max-h-32 overflow-y-auto rounded-md bg-muted px-2 py-1.5 text-xs whitespace-pre-wrap break-words text-muted-foreground"
          >
            {preview}
          </pre>
        )}
      </div>

      <footer className="flex shrink-0 items-center justify-end gap-2 border-t px-4 py-2.5">
        <Button
          type="button"
          variant="outline"
          size="sm"
          data-testid="ask-cancel"
          onClick={() => closeWith("cancelled")}
          disabled={submitting}
        >
          Cancel
        </Button>
        <Button
          type="button"
          size="sm"
          data-testid="ask-submit"
          onClick={() => void handleSubmit()}
          disabled={submitting || disabled}
        >
          {submitting ? "Sending…" : "Send"}
        </Button>
      </footer>
    </div>
  )

  if (compact) {
    return (
      <div className="fixed inset-0 z-50 flex flex-col justify-end" data-testid="ask-sheet-mobile">
        {/* Tapping away closes, like every other bottom sheet on the phone.
            It sends nothing — the same contract as Escape and Cancel. */}
        <div
          className="absolute inset-0 bg-black/40"
          onClick={() => closeWith("dismissed")}
          aria-hidden="true"
        />
        <div className="relative">{body}</div>
      </div>
    )
  }

  return <div className="mx-auto w-full max-w-3xl px-3 pb-2 md:px-6">{body}</div>
}

/**
 * A field this sheet refuses to render.
 *
 * The unknown-type fallback is a text input, and that is kept: it is what lets
 * the server ship a field type without a coordinated frontend release. What it
 * cannot be allowed to do is render a type that NAMES A SECRET as an ordinary
 * box, because the value would go straight into a durable, searchable chat
 * message while the user believed the field had special handling.
 *
 * Such a definition can no longer be saved (internal/askforms). This is for
 * the row that predates that rule, or was written straight into the database:
 * no input, an explanation of what is wrong with the FORM rather than with the
 * user, and a submit that refuses while it is on screen.
 */
function BlockedField({ field }: { field: AskFormField }) {
  return (
    <div
      className="space-y-1 rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2"
      data-testid={`ask-field-blocked-${field.name}`}
    >
      <p className="text-sm font-medium">{fieldLabelText(field)}</p>
      <p className="text-xs text-muted-foreground">
        This field asks for a {field.type} value, which a chat message cannot carry safely — a
        form submit is an ordinary message that is stored and searchable. Ask whoever configured
        this form to remove the field; a credential belongs in the vault, referenced by name.
      </p>
    </div>
  )
}

/** A definition's `default` arrives as `unknown` (lib/ask-template.ts keeps it
 *  loose so a CLI body need not quote everything). The controls this sheet
 *  renders are all string-valued, so anything else is coerced once, here. */
function defaultString(field: AskFormField): string {
  const d = field.default
  if (typeof d === "string") return d
  if (typeof d === "number" || typeof d === "boolean") return String(d)
  return ""
}

/**
 * What the user typed → what the renderer substitutes.
 *
 * Derived rather than stored, so the sheet holds exactly one representation of
 * the answers and this function is the only place that knows how each field
 * type reaches the template:
 *
 *   · `multiselect` → an array, which the renderer joins with ", ".
 *   · `checkbox`    → a boolean; `true` reads "yes" and `false` is empty, so
 *     an unticked box drops its line instead of asserting "no".
 *   · `file`/`photo` → the paths uploaded into THAT field, as an array, which
 *     the renderer turns into a newline list. Its own, never the form's: one
 *     list for every upload field is how a contract came to answer a request
 *     for an identity photo.
 *   · `money`       → two entries: the amount under the field's own name and
 *     the currency under `<name>_currency`.
 *
 * The renderer is unchanged by any of this. It has always taken one value per
 * field and rendered `file`/`photo` as a path list; giving each field its own
 * list is a change to the VALUES, not to the substitution — which is why
 * lib/ask-template.ts and internal/askforms stay pinned to the one golden
 * fixture without moving.
 */
export function toAskValues(
  fields: AskFormField[],
  values: Record<string, string>,
  pathsByField: Record<string, string[]>,
): AskValues {
  const out: AskValues = {}
  for (const field of fields) {
    // A field that fails closed has no value AT ALL — not an empty one, not a
    // redacted one. Nothing renders it, so nothing can have been typed into
    // it; leaving the key out means the renderer substitutes nothing and the
    // line it sits on drops, exactly like an unanswered optional field.
    if (classifyAskFieldType(fieldType(field)).verdict === "unsafe") continue
    const raw = values[field.name] ?? ""
    switch (fieldType(field)) {
      case "file":
      case "photo":
        out[field.name] = pathsByField[field.name] ?? []
        break
      case "multiselect":
        out[field.name] = raw
          .split(",")
          .map((v) => v.trim())
          .filter((v) => v !== "")
        break
      case "checkbox":
        out[field.name] = raw === "true"
        break
      case "money": {
        const { amount, currency } = splitMoney(raw, field.currency ?? undefined)
        out[field.name] = amount
        // Only when there is an amount: a bare "CZK" on a line of its own is
        // not something the user asked to send.
        out[currencyPlaceholder(field.name)] = amount.trim() === "" ? "" : currency
        break
      }
      default:
        out[field.name] = raw
    }
  }
  return out
}
