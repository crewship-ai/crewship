// Tests for Admin → Rate Limiters: rows render from the API, Save PUTs the
// edited value (with the workspace_id query param), Reset DELETEs the
// override, and an out-of-range value is blocked client-side (Save disabled,
// no PUT fired).

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { RateLimitsTab } from "../rate-limits-tab"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => h.apiFetch(...args),
}))

function ok(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}

function fail(status: number, body: unknown = {}): Response {
  return { ok: false, status, json: async () => body } as unknown as Response
}

function makeLimiter(overrides: Record<string, unknown> = {}) {
  return {
    key: "http.auth_per_min",
    group: "HTTP (per-IP)",
    display_name: "Auth endpoints",
    description: "Login / token-refresh throttle",
    unit: "req/min",
    default: 10,
    value: 10,
    min: 1,
    max: 100000,
    overridden: false,
    ...overrides,
  }
}

beforeEach(() => {
  h.apiFetch.mockReset()
})

describe("rendering", () => {
  it("renders a row per limiter from the mocked list", async () => {
    h.apiFetch.mockImplementation(async () =>
      ok({ limiters: [
        makeLimiter(),
        makeLimiter({ key: "http.api_per_min", display_name: "API endpoints", group: "HTTP (per-IP)", value: 60, default: 60 }),
      ] }),
    )
    render(<RateLimitsTab workspaceId="ws1" />)

    expect(await screen.findByText("Auth endpoints")).toBeInTheDocument()
    expect(screen.getByText("API endpoints")).toBeInTheDocument()
    // Initial GET carries the workspace_id query param.
    expect(h.apiFetch).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/admin/rate-limits?workspace_id=ws1"),
    )
  })

  it("shows an error state when the list fetch fails", async () => {
    h.apiFetch.mockImplementation(async () => fail(500))
    render(<RateLimitsTab workspaceId="ws1" />)
    expect(await screen.findByText(/Failed to load rate limiters/)).toBeInTheDocument()
  })
})

describe("save (PUT)", () => {
  it("PUTs the edited value to the limiter key with the workspace_id param", async () => {
    h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (init?.method === "PUT") return ok(makeLimiter({ value: 25, overridden: true }))
      return ok({ limiters: [makeLimiter()] })
    })
    render(<RateLimitsTab workspaceId="ws1" />)

    const input = await screen.findByLabelText("Auth endpoints value")
    fireEvent.change(input, { target: { value: "25" } })

    const save = screen.getByRole("button", { name: "Save" })
    expect(save).not.toBeDisabled()
    fireEvent.click(save)

    await waitFor(() => {
      expect(h.apiFetch).toHaveBeenCalledWith(
        "/api/v1/admin/rate-limits/http.auth_per_min?workspace_id=ws1",
        expect.objectContaining({
          method: "PUT",
          body: JSON.stringify({ value: 25 }),
        }),
      )
    })
    // The returned limiter is merged in — the row now reads Overridden.
    expect(await screen.findByText("Overridden")).toBeInTheDocument()
  })

  it("surfaces an API error (e.g. 400 out-of-range) without a client block bypass", async () => {
    const { toast } = await import("sonner")
    h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (init?.method === "PUT") return fail(400, { error: "value out of range" })
      // max is small so 25 is a valid client-side value but the server rejects it.
      return ok({ limiters: [makeLimiter({ max: 100000 })] })
    })
    render(<RateLimitsTab workspaceId="ws1" />)

    const input = await screen.findByLabelText("Auth endpoints value")
    fireEvent.change(input, { target: { value: "25" } })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith("value out of range")
    })
  })
})

describe("reset (DELETE)", () => {
  it("DELETEs the override for an overridden limiter", async () => {
    h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (init?.method === "DELETE") return ok(makeLimiter({ value: 10, overridden: false }))
      return ok({ limiters: [makeLimiter({ value: 25, overridden: true })] })
    })
    render(<RateLimitsTab workspaceId="ws1" />)

    const reset = await screen.findByRole("button", { name: /Reset Auth endpoints to default/ })
    expect(reset).not.toBeDisabled()
    fireEvent.click(reset)

    await waitFor(() => {
      expect(h.apiFetch).toHaveBeenCalledWith(
        "/api/v1/admin/rate-limits/http.auth_per_min?workspace_id=ws1",
        expect.objectContaining({ method: "DELETE" }),
      )
    })
  })

  it("disables Reset when the limiter is not overridden", async () => {
    h.apiFetch.mockImplementation(async () => ok({ limiters: [makeLimiter({ overridden: false })] }))
    render(<RateLimitsTab workspaceId="ws1" />)

    const reset = await screen.findByRole("button", { name: /Reset Auth endpoints to default/ })
    expect(reset).toBeDisabled()
  })
})

describe("client-side validation", () => {
  it("blocks an out-of-range value: Save stays disabled and no PUT is fired", async () => {
    h.apiFetch.mockImplementation(async () => ok({ limiters: [makeLimiter({ min: 1, max: 100 })] }))
    render(<RateLimitsTab workspaceId="ws1" />)

    const input = await screen.findByLabelText("Auth endpoints value")
    fireEvent.change(input, { target: { value: "999" } }) // above max 100

    const save = screen.getByRole("button", { name: "Save" })
    expect(save).toBeDisabled()
    expect(screen.getByText("must be between 1 and 100")).toBeInTheDocument()

    // No PUT ever fires — only the initial GET(s).
    expect(h.apiFetch.mock.calls.every((c) => (c[1] as RequestInit | undefined)?.method !== "PUT")).toBe(true)
  })
})
