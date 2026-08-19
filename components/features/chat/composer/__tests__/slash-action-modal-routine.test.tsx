import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

// =============================================================================
// Running a routine from the chat palette.
//
// The modal was built for the four platform slash actions, each with a
// hand-written endpoint and body. A per-routine entry has neither: its id
// carries the routine's slug, its endpoint is derived from that, and its body
// is the form's values restored to the JSON types the routine declared.
//
// What this file pins is the part a user would notice breaking — the form
// opens on the routine's own defaults, the POST goes to that routine's run
// endpoint, and an integer leaves as a number rather than as the string
// somebody typed.
// =============================================================================

const toastError = vi.fn()
const toastSuccess = vi.fn()
vi.mock("sonner", () => ({
  toast: {
    error: (m: string) => toastError(m),
    success: (m: string) => toastSuccess(m),
    info: vi.fn(),
  },
}))

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetch(...args),
}))

import { SlashActionModal } from "../slash-action-modal"
import type { SlashActionSchema } from "@/hooks/use-slash-commands"

// The routine this feature was built against, as the server's catalog
// hands it over (internal/api/slash_routine_catalog.go).
const msn: SlashActionSchema = {
  id: "routine.run:msn-etn-podklady",
  label: "Monthly accounting pack",
  label_cs: "Účetní podklady za měsíc",
  icon: "receipt",
  capability: "routine.run",
  form_schema: [
    { name: "obdobi", type: "text", value_type: "string" },
    { name: "ucetnictvi_root", type: "text", value_type: "string", default: "Unify - Účetnictví" },
    { name: "vypis_odesilatel", type: "text", value_type: "string", default: "info@rb.cz" },
  ],
}

const typed: SlashActionSchema = {
  id: "routine.run:typed",
  label: "Typed routine",
  capability: "routine.run",
  form_schema: [
    { name: "count", type: "number", value_type: "integer" },
    { name: "opts", type: "textarea", value_type: "object" },
  ],
}

function ok(body: unknown = { run_id: "run_1", status: "COMPLETED" }) {
  return {
    ok: true,
    status: 200,
    json: async () => body,
    text: async () => JSON.stringify(body),
  }
}

function lastBody(): Record<string, unknown> {
  const call = apiFetch.mock.calls.at(-1)
  return JSON.parse((call![1] as { body: string }).body)
}

beforeEach(() => {
  vi.clearAllMocks()
  apiFetch.mockResolvedValue(ok())
})

describe("slash action modal — running a routine", () => {
  it("opens the form prefilled with the routine's declared defaults", () => {
    render(<SlashActionModal command={msn} workspaceId="ws-1" onClose={vi.fn()} />)
    // The two inputs that declare a default open holding it; the one that
    // doesn't opens empty, because for msn-etn-podklady an empty period
    // is what means "the previous month".
    expect(screen.getByLabelText(/ucetnictvi root/i)).toHaveValue("Unify - Účetnictví")
    expect(screen.getByLabelText(/vypis odesilatel/i)).toHaveValue("info@rb.cz")
    expect(screen.getByLabelText(/obdobi/i)).toHaveValue("")
  })

  it("does not paste the conversation into a routine input named prompt or description", () => {
    // The host seeds those two names with up to 4000 characters of
    // transcript, for the four "…from this conversation" actions. Both
    // are perfectly ordinary names for a routine input, and a routine
    // that declares one must keep its own default rather than silently
    // receive the last six turns of chat.
    const withCommonNames: SlashActionSchema = {
      ...msn,
      form_schema: [
        { name: "prompt", type: "text", value_type: "string", default: "summarise the month" },
        { name: "description", type: "textarea", value_type: "string" },
      ],
    }
    render(
      <SlashActionModal
        command={withCommonNames}
        workspaceId="ws-1"
        contextPreFill={{ prompt: "You: hello\n\nAssistant: hi", description: "chat transcript" }}
        onClose={vi.fn()}
      />,
    )
    expect(screen.getByLabelText(/prompt/i)).toHaveValue("summarise the month")
    expect(screen.getByLabelText(/description/i)).toHaveValue("")
  })

  it("posts to the routine's run endpoint, addressed by its slug", async () => {
    render(<SlashActionModal command={msn} workspaceId="ws-1" onClose={vi.fn()} />)
    fireEvent.change(screen.getByLabelText(/obdobi/i), { target: { value: "2026-07" } })
    fireEvent.click(screen.getByRole("button", { name: msn.label }))

    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    expect(apiFetch.mock.calls[0][0]).toBe("/api/v1/workspaces/ws-1/pipelines/msn-etn-podklady/run")
    expect(lastBody()).toEqual({
      inputs: {
        obdobi: "2026-07",
        ucetnictvi_root: "Unify - Účetnictví",
        vypis_odesilatel: "info@rb.cz",
      },
    })
  })

  it("omits a field the user left empty so the routine's own default applies", async () => {
    render(<SlashActionModal command={msn} workspaceId="ws-1" onClose={vi.fn()} />)
    fireEvent.change(screen.getByLabelText(/vypis odesilatel/i), { target: { value: "" } })
    fireEvent.click(screen.getByRole("button", { name: msn.label }))

    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    const inputs = lastBody().inputs as Record<string, unknown>
    // Sending "" would replace a server-side default with a blank.
    expect("vypis_odesilatel" in inputs).toBe(false)
    expect("obdobi" in inputs).toBe(false)
    expect(inputs.ucetnictvi_root).toBe("Unify - Účetnictví")
  })

  it("sends an integer as a number, not as the string that was typed", async () => {
    render(<SlashActionModal command={typed} workspaceId="ws-1" onClose={vi.fn()} />)
    fireEvent.change(screen.getByLabelText(/count/i), { target: { value: "42" } })
    fireEvent.change(screen.getByLabelText(/opts/i), { target: { value: '{"k":"v"}' } })
    fireEvent.click(screen.getByRole("button", { name: typed.label }))

    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    // A `code` step sees inputs with their original types, so an
    // integer arriving as "42" fails the run at that step.
    expect(lastBody()).toEqual({ inputs: { count: 42, opts: { k: "v" } } })
  })

  it("refuses a value it cannot restore, without sending anything", async () => {
    render(<SlashActionModal command={typed} workspaceId="ws-1" onClose={vi.fn()} />)
    fireEvent.change(screen.getByLabelText(/opts/i), { target: { value: "{not json" } })
    fireEvent.click(screen.getByRole("button", { name: typed.label }))

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    // The form stays open with the offending field named — posting the
    // string and reading back the server's 400 would say less, later.
    expect(toastError.mock.calls[0][0]).toMatch(/opts/)
    expect(apiFetch).not.toHaveBeenCalled()
    // And the modal is usable again rather than stuck mid-submit.
    expect(screen.getByRole("button", { name: typed.label })).not.toBeDisabled()
  })

  it("still refuses an empty required field before building a body", async () => {
    const required: SlashActionSchema = {
      ...msn,
      form_schema: [{ name: "obdobi", type: "text", value_type: "string", required: true }],
    }
    render(<SlashActionModal command={required} workspaceId="ws-1" onClose={vi.fn()} />)
    fireEvent.click(screen.getByRole("button", { name: required.label }))

    await waitFor(() => expect(toastError).toHaveBeenCalledWith("obdobi is required"))
    expect(apiFetch).not.toHaveBeenCalled()
  })

  it("closes and reports success once the run starts", async () => {
    const onClose = vi.fn()
    const onSuccess = vi.fn()
    render(
      <SlashActionModal command={msn} workspaceId="ws-1" onClose={onClose} onSuccess={onSuccess} />,
    )
    fireEvent.click(screen.getByRole("button", { name: msn.label }))

    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(toastSuccess).toHaveBeenCalled()
    expect(onSuccess).toHaveBeenCalledWith(msn, { run_id: "run_1", status: "COMPLETED" })
  })

  it("surfaces a 403 as a permission message rather than a raw body", async () => {
    // A member whose routine.run grant was revoked between the palette
    // opening and the submit.
    apiFetch.mockResolvedValue({
      ok: false,
      status: 403,
      text: async () => `{"detail":"Forbidden"}`,
      json: async () => ({}),
    })
    render(<SlashActionModal command={msn} workspaceId="ws-1" onClose={vi.fn()} />)
    fireEvent.click(screen.getByRole("button", { name: msn.label }))

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(toastError.mock.calls[0][0]).toMatch(/permission/i)
  })
})
