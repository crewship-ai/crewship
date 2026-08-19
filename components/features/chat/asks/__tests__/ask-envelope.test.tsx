import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, act } from "@testing-library/react"

// =============================================================================
// Audit P0.6 — the submission is durable structured data.
//
// Before: the answers were local component state, and the only record a
// submission left behind was a label in an in-memory map keyed by the RENDERED
// MESSAGE CONTENT. Two identical submissions collided, a reload lost
// everything, and nothing anywhere said which upload answered which question.
//
// After: the message is still an ordinary message — that decision is what
// makes forms work against every CLI adapter and it is not reopened — and the
// structure rides beside it as metadata. What is asserted here is that the
// envelope exists, describes the submission, survives a reload, and never
// carries a secret.
// =============================================================================

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", loading: false }),
}))

const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: { error: (m: string) => toastError(m), success: vi.fn(), info: vi.fn() },
}))

import { renderAskTemplate } from "@/lib/ask-template"
import { useComposerStore } from "@/stores/composer-store"

import { AskFormSheet } from "../ask-form-sheet"
import {
  askEnvelopeFromMetadata,
  ASK_SUBMISSION_METADATA_KEY,
  type AskSubmissionEnvelope,
} from "../ask-envelope"
import {
  forgetAskSubmissionsInMemory,
  listAskSubmissions,
  lookupAskSubmission,
  resetAskProvenance,
} from "../ask-provenance"
import type { AskForm } from "../types"

const onSubmit = vi.fn(async () => true)
const onClose = vi.fn()

const receipt: AskForm = {
  id: "receipt",
  label: "Add a receipt",
  // Declared by the author; carried into every envelope so a transcript can
  // still be read against the questionnaire that produced it.
  version: 3,
  template: "Supplier: {{supplier}}\nAmount: {{amount}} {{amount_currency}}\nDoc: {{document}}",
  fields: [
    { name: "supplier", label: "Supplier", type: "text" },
    { name: "amount", label: "Amount", type: "money", currency: ["CZK", "EUR"] },
    { name: "paid", label: "Already paid", type: "checkbox" },
    { name: "document", label: "Document", type: "file" },
  ],
} as AskForm

function renderSheet(form: AskForm = receipt) {
  return render(
    <AskFormSheet
      form={form}
      agentId="agent-1"
      sessionId="sess-1"
      onSubmit={onSubmit}
      renderTemplate={renderAskTemplate}
      onClose={onClose}
    />,
  )
}

function attachTo(field: string, names: string[], formId = "receipt") {
  act(() => {
    useComposerStore.setState({
      attachments: {
        "sess-1": names.map((name, i) => ({
          id: `att-${i}`,
          name,
          size: 100,
          type: "image/heic",
          status: "ready" as const,
          path: `attachments/sess-1/${name}`,
          owner: { formId, field },
        })),
      },
    })
  })
}

function envelopeFromLastSubmit(): AskSubmissionEnvelope {
  const call = onSubmit.mock.calls[onSubmit.mock.calls.length - 1] as unknown as [
    AskForm,
    string,
    AskSubmissionEnvelope,
  ]
  return call[2]
}

async function fillAndSend(supplier: string) {
  fireEvent.change(screen.getByLabelText("Supplier"), { target: { value: supplier } })
  fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "1249" } })
  fireEvent.click(screen.getByLabelText("Already paid"))
  fireEvent.click(screen.getByTestId("ask-submit"))
  await waitFor(() => expect(onSubmit).toHaveBeenCalled())
}

describe("ask submission envelope", () => {
  beforeEach(() => {
    useComposerStore.setState({ attachments: {}, drafts: {} })
    resetAskProvenance()
    onSubmit.mockClear()
    onClose.mockClear()
    toastError.mockClear()
  })

  it("hands the send path the structure the message cannot carry", async () => {
    renderSheet()
    attachTo("document", ["IMG_4821.heic"])
    await fillAndSend("Vodafone")

    const envelope = envelopeFromLastSubmit()
    expect(envelope.form_id).toBe("receipt")
    expect(envelope.form_label).toBe("Add a receipt")
    expect(envelope.form_version).toBe(3)
    expect(envelope.submission_id).toMatch(/^sub_/)

    // The answers, in the shape each field type is defined in — not the
    // flattened string the message happens to render them as.
    expect(envelope.values.supplier).toBe("Vodafone")
    expect(envelope.values.amount).toBe("1249")
    expect(envelope.values.amount_currency).toBe("CZK")
    expect(envelope.values.paid).toBe(true)

    // Which upload answered which question. `document` appears HERE and not
    // in values: one file, one place, under the field that asked for it.
    expect(envelope.field_attachment_ids).toEqual({
      document: ["attachments/sess-1/IMG_4821.heic"],
    })
    expect(envelope.values.document).toBeUndefined()

    // And the text it belongs to, so a reader holding both can tell.
    expect(envelope.rendered_text).toContain("Supplier: Vodafone")
    // The message itself is unchanged: still ordinary, still readable.
    expect(onSubmit.mock.calls[0][1]).toBe(envelope.rendered_text)
  })

  it("gives two identical submissions two identities", async () => {
    const first = renderSheet()
    await fillAndSend("Vodafone")
    first.unmount()

    renderSheet()
    await fillAndSend("Vodafone")

    const [a, b] = onSubmit.mock.calls.map(
      (c) => (c as unknown as [AskForm, string, AskSubmissionEnvelope])[2],
    )
    // The two messages are character-for-character identical — which is
    // exactly the case a content key could not tell apart.
    expect(a.rendered_text).toBe(b.rendered_text)
    expect(a.submission_id).not.toBe(b.submission_id)

    // And both are recorded, rather than the second overwriting the first.
    const recorded = listAskSubmissions("sess-1")
    expect(recorded.map((e) => e.submission_id)).toEqual([a.submission_id, b.submission_id])
  })

  // A reload drops every module's memory and keeps nothing but what was
  // written down. This is the state the old map could not survive.
  it("survives a reload with the form, the version, the values and the files", async () => {
    renderSheet()
    attachTo("document", ["IMG_4821.heic"])
    await fillAndSend("Vodafone")
    const sent = envelopeFromLastSubmit()

    forgetAskSubmissionsInMemory()

    const afterReload = lookupAskSubmission("sess-1", sent.submission_id)
    expect(afterReload).not.toBeNull()
    expect(afterReload?.form_id).toBe("receipt")
    expect(afterReload?.form_version).toBe(3)
    expect(afterReload?.values.supplier).toBe("Vodafone")
    expect(afterReload?.values.paid).toBe(true)
    expect(afterReload?.field_attachment_ids?.document).toEqual([
      "attachments/sess-1/IMG_4821.heic",
    ])
    // Another conversation's ledger is another conversation's.
    expect(lookupAskSubmission("sess-2", sent.submission_id)).toBeNull()
  })

  it("is read back from a message's metadata the same way it is written", async () => {
    renderSheet()
    await fillAndSend("Vodafone")
    const sent = envelopeFromLastSubmit()

    // The shape a persisted message carries: an ordinary metadata map with
    // the envelope under one agreed key (internal/askforms/envelope.go writes
    // the same one into conversation.Message.Metadata).
    const roundTripped = askEnvelopeFromMetadata(
      JSON.parse(JSON.stringify({ [ASK_SUBMISSION_METADATA_KEY]: sent })),
    )
    expect(roundTripped).toEqual(sent)
  })

  it("reads nothing out of metadata that is not an envelope", () => {
    for (const metadata of [
      null,
      undefined,
      { trace_id: "abc" },
      { [ASK_SUBMISSION_METADATA_KEY]: "not an object" },
      { [ASK_SUBMISSION_METADATA_KEY]: { form_id: "receipt" } },
    ]) {
      expect(askEnvelopeFromMetadata(metadata)).toBeNull()
    }
  })

  it("never records a field that failed closed, in the envelope or in storage", async () => {
    renderSheet({
      id: "connect",
      label: "Connect",
      template: "Service: {{service}}",
      fields: [
        { name: "service", label: "Service", type: "text" },
        { name: "api", label: "API key", type: "api_key" },
      ],
    })
    fireEvent.change(screen.getByLabelText("Service"), { target: { value: "Vodafone" } })
    fireEvent.click(screen.getByTestId("ask-submit"))

    // The form cannot be submitted at all, so there is nothing to record —
    // and the durable store is where a leaked secret would outlive the tab.
    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(onSubmit).not.toHaveBeenCalled()
    expect(listAskSubmissions("sess-1")).toEqual([])
    expect(JSON.stringify(window.sessionStorage)).not.toContain("api")
  })
})
