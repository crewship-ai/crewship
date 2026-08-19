import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, act } from "@testing-library/react"

// =============================================================================
// Audit P0.7 — the constraints a definition may state, enforced where a user
// actually answers.
//
// `internal/askforms` has always validated the DEFINITION (types, options,
// caps, placeholders). The submit path checked `required` and nothing else, so
// `min`, `max`, `pattern` and `multiple` were promises the form made and
// nothing kept — and an unknown type degraded to a plain text input, which for
// a secret-like type meant a value the user believed was handled specially
// landing verbatim in a durable chat message.
//
// The rule itself is tested against the shared Go/TS fixture in
// lib/__tests__/ask-validate.test.ts. What is tested HERE is that the sheet
// applies it: the user meets the rule, by name, before anything is sent.
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
import { ASK_SUBMISSION_METADATA_KEY } from "../ask-envelope"
import type { AskForm } from "../types"

const onSubmit = vi.fn(async () => true)
const onClose = vi.fn()

function renderSheet(form: AskForm) {
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

function attach(field: string, names: string[], formId: string) {
  act(() => {
    useComposerStore.setState({
      attachments: {
        "sess-1": names.map((name, i) => ({
          id: `att-${i}`,
          name,
          size: 100,
          type: "image/jpeg",
          status: "ready" as const,
          path: `attachments/sess-1/${name}`,
          owner: { formId, field },
        })),
      },
    })
  })
}

const constrained: AskForm = {
  id: "constrained",
  label: "Constrained",
  template: "Amount: {{amount}}\nNote: {{note}}\nVAT: {{vat}}\nTags: {{tags}}",
  fields: [
    { name: "amount", label: "Amount", type: "money", min: 1, max: 5000 },
    { name: "note", label: "Note", type: "text", min: 3 },
    { name: "vat", label: "VAT ID", type: "text", pattern: "CZ[0-9]{8}" },
    { name: "tags", label: "Tags", type: "multiselect", options: ["a", "b", "c"], max: 1 },
  ],
}

describe("ask form constraints at submit", () => {
  beforeEach(() => {
    useComposerStore.setState({ attachments: {}, drafts: {} })
    onSubmit.mockClear()
    onClose.mockClear()
    toastError.mockClear()
  })

  it("refuses a value below min and names the field", async () => {
    renderSheet(constrained)
    fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "0.5" } })

    fireEvent.click(screen.getByTestId("ask-submit"))

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(toastError.mock.calls[0][0]).toBe("Amount must be at least 1.")
    expect(onSubmit).not.toHaveBeenCalled()
    expect(onClose).not.toHaveBeenCalled()
  })

  it("refuses a value above max and names the field", async () => {
    renderSheet(constrained)
    fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "9000" } })

    fireEvent.click(screen.getByTestId("ask-submit"))

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(toastError.mock.calls[0][0]).toBe("Amount must be at most 5000.")
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it("refuses a value that does not match the pattern, anchored at both ends", async () => {
    renderSheet(constrained)
    fireEvent.change(screen.getByLabelText("VAT ID"), { target: { value: "xxCZ25788001xx" } })

    fireEvent.click(screen.getByTestId("ask-submit"))

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(toastError.mock.calls[0][0]).toBe("VAT ID is not in the expected format.")
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it("refuses more answers than a multi-valued field allows", async () => {
    renderSheet(constrained)
    fireEvent.click(screen.getByLabelText("a"))
    fireEvent.click(screen.getByLabelText("b"))

    fireEvent.click(screen.getByTestId("ask-submit"))

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(toastError.mock.calls[0][0]).toBe("Tags takes at most 1 option.")
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it("refuses a second file on a field declared multiple:false", async () => {
    const oneFile: AskForm = {
      id: "one-file",
      label: "One file",
      template: "Doc: {{doc}}",
      fields: [{ name: "doc", label: "Document", type: "file", multiple: false }],
    }
    renderSheet(oneFile)
    attach("doc", ["a.pdf", "b.pdf"], "one-file")

    fireEvent.click(screen.getByTestId("ask-submit"))

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(toastError.mock.calls[0][0]).toBe("Document takes one file — remove the extra ones.")
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it("sends once every constraint is satisfied", async () => {
    renderSheet(constrained)
    fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "1249" } })
    fireEvent.change(screen.getByLabelText("Note"), { target: { value: "monthly" } })
    fireEvent.change(screen.getByLabelText("VAT ID"), { target: { value: "CZ25788001" } })
    fireEvent.click(screen.getByLabelText("a"))

    fireEvent.click(screen.getByTestId("ask-submit"))

    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1))
    expect(toastError).not.toHaveBeenCalled()
  })
})

// ---------------------------------------------------------------------------
// The unknown-type rule, from the side that renders it.
//
// The open list is KEPT — that is what lets the server ship a field type
// without a coordinated frontend release. What changes is that a type naming a
// secret fails closed instead of quietly becoming a text input.
// ---------------------------------------------------------------------------
describe("unknown field types", () => {
  beforeEach(() => {
    useComposerStore.setState({ attachments: {}, drafts: {} })
    onSubmit.mockClear()
    onClose.mockClear()
    toastError.mockClear()
  })

  const withSecret: AskForm = {
    id: "connect",
    label: "Connect a service",
    template: "Service: {{service}}\nKey: {{api}}",
    fields: [
      { name: "service", label: "Service", type: "text" },
      // Refused on save today (internal/askforms). This is the row that
      // predates the rule, or was written straight into the database.
      { name: "api", label: "API key", type: "api_key" },
    ],
  }

  it("still renders a text input for a type it has never heard of", () => {
    renderSheet({
      id: "open",
      label: "Open",
      template: "{{future}}",
      fields: [{ name: "future", label: "Future thing", type: "quantum-flux" }],
    })
    const input = screen.getByLabelText("Future thing")
    expect(input.tagName).toBe("INPUT")
    expect(input.getAttribute("type") ?? "text").toBe("text")
    expect(screen.queryByTestId("ask-field-blocked-future")).toBeNull()
  })

  it("renders no input at all for a type that names a secret", () => {
    renderSheet(withSecret)

    expect(screen.getByLabelText("Service")).toBeTruthy()
    // Not a password box, not a text box — nothing that can be typed into.
    expect(screen.queryByLabelText("API key")).toBeNull()
    const blocked = screen.getByTestId("ask-field-blocked-api")
    expect(blocked.textContent).toContain("API key")
    expect(blocked.querySelector("input")).toBeNull()
  })

  it("refuses to submit a form that contains one, naming the field", async () => {
    renderSheet(withSecret)
    fireEvent.change(screen.getByLabelText("Service"), { target: { value: "Vodafone" } })

    fireEvent.click(screen.getByTestId("ask-submit"))

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(toastError.mock.calls[0][0]).toContain("API key")
    expect(toastError.mock.calls[0][0]).toContain("cannot be answered here")
    expect(onSubmit).not.toHaveBeenCalled()
    expect(onClose).not.toHaveBeenCalled()
  })

  it("keeps the field out of the preview, so nothing typed elsewhere can leak through it", () => {
    renderSheet(withSecret)
    fireEvent.change(screen.getByLabelText("Service"), { target: { value: "Vodafone" } })

    fireEvent.click(screen.getByTestId("ask-preview-toggle"))
    const preview = screen.getByTestId("ask-preview").textContent ?? ""
    expect(preview).toContain("Service: Vodafone")
    // The `Key:` line had one placeholder, it rendered empty, and the whole
    // line went with it — the same rule an unanswered optional field follows.
    expect(preview).not.toContain("Key:")
  })
})

// ---------------------------------------------------------------------------
// A secret must not survive anywhere, including the places nobody looks at
// until an incident: the envelope handed to the send path, and the console.
// ---------------------------------------------------------------------------
describe("a secret-typed value never leaves the sheet", () => {
  beforeEach(() => {
    useComposerStore.setState({ attachments: {}, drafts: {} })
    onSubmit.mockClear()
    toastError.mockClear()
  })

  it("cannot be typed, cannot be sent, and cannot be logged", async () => {
    const logs: unknown[] = []
    const spies = (["log", "info", "warn", "error", "debug"] as const).map((level) =>
      vi.spyOn(console, level).mockImplementation((...args: unknown[]) => {
        logs.push(...args)
      }),
    )

    renderSheet({
      id: "connect",
      label: "Connect",
      template: "Service: {{service}}\nKey: {{api}}",
      fields: [
        { name: "service", label: "Service", type: "text" },
        { name: "api", label: "API key", type: "password" },
      ],
    })
    fireEvent.change(screen.getByLabelText("Service"), { target: { value: "Vodafone" } })
    fireEvent.click(screen.getByTestId("ask-submit"))

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(onSubmit).not.toHaveBeenCalled()

    const everythingSaid = JSON.stringify(logs) + (toastError.mock.calls[0]?.[0] ?? "")
    expect(everythingSaid).not.toContain("sk-live")
    for (const spy of spies) spy.mockRestore()
  })

  it("is absent from the envelope even when the form is otherwise sendable", async () => {
    // A form whose secret-typed field is the ONLY unsafe one still refuses —
    // fail closed is per form, not per field, because a user cannot be asked
    // to judge which half of a questionnaire is trustworthy.
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

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(onSubmit).not.toHaveBeenCalled()
    expect(ASK_SUBMISSION_METADATA_KEY).toBe("ask_submission")
  })
})
