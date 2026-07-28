// The reveal ceremony. This is the only surface in the product that puts a
// stored plaintext on screen, so the tests here are about what must NEVER
// happen as much as about what must.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { RevealDialog, MIN_REVEAL_REASON_LENGTH } from "../reveal-dialog"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))

const GOOD_REASON = "Migrating the S3 bucket to the new account and pasting the key into Terraform"

function renderDialog(overrides: Partial<React.ComponentProps<typeof RevealDialog>> = {}) {
  const onOpenChange = vi.fn()
  const onRotateInstead = vi.fn()
  const utils = render(
    <RevealDialog
      workspaceId="ws1"
      credentialId="cred_1"
      credentialName="AWS_MAIN"
      open
      onOpenChange={onOpenChange}
      onRotateInstead={onRotateInstead}
      {...overrides}
    />,
  )
  return { ...utils, onOpenChange, onRotateInstead }
}

beforeEach(() => {
  h.apiFetch.mockReset()
  h.apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ value: "ghp_secret" }) })
})

describe("the reason floor", () => {
  it("keeps Reveal disabled until the reason is long enough to mean something", () => {
    renderDialog()
    const button = screen.getByRole("button", { name: /reveal the existing value/i })
    expect(button).toBeDisabled()

    fireEvent.change(screen.getByLabelText(/reason/i), { target: { value: "test" } })
    expect(button).toBeDisabled()

    fireEvent.change(screen.getByLabelText(/reason/i), { target: { value: GOOD_REASON } })
    expect(button).not.toBeDisabled()
  })

  it("uses the same floor the server does", () => {
    expect(MIN_REVEAL_REASON_LENGTH).toBe(20)
  })

  it("sends the trimmed reason in the request body", async () => {
    renderDialog()
    fireEvent.change(screen.getByLabelText(/reason/i), { target: { value: `  ${GOOD_REASON}  ` } })
    fireEvent.click(screen.getByRole("button", { name: /reveal the existing value/i }))

    await waitFor(() => expect(h.apiFetch).toHaveBeenCalled())
    const [url, init] = h.apiFetch.mock.calls[0]
    expect(String(url)).toContain("/api/v1/credentials/cred_1/reveal")
    expect(JSON.parse(String((init as { body?: string }).body))).toEqual({ reason: GOOD_REASON })
  })
})

describe("SEALED", () => {
  // L0 has no escape hatch, for any role. Offering the button and letting the
  // server 403 would be exactly the render-then-403 pattern this codebase has
  // been removing.
  it("cannot be revealed, and the dialog says so instead of trying", () => {
    renderDialog({ sensitivity: "SEALED" })
    fireEvent.change(screen.getByLabelText(/reason/i), { target: { value: GOOD_REASON } })
    expect(screen.getByRole("button", { name: /reveal the existing value/i })).toBeDisabled()
    expect(screen.getByText(/never revealable/i)).toBeInTheDocument()
  })

  it("allows a RESTRICTED credential through the same form", () => {
    renderDialog({ sensitivity: "RESTRICTED" })
    fireEvent.change(screen.getByLabelText(/reason/i), { target: { value: GOOD_REASON } })
    expect(screen.getByRole("button", { name: /reveal the existing value/i })).not.toBeDisabled()
  })

  // GET /credentials does not return `sensitivity` (see the report), so the
  // sheet often cannot pre-check this layer. Saying the server will is honest;
  // pretending it is satisfied is not.
  it("says the classification is checked server-side when the payload has none", () => {
    renderDialog({ sensitivity: null })
    expect(screen.getByText(/classification is checked by the server/i)).toBeInTheDocument()
  })
})

describe("the offered better path", () => {
  it("puts rotation on the same screen and hands off when it is chosen", () => {
    const { onRotateInstead } = renderDialog()
    expect(screen.getByText(/have you considered rotating/i)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /rotate instead/i }))
    expect(onRotateInstead).toHaveBeenCalled()
  })
})

describe("the result", () => {
  it("shows the value exactly once and drops the request form", async () => {
    renderDialog()
    fireEvent.change(screen.getByLabelText(/reason/i), { target: { value: GOOD_REASON } })
    fireEvent.click(screen.getByRole("button", { name: /reveal the existing value/i }))

    expect(await screen.findByTestId("revealed-value")).toHaveTextContent("ghp_secret")
    // No second bite: the reason field and the reveal button are gone.
    expect(screen.queryByLabelText(/reason/i)).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /reveal the existing value/i })).not.toBeInTheDocument()
    expect(screen.getByText(/shown once/i)).toBeInTheDocument()
  })

  it("forgets the value when the dialog closes", async () => {
    const { rerender } = renderDialog()
    fireEvent.change(screen.getByLabelText(/reason/i), { target: { value: GOOD_REASON } })
    fireEvent.click(screen.getByRole("button", { name: /reveal the existing value/i }))
    await screen.findByTestId("revealed-value")

    rerender(
      <RevealDialog
        workspaceId="ws1"
        credentialId="cred_1"
        credentialName="AWS_MAIN"
        open={false}
        onOpenChange={() => {}}
        onRotateInstead={() => {}}
      />,
    )
    rerender(
      <RevealDialog
        workspaceId="ws1"
        credentialId="cred_1"
        credentialName="AWS_MAIN"
        open
        onOpenChange={() => {}}
        onRotateInstead={() => {}}
      />,
    )

    expect(screen.queryByTestId("revealed-value")).not.toBeInTheDocument()
    expect((screen.getByLabelText(/reason/i) as HTMLTextAreaElement).value).toBe("")
  })
})

describe("refusals", () => {
  // Each layer's message names the wall that stopped you and what to do about
  // it. Flattening them all to "Forbidden" is how a user ends up asking in
  // chat instead of opening Settings.
  it("shows the server's own refusal text verbatim", async () => {
    h.apiFetch.mockResolvedValue({
      ok: false,
      status: 403,
      json: async () => ({
        error:
          "Reveal is disabled for this workspace. An OWNER must enable it in Settings → Access & Secrets first.",
      }),
    })
    renderDialog()
    fireEvent.change(screen.getByLabelText(/reason/i), { target: { value: GOOD_REASON } })
    fireEvent.click(screen.getByRole("button", { name: /reveal the existing value/i }))

    expect(await screen.findByRole("alert")).toHaveTextContent(/an owner must enable it in settings/i)
    expect(screen.queryByTestId("revealed-value")).not.toBeInTheDocument()
  })

  it("falls back to the status code when the body carries no message", async () => {
    h.apiFetch.mockResolvedValue({ ok: false, status: 500, json: async () => ({}) })
    renderDialog()
    fireEvent.change(screen.getByLabelText(/reason/i), { target: { value: GOOD_REASON } })
    fireEvent.click(screen.getByRole("button", { name: /reveal the existing value/i }))
    expect(await screen.findByRole("alert")).toHaveTextContent(/http 500/i)
  })

  it("says nothing was revealed when the request never lands", async () => {
    h.apiFetch.mockRejectedValue(new TypeError("offline"))
    renderDialog()
    fireEvent.change(screen.getByLabelText(/reason/i), { target: { value: GOOD_REASON } })
    fireEvent.click(screen.getByRole("button", { name: /reveal the existing value/i }))
    expect(await screen.findByRole("alert")).toHaveTextContent(/nothing was revealed/i)
  })
})
