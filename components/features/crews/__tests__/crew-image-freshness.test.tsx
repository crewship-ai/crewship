import { render, screen, waitFor, cleanup, fireEvent } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { CrewImageFreshness } from "../crew-image-freshness"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetch(...args),
}))
const toastError = vi.fn()
const toastSuccess = vi.fn()
vi.mock("sonner", () => ({ toast: { success: (...a: unknown[]) => toastSuccess(...a), error: (...a: unknown[]) => toastError(...a) } }))

const RUNNING = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
const RESOLVED = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

function ok(body: unknown) {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}

function status(overrides: Record<string, unknown> = {}) {
  return ok({
    crew_id: "crew_1",
    image: "ghcr.io/acme/runtime:latest",
    container_id: "3f1a9c02b7de00",
    running: true,
    running_digest: RUNNING,
    resolved_digest: RESOLVED,
    behind: true,
    reason: "",
    ...overrides,
  })
}

beforeEach(() => {
  apiFetch.mockReset()
  toastError.mockReset()
  toastSuccess.mockReset()
})

afterEach(cleanup)

describe("CrewImageFreshness", () => {
  it("says the crew is behind and offers the refresh", async () => {
    apiFetch.mockResolvedValue(status())
    render(<CrewImageFreshness crewId="crew_1" workspaceId="ws_1" canEdit />)

    expect(await screen.findByTestId("crew-image-freshness-verdict")).toHaveTextContent(/behind/i)
    expect(screen.getByTestId("crew-image-refresh")).toBeInTheDocument()
  })

  // The two digests are the evidence. A card that says "behind" without them
  // asks the operator to take its word for it, and gives them nothing to paste
  // into an issue.
  it("shows both digests so the operator can see what moved", async () => {
    apiFetch.mockResolvedValue(status())
    render(<CrewImageFreshness crewId="crew_1" workspaceId="ws_1" canEdit />)

    await screen.findByTestId("crew-image-freshness-verdict")
    expect(screen.getByText(new RegExp(RUNNING.slice(7, 19)))).toBeInTheDocument()
    expect(screen.getByText(new RegExp(RESOLVED.slice(7, 19)))).toBeInTheDocument()
  })

  // The whole point of the provider's `reason` field: "not behind" because
  // nothing could be checked must never render as a green tick.
  it("does not claim the crew is current when nothing could be checked", async () => {
    apiFetch.mockResolvedValue(status({ behind: false, reason: "registry unreachable", running_digest: "", resolved_digest: "" }))
    render(<CrewImageFreshness crewId="crew_1" workspaceId="ws_1" canEdit />)

    const verdict = await screen.findByTestId("crew-image-freshness-verdict")
    expect(verdict).toHaveTextContent(/unknown/i)
    expect(verdict).not.toHaveTextContent(/^current$/i)
    expect(screen.getByTestId("crew-image-freshness-reason")).toHaveTextContent(/registry unreachable/i)
  })

  it("reports a confirmed match as current, with no refresh to press", async () => {
    apiFetch.mockResolvedValue(status({ behind: false, reason: "", running_digest: RESOLVED }))
    render(<CrewImageFreshness crewId="crew_1" workspaceId="ws_1" canEdit />)

    expect(await screen.findByTestId("crew-image-freshness-verdict")).toHaveTextContent(/current/i)
    expect(screen.queryByTestId("crew-image-refresh")).not.toBeInTheDocument()
  })

  it("posts the refresh and reports what changed", async () => {
    apiFetch
      .mockResolvedValueOnce(status())
      .mockResolvedValueOnce(ok({
        crew_id: "crew_1",
        image: "ghcr.io/acme/runtime:latest",
        previous_digest: RUNNING,
        new_digest: RESOLVED,
        container_removed: true,
      }))
      .mockResolvedValue(status({ behind: false, reason: "", running_digest: RESOLVED }))

    render(<CrewImageFreshness crewId="crew_1" workspaceId="ws_1" canEdit />)
    fireEvent.click(await screen.findByTestId("crew-image-refresh"))

    await waitFor(() => {
      expect(apiFetch).toHaveBeenCalledWith(
        expect.stringContaining("/api/v1/crews/crew_1/refresh-image"),
        expect.objectContaining({ method: "POST" }),
      )
    })
    await waitFor(() => expect(toastSuccess).toHaveBeenCalled())
  })

  // A refresh that failed must never read as a refresh that worked — the
  // common cause is a throttled registry, and an operator told "done" stops
  // looking while still on the old image.
  it("surfaces a failed refresh as an error", async () => {
    apiFetch
      .mockResolvedValueOnce(status())
      .mockResolvedValueOnce({ ok: false, status: 500, json: async () => ({ error: "registry throttled" }) } as unknown as Response)

    render(<CrewImageFreshness crewId="crew_1" workspaceId="ws_1" canEdit />)
    fireEvent.click(await screen.findByTestId("crew-image-refresh"))

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(toastSuccess).not.toHaveBeenCalled()
  })

  // 503 means no provider could answer. Rendering that as "current" would be
  // the single worst answer this card could give.
  it("says the check is unavailable rather than inventing a verdict", async () => {
    apiFetch.mockResolvedValue({ ok: false, status: 503, json: async () => ({ error: "not available" }) } as unknown as Response)
    render(<CrewImageFreshness crewId="crew_1" workspaceId="ws_1" canEdit />)

    const verdict = await screen.findByTestId("crew-image-freshness-verdict")
    expect(verdict).toHaveTextContent(/unavailable/i)
    expect(verdict).not.toHaveTextContent(/current/i)
  })

  // Refreshing pulls and force-removes a running container; a viewer sees the
  // verdict but gets no button.
  it("hides the refresh from a caller who cannot edit", async () => {
    apiFetch.mockResolvedValue(status())
    render(<CrewImageFreshness crewId="crew_1" workspaceId="ws_1" canEdit={false} />)

    await screen.findByTestId("crew-image-freshness-verdict")
    expect(screen.queryByTestId("crew-image-refresh")).not.toBeInTheDocument()
  })
})
