import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

// =============================================================================
// Characterization test for the slash action modal.
//
// It exists because the modal's field renderer was pulled out into a shared
// component (asks/form-field.tsx) so the ask sheet and the slash modal render
// one set of inputs instead of two. An extraction is only safe if the thing it
// was extracted from is pinned first: this test was written against the modal
// as it stood and must go on passing byte-for-byte afterwards — same labels,
// same input kinds, same required-field wording, same POST body.
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

const routine: SlashActionSchema = {
  id: "routine",
  label: "New routine",
  capability: "routine.create",
  form_schema: [
    { name: "name", type: "text", required: true },
    { name: "cron", type: "cron", required: true },
    { name: "timezone", type: "timezone" },
  ],
}

const everyType: SlashActionSchema = {
  id: "routine",
  label: "Types",
  capability: "routine.create",
  form_schema: [
    { name: "plain_name", type: "text" },
    { name: "body", type: "textarea" },
    { name: "cron", type: "cron" },
    { name: "timezone", type: "timezone" },
    { name: "priority", type: "priority" },
    { name: "memory_scope", type: "memory_scope" },
    { name: "credential_type", type: "credential_type" },
    { name: "secret", type: "secret" },
    { name: "slug", type: "slug" },
    { name: "brand_new", type: "not_a_real_type" },
  ],
}

const onClose = vi.fn()

describe("slash action modal — behaviour pinned across the field-renderer extraction", () => {
  beforeEach(() => {
    toastError.mockClear()
    toastSuccess.mockClear()
    apiFetch.mockReset()
    onClose.mockClear()
  })

  it("labels a field by its underscored name, exactly as it always has", () => {
    render(<SlashActionModal command={everyType} workspaceId="ws-1" onClose={onClose} />)

    // "plain_name" → "plain name" in the DOM (the capitalisation is CSS).
    expect(screen.getByLabelText("plain name").tagName).toBe("INPUT")
    expect(screen.getByLabelText("body").tagName).toBe("TEXTAREA")
    expect(screen.getByLabelText(/^cron/)).toHaveAttribute("placeholder", "0 7 * * MON")
    expect(screen.getByLabelText("secret")).toHaveAttribute("type", "password")
    expect(screen.getByLabelText("slug")).toHaveAttribute("placeholder", "kebab-case-slug")
    expect(screen.getByLabelText("timezone")).toHaveAttribute("role", "combobox")
    expect(screen.getByLabelText("priority")).toHaveAttribute("role", "combobox")
    expect(screen.getByLabelText("memory scope")).toHaveAttribute("role", "combobox")
    expect(screen.getByLabelText("credential type")).toHaveAttribute("role", "combobox")

    // The by-design fallback: a type the dashboard has never heard of is a
    // text input, so the server can add one without a frontend release.
    const unknown = screen.getByLabelText("brand new")
    expect(unknown.tagName).toBe("INPUT")
    // No explicit type attribute — an <input> with none IS a text input, and
    // that is what this modal has always emitted.
    expect(unknown.getAttribute("type") ?? "text").toBe("text")
  })

  it("refuses an empty required field with the field's raw name", async () => {
    render(<SlashActionModal command={routine} workspaceId="ws-1" onClose={onClose} />)

    fireEvent.submit(document.querySelector("form")!)

    await waitFor(() => expect(toastError).toHaveBeenCalledWith("name is required"))
    expect(apiFetch).not.toHaveBeenCalled()
  })

  it("POSTs the same body to the same endpoint", async () => {
    apiFetch.mockResolvedValue({ ok: true, json: async () => ({ id: "r1" }) })
    render(<SlashActionModal command={routine} workspaceId="ws-1" onClose={onClose} />)

    fireEvent.change(screen.getByLabelText(/^name/), { target: { value: "Monthly close" } })
    fireEvent.change(screen.getByLabelText(/^cron/), { target: { value: "0 7 1 * *" } })
    fireEvent.submit(document.querySelector("form")!)

    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
    const [url, init] = apiFetch.mock.calls[0] as [string, RequestInit]
    expect(url).toBe("/api/v1/workspaces/ws-1/pipeline-schedules")
    expect(JSON.parse(init.body as string)).toEqual({
      name: "Monthly close",
      cron_expr: "0 7 1 * *",
      timezone: "UTC",
    })
    await waitFor(() => expect(onClose).toHaveBeenCalled())
  })

  it("pre-fills from conversation context", () => {
    render(
      <SlashActionModal
        command={routine}
        workspaceId="ws-1"
        contextPreFill={{ name: "from the chat" }}
        onClose={onClose}
      />,
    )
    expect(screen.getByLabelText(/^name/)).toHaveValue("from the chat")
  })
})
