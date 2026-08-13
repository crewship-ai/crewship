"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { ChevronRight, ClipboardList, X } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { useComposerStore, type ComposerAttachment } from "@/stores/composer-store"
import { composeMessageWithAttachments, sendableAttachments } from "@/lib/attachment-message"
import { cn } from "@/lib/utils"

import { AttachmentZone, AttachmentButton, CameraButton } from "../composer/attachment-zone"
import { currencyPlaceholder } from "@/lib/ask-template"

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
 *   2. **Upload anything itself.** `file` / `photo` fields mount the composer's
 *      own AttachmentZone — the same `useAttachmentUpload`, the same 25 MB
 *      guard, the same abort-on-remove, the same per-session attachment list.
 *      Because the list is shared, the CHIPS are drawn in exactly one place at
 *      a time: while this sheet has an upload field, it is the one that draws
 *      them (the composer hides its own), and within the sheet it is the first
 *      upload field. Two views of one list read as two attachments.
 *   3. **Send anything itself.** It hands the rendered text to `onSubmit`,
 *      which is the composer's own `useMessageSubmit` path — so the size
 *      guard, the still-uploading refusal and the draft-survival rules all
 *      still apply to a form exactly as they do to something typed.
 */

export interface AskFormSheetProps {
  /** The open form. `null` renders nothing at all. */
  form: AskForm | null
  agentId: string
  sessionId: string
  /** Sends the rendered text as an ordinary message. Resolves `true` when the
   *  message actually went out — a size-guard refusal resolves `false` and the
   *  sheet stays open with everything the user typed still in it. */
  onSubmit: (form: AskForm, text: string) => Promise<boolean>
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
  const ready = useMemo(() => sendableAttachments(attachments), [attachments])

  // Which upload field draws the chips. There is one attachment list per
  // session, and a form is allowed two upload fields (a receipt and a photo of
  // it); every slot mounts an AttachmentZone over that same list, so without
  // this the file would be listed once per slot. The first one shows it — all
  // of them feed it, and the renderer's value for every `file` / `photo` field
  // is the same list of paths anyway (toAskValues below).
  const chipFieldName = useMemo(
    () => form.fields.find(isAttachmentField)?.name ?? null,
    [form.fields],
  )

  // A `file` / `photo` field's value is not typed, it is uploaded. What the
  // template sees is the agent-visible relative path the upload already
  // returned; the renderer passes a value that is already prefixed straight
  // through, so the path in the template and the path in the attachment block
  // are the same string (PRD §7.4).
  const attachmentPaths = useMemo(() => ready.map((a) => a.path!), [ready])

  const renderValues = useMemo<AskValues>(
    () => toAskValues(form.fields, values, attachmentPaths),
    [form.fields, values, attachmentPaths],
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

  const setField = useCallback(
    (name: string) => (e: { target: { value: string } }) => {
      setValues((prev) => ({ ...prev, [name]: e.target.value }))
    },
    [],
  )

  const handleSubmit = useCallback(async () => {
    if (submitting || disabled) return

    // Required fields first, named. "Something is missing" is not a message.
    for (const f of form.fields) {
      const value = isAttachmentField(f)
        ? attachmentPaths.join("\n")
        : values[f.name] ?? ""
      if (f.required && value.trim() === "") {
        toast.error(
          isAttachmentField(f)
            ? `${fieldLabelText(f)} is required — attach a file before sending.`
            : `${fieldLabelText(f)} is required.`,
        )
        return
      }
    }

    if (form.attachment === "required" && ready.length === 0) {
      toast.error(
        `“${form.label}” needs a document — attach a file or take a photo before sending.`,
      )
      return
    }

    if (rendered.trim() === "") {
      toast.error("This form renders an empty message — fill something in first.")
      return
    }

    setSubmitting(true)
    try {
      const sent = await onSubmit(form, rendered)
      if (sent) onClose()
    } finally {
      setSubmitting(false)
    }
  }, [
    submitting,
    disabled,
    form,
    values,
    attachmentPaths,
    ready.length,
    rendered,
    onSubmit,
    onClose,
  ])

  // Escape closes and sends nothing. Handled on the root rather than on
  // `document`, so a Radix Select open inside a field (which portals out of
  // this tree and handles its own Escape) closes the select and not the sheet.
  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") {
      e.stopPropagation()
      onClose()
    }
  }

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
          onClick={onClose}
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
        {form.fields.map((f) => (
          <FormField
            key={f.name}
            field={f}
            value={values[f.name] ?? ""}
            onChange={setField(f.name)}
            idPrefix={`ask-${form.id}-`}
            testIdPrefix="ask-field"
            attachmentSlot={
              isAttachmentField(f) ? (
                <AttachmentSlot
                  agentId={agentId}
                  sessionId={sessionId}
                  field={f}
                  showChips={f.name === chipFieldName}
                />
              ) : undefined
            }
          />
        ))}

        {/* A hidden submit so Enter inside a text field sends the form, the
            same reflex the composer's textarea has. */}
        <button type="submit" className="hidden" tabIndex={-1} aria-hidden="true" />
      </form>

      <div className="shrink-0 border-t px-4 py-2">
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
          onClick={onClose}
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
        <div className="absolute inset-0 bg-black/40" onClick={onClose} aria-hidden="true" />
        <div className="relative">{body}</div>
      </div>
    )
  }

  return <div className="mx-auto w-full max-w-3xl px-3 pb-2 md:px-6">{body}</div>
}

/**
 * The upload control inside a `file` / `photo` field.
 *
 * Nothing here uploads. `AttachmentZone` is the composer's own drop zone and
 * chip list, `AttachmentButton` the paperclip and `CameraButton` the
 * `capture="environment"` input a phone opens the rear camera for — all three
 * already route through the one `useAttachmentUpload`. A form field is a
 * fourth entry point to that path, not a second implementation of it, so a
 * photo attached here lands in the same per-session list, with the same size
 * guard, and is named in the outgoing message by the same convention.
 *
 * A `photo` field leads with the camera; a `file` field leads with the
 * paperclip. Both offer both — a receipt is photographed on a phone and
 * dragged in from a folder on a desktop, and the field type is a hint about
 * which is likelier, not a restriction.
 */
function AttachmentSlot({
  agentId,
  sessionId,
  field,
  showChips,
}: {
  agentId: string
  sessionId: string
  field: AskFormField
  /** False for the second and later upload fields of the same form — see
   *  `chipFieldName` above. */
  showChips: boolean
}) {
  const isPhoto = field.type === "photo"
  return (
    <AttachmentZone agentId={agentId} sessionId={sessionId} showChips={showChips}>
      <div className="flex items-center gap-2 rounded-lg border border-dashed px-3 py-2.5">
        {isPhoto ? (
          <>
            <CameraButton agentId={agentId} sessionId={sessionId} />
            <AttachmentButton agentId={agentId} sessionId={sessionId} />
          </>
        ) : (
          <>
            <AttachmentButton agentId={agentId} sessionId={sessionId} />
            <CameraButton agentId={agentId} sessionId={sessionId} />
          </>
        )}
        <span className="text-xs text-muted-foreground">
          {isPhoto ? "Take a photo, or drop a file here" : "Drop a file here, or pick one"}
        </span>
      </div>
    </AttachmentZone>
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
 *   · `file`/`photo` → the uploaded paths, as an array, which the renderer
 *     turns into a newline list.
 *   · `money`       → two entries: the amount under the field's own name and
 *     the currency under `<name>_currency`.
 */
export function toAskValues(
  fields: AskFormField[],
  values: Record<string, string>,
  attachmentPaths: string[],
): AskValues {
  const out: AskValues = {}
  for (const field of fields) {
    const raw = values[field.name] ?? ""
    switch (fieldType(field)) {
      case "file":
      case "photo":
        out[field.name] = attachmentPaths
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
