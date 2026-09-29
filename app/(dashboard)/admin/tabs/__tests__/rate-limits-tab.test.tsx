// Tests for Admin → Limits: rows render from the API, edits collect in one
// save bar that PUTs each changed value (with the workspace_id query param),
// Reset DELETEs an override and only exists where there is one, and an
// out-of-range value is blocked client-side (Save disabled, no PUT fired).

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
    description: "Login / token-refresh throttle. Read-only session polls do not count.",
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

describe("save bar (PUT)", () => {
  it("collects edits in one bar and PUTs each changed limit", async () => {
    h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (init?.method === "PUT") {
        const value = JSON.parse(String(init.body)).value
        return url.includes("http.api") ? ok(makeLimiter({ key: "http.api_per_min", display_name: "API endpoints", value, default: 60, overridden: true }))
          : ok(makeLimiter({ value, overridden: true }))
      }
      return ok({ limiters: [makeLimiter(), makeLimiter({ key: "http.api_per_min", display_name: "API endpoints", value: 60, default: 60 })] })
    })
    render(<RateLimitsTab workspaceId="ws1" />)

    expect(screen.queryByRole("button", { name: "Save" })).toBeNull()
    fireEvent.change(await screen.findByLabelText("Auth endpoints value"), { target: { value: "25" } })
    fireEvent.change(screen.getByLabelText("API endpoints value"), { target: { value: "90" } })
    expect(screen.getByRole("region", { name: "Unsaved changes" })).toHaveTextContent("2 unsaved changes")

    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => {
      expect(h.apiFetch).toHaveBeenCalledWith(
        "/api/v1/admin/rate-limits/http.auth_per_min?workspace_id=ws1",
        expect.objectContaining({ method: "PUT", body: JSON.stringify({ value: 25 }) }),
      )
      expect(h.apiFetch).toHaveBeenCalledWith(
        "/api/v1/admin/rate-limits/http.api_per_min?workspace_id=ws1",
        expect.objectContaining({ method: "PUT", body: JSON.stringify({ value: 90 }) }),
      )
    })
    // The returned limiters are merged in: the bar is gone, the rows say which way they moved.
    await waitFor(() => expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull())
    expect(screen.getByText(/looser · default 10/)).toBeInTheDocument()
  })

  it("Discard puts every edited field back", async () => {
    h.apiFetch.mockImplementation(async () => ok({ limiters: [makeLimiter()] }))
    render(<RateLimitsTab workspaceId="ws1" />)
    const input = await screen.findByLabelText("Auth endpoints value")
    fireEvent.change(input, { target: { value: "25" } })
    fireEvent.click(screen.getByRole("button", { name: "Discard" }))
    expect(input).toHaveValue("10")
    expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull()
  })

  it("surfaces an API error (e.g. 400 out-of-range) and keeps the edit", async () => {
    const { toast } = await import("sonner")
    h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (init?.method === "PUT") return fail(400, { error: "value out of range" })
      return ok({ limiters: [makeLimiter({ max: 100000 })] })
    })
    render(<RateLimitsTab workspaceId="ws1" />)

    fireEvent.change(await screen.findByLabelText("Auth endpoints value"), { target: { value: "25" } })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith("Auth endpoints: value out of range")
    })
    expect(screen.getByRole("region", { name: "Unsaved changes" })).toHaveTextContent("1 unsaved change")
  })
})

describe("reset (DELETE)", () => {
  it("DELETEs the override for an overridden limiter", async () => {
    h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (init?.method === "DELETE") return ok(makeLimiter({ value: 10, overridden: false }))
      return ok({ limiters: [makeLimiter({ value: 25, overridden: true })] })
    })
    render(<RateLimitsTab workspaceId="ws1" />)

    fireEvent.click(await screen.findByRole("button", { name: /Reset Auth endpoints to 10/ }))

    await waitFor(() => {
      expect(h.apiFetch).toHaveBeenCalledWith(
        "/api/v1/admin/rate-limits/http.auth_per_min?workspace_id=ws1",
        expect.objectContaining({ method: "DELETE" }),
      )
    })
  })

  it("offers no Reset where the limiter is at its default", async () => {
    h.apiFetch.mockImplementation(async () => ok({ limiters: [makeLimiter({ overridden: false })] }))
    render(<RateLimitsTab workspaceId="ws1" />)
    await screen.findByText("Auth endpoints")
    expect(screen.queryByRole("button", { name: /Reset Auth endpoints/ })).toBeNull()
  })
})

describe("client-side validation", () => {
  it("blocks an out-of-range value: Save stays disabled and no PUT is fired", async () => {
    h.apiFetch.mockImplementation(async () => ok({ limiters: [makeLimiter({ min: 1, max: 100 })] }))
    render(<RateLimitsTab workspaceId="ws1" />)

    fireEvent.change(await screen.findByLabelText("Auth endpoints value"), { target: { value: "999" } })

    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled()
    expect(screen.getByText("Must be between 1 and 100")).toBeInTheDocument()
    expect(h.apiFetch.mock.calls.every((c) => (c[1] as RequestInit | undefined)?.method !== "PUT")).toBe(true)
  })
})

describe("reading a limit", () => {
  const LIST = {
    limiters: [
      makeLimiter(),
      makeLimiter({ key: "login.lockout_threshold", group: "Login", display_name: "Account lockout threshold", unit: "attempts", value: 20, default: 50, overridden: true }),
      makeLimiter({ key: "pages.public_view_per_hour", group: "Pages", display_name: "Public page views", unit: "views/hour" }),
    ],
  }

  it("keeps the description to its first sentence and the rest on hover", async () => {
    h.apiFetch.mockResolvedValue(ok(LIST))
    render(<RateLimitsTab workspaceId="ws-1" />)
    const [d] = await screen.findAllByText("Login / token-refresh throttle.")
    expect(d).toHaveAttribute("title", "Login / token-refresh throttle. Read-only session polls do not count.")
  })

  it("counts what differs from the defaults and shows only those on request", async () => {
    h.apiFetch.mockResolvedValue(ok(LIST))
    render(<RateLimitsTab workspaceId="ws-1" />)
    expect(await screen.findByText(/tighter · default 50/)).toBeInTheDocument()
    expect(document.querySelector("[data-slot=settings-summary]")).toHaveTextContent("1 changed from default")
    fireEvent.click(screen.getByRole("button", { name: "Changed" }))
    expect(screen.queryByText("Auth endpoints")).toBeNull()
    expect(screen.getByText("Account lockout threshold")).toBeInTheDocument()
  })
})
