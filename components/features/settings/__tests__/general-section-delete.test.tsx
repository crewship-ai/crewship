import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within, cleanup } from "@testing-library/react"
import { GeneralSection } from "../sections/general-section"

// #881 — the workspace delete flow requires typing the slug to confirm and
// sends { confirm_slug } in the DELETE body (the server re-validates it).

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

const toastError = vi.fn()
vi.mock("@/lib/toast", () => ({ toast: { error: (...a: unknown[]) => toastError(...a), success: vi.fn() } }))

// The privileged-credentials card GETs the same /api/v1/workspaces/{id} URL
// this test matches on, and would eat the mockResolvedValueOnce below. It has
// its own test file; here it is out of scope.
vi.mock("@/components/features/settings/sections/privileged-credentials-card", () => ({
  PrivilegedCredentialsCard: () => <div data-testid="privileged-credentials-card" />,
}))

function renderSection() {
  return render(
    <GeneralSection
      workspaceId="ws1"
      orgName="Acme Robotics"
      orgSlug="acme"
      preferredLanguage={null}
      agentCount={3}
      crewCount={2}
      memberCount={4}
      role="OWNER"
      onUpdated={vi.fn()}
      onDelete={vi.fn()}
    />,
  )
}

function openDeleteDialog() {
  renderSection()
  // The danger-zone trigger button.
  fireEvent.click(screen.getByRole("button", { name: /^delete workspace…$/i }))
  return screen.getByRole("alertdialog")
}

describe("GeneralSection delete flow (#881)", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
    toastError.mockReset()
    apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ success: true }) })
  })

  it("keeps the confirm button disabled until the slug matches", () => {
    const dialog = openDeleteDialog()
    const confirmBtn = within(dialog).getByRole("button", { name: /^delete workspace$/i })
    expect((confirmBtn as HTMLButtonElement).disabled).toBe(true)

    const input = within(dialog).getByLabelText(/to confirm/i)
    fireEvent.change(input, { target: { value: "wrong" } })
    expect((confirmBtn as HTMLButtonElement).disabled).toBe(true)

    fireEvent.change(input, { target: { value: "acme" } })
    expect((confirmBtn as HTMLButtonElement).disabled).toBe(false)
  })

  it("sends DELETE with { confirm_slug } once the slug is typed", async () => {
    const dialog = openDeleteDialog()
    fireEvent.change(within(dialog).getByLabelText(/to confirm/i), { target: { value: "acme" } })
    fireEvent.click(within(dialog).getByRole("button", { name: /^delete workspace$/i }))

    await waitFor(() => {
      const call = apiFetch.mock.calls.find((c) => String(c[0]).startsWith("/api/v1/workspaces/ws1"))
      expect(call).toBeTruthy()
      expect(call![1]).toMatchObject({ method: "DELETE" })
      expect(JSON.parse(call![1].body)).toEqual({ confirm_slug: "acme" })
    })
  })

  it("surfaces the server error message on failure", async () => {
    // The delete endpoint now emits RFC 7807 problem+json ({ detail }) via
    // writeProblem (#890) — the FE reads detail ?? error, so mock the shape
    // the handler actually returns.
    apiFetch.mockResolvedValueOnce({
      ok: false, status: 409, json: async () => ({ detail: "Cannot delete your only workspace" }),
    })
    const dialog = openDeleteDialog()
    fireEvent.change(within(dialog).getByLabelText(/to confirm/i), { target: { value: "acme" } })
    fireEvent.click(within(dialog).getByRole("button", { name: /^delete workspace$/i }))

    // Errors are the one corner toast that stays until closed (KIT-04).
    await waitFor(() => expect(toastError).toHaveBeenCalledWith(
      "Couldn’t delete the workspace",
      expect.objectContaining({ description: "Cannot delete your only workspace" }),
    ))
  })

  it("typeToConfirm keeps the dialog open on 4xx", async () => {
    apiFetch.mockResolvedValueOnce({
      ok: false, status: 409, json: async () => ({ detail: "Cannot delete your only workspace" }),
    })
    const dialog = openDeleteDialog()
    fireEvent.change(within(dialog).getByLabelText(/to confirm/i), { target: { value: "acme" } })
    fireEvent.click(within(dialog).getByRole("button", { name: /^delete workspace$/i }))

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    // The person is where they were: the dialog, the typed slug, a live button.
    expect(screen.getByRole("alertdialog")).toBeInTheDocument()
    expect((within(dialog).getByLabelText(/to confirm/i) as HTMLInputElement).value).toBe("acme")
    await waitFor(() => expect((within(dialog).getByRole("button", { name: /^delete workspace$/i }) as HTMLButtonElement).disabled).toBe(false))
  })

  it("says what is lost before anyone types", () => {
    const dialog = openDeleteDialog()
    expect(within(dialog).getByText(/crews, agents, credentials/i)).toBeInTheDocument()
    expect(within(dialog).getByText(/cannot be undone/i)).toBeInTheDocument()
  })
})
