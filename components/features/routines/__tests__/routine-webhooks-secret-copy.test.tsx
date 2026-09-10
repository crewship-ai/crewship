/**
 * The signing secret is not optional, and this screen used to say it was.
 *
 * Three strings described the LEGACY behaviour: "Optionally protect it with an
 * HMAC signing secret", a field labelled "Signing secret (optional)", and a
 * placeholder reading "leave empty to skip HMAC verification". All three were
 * true once — pipeline.Webhook.Verify returned nil for an empty SigningSecret,
 * so an unsigned POST to the public URL passed the verification step. That
 * hole was closed: internal/api/pipeline_webhooks.go now mints a 32-byte
 * secret server-side when the caller supplies none, and reveals it once.
 *
 * So the copy was not merely stale, it was an instruction to do something the
 * product no longer permits — "leave it empty and no signature is required" is
 * advice that now produces a webhook with a secret the operator never saw and
 * a sender that cannot sign. These tests pin the corrected sentences.
 */
import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/hooks/use-pipeline-webhooks", () => ({
  usePipelineWebhooks: () => ({
    webhooks: [],
    loading: false,
    error: null,
    refresh: vi.fn(),
    create: vi.fn(),
    update: vi.fn(),
    remove: vi.fn(),
  }),
}))

import { RoutineWebhooksTab } from "../routine-webhooks-tab"

afterEach(cleanup)

function renderTab() {
  return render(<RoutineWebhooksTab workspaceId="ws-1" pipelineId="p-1" slug="nightly" />)
}

describe("the webhook signing-secret copy", () => {
  it("never calls the secret optional or offers to skip verification", () => {
    const { baseElement } = renderTab()
    fireEvent.click(screen.getByRole("button", { name: /add webhook/i }))
    const text = baseElement.textContent ?? ""

    expect(text).not.toContain("Signing secret (optional)")
    expect(text).not.toContain("Optionally protect it")
    expect(baseElement.innerHTML).not.toContain("skip HMAC verification")
  })

  it("labels the field as the required thing it is", () => {
    renderTab()
    fireEvent.click(screen.getByRole("button", { name: /add webhook/i }))
    expect(screen.getByText("Signing secret")).toBeTruthy()
  })

  it("says an empty field means the server generates one, not that signing is off", () => {
    const { baseElement } = renderTab()
    fireEvent.click(screen.getByRole("button", { name: /add webhook/i }))

    expect(screen.getByPlaceholderText("leave empty and one is generated for you")).toBeTruthy()
    expect(baseElement.textContent).toContain("Signing cannot be turned off")
  })

  it("tells the empty state the same thing the form does", () => {
    const { baseElement } = renderTab()
    expect(baseElement.textContent).toContain("Every endpoint is HMAC-signed")
    expect(baseElement.textContent).toContain("shown once at creation")
  })
})
