// Tests for CredentialForm env-var-name validation — the credential
// name doubles as the ENV variable agents read, so newly typed names
// must match ^[A-Z_][A-Z0-9_]*$. Legacy (pre-existing) invalid names
// warn but stay submittable so old credentials don't become
// uneditable.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { CredentialForm } from "../credential-form"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))

vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => h.apiFetch(...args),
}))

beforeEach(() => {
  h.apiFetch.mockReset()
  h.apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => [] })
})

function renderForm(props: Partial<React.ComponentProps<typeof CredentialForm>> = {}) {
  const onSubmit = vi.fn().mockResolvedValue(null)
  render(
    <CredentialForm
      workspaceId="ws1"
      mode="create"
      hideValue
      onSubmit={onSubmit}
      onCancel={() => {}}
      {...props}
    />,
  )
  return { onSubmit }
}

const nameInput = () => screen.getByLabelText("Name") as HTMLInputElement
const submit = () => fireEvent.click(screen.getByRole("button", { name: /save/i }))

describe("create mode", () => {
  it("accepts a valid env var name", async () => {
    const { onSubmit } = renderForm()
    fireEvent.change(nameInput(), { target: { value: "STRIPE_API_KEY" } })
    submit()
    await waitFor(() => expect(onSubmit).toHaveBeenCalled())
    expect(onSubmit.mock.calls[0][0].name).toBe("STRIPE_API_KEY")
  })

  it("shows the inline error with a normalised suggestion after blur", () => {
    renderForm()
    fireEvent.change(nameInput(), { target: { value: "stripe api-key" } })
    // No premature nagging while the field is still focused…
    expect(screen.queryByText(/must be a valid env var name/i)).not.toBeInTheDocument()
    fireEvent.blur(nameInput())
    expect(screen.getByText(/must be a valid env var name/i)).toBeInTheDocument()

    // …and the one-click fix applies the normalised name.
    fireEvent.click(screen.getByRole("button", { name: "Use STRIPE_API_KEY" }))
    expect(nameInput().value).toBe("STRIPE_API_KEY")
    expect(screen.queryByText(/must be a valid env var name/i)).not.toBeInTheDocument()
  })

  it("blocks submit on an invalid name", async () => {
    const { onSubmit } = renderForm()
    fireEvent.change(nameInput(), { target: { value: "stripe key" } })
    submit()
    await screen.findAllByText(/must be a valid env var name/i)
    expect(onSubmit).not.toHaveBeenCalled()
  })
})

describe("edit mode with a legacy invalid name", () => {
  it("warns but still submits when the legacy name is left unchanged", async () => {
    const { onSubmit } = renderForm({
      mode: "edit",
      initial: { name: "my legacy key" },
    })
    // Warning is visible immediately (amber, non-blocking).
    expect(screen.getByText(/isn't a valid env var name/i)).toBeInTheDocument()
    submit()
    await waitFor(() => expect(onSubmit).toHaveBeenCalled())
    expect(onSubmit.mock.calls[0][0].name).toBe("my legacy key")
  })

  it("blocks submit when the name is changed to a different invalid value", async () => {
    const { onSubmit } = renderForm({
      mode: "edit",
      initial: { name: "my legacy key" },
    })
    fireEvent.change(nameInput(), { target: { value: "another bad name" } })
    submit()
    await screen.findAllByText(/must be a valid env var name/i)
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it("accepts changing a legacy name to a valid one", async () => {
    const { onSubmit } = renderForm({
      mode: "edit",
      initial: { name: "my legacy key" },
    })
    fireEvent.change(nameInput(), { target: { value: "MY_LEGACY_KEY" } })
    submit()
    await waitFor(() => expect(onSubmit).toHaveBeenCalled())
    expect(onSubmit.mock.calls[0][0].name).toBe("MY_LEGACY_KEY")
  })
})
