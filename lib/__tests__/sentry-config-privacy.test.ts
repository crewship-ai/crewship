import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const { init } = vi.hoisted(() => ({ init: vi.fn() }))
vi.mock("@sentry/nextjs", () => ({ init }))

const collection = {
  userInfo: false,
  cookies: false,
  httpHeaders: false,
  httpBodies: [],
  urlQueryParams: false,
  genAI: { inputs: false, outputs: false },
  graphQL: { document: false, variables: false },
  databaseQueryData: false,
  queues: false,
  stackFrameVariables: false,
  frameContextLines: 0,
}

describe("Sentry initialization privacy", () => {
  beforeEach(() => {
    vi.resetModules()
    init.mockReset()
    vi.stubEnv("NEXT_PUBLIC_SENTRY_DSN", "https://public@example.invalid/1")
    vi.stubEnv("VITEST", "")
  })

  afterEach(() => {
    vi.unstubAllEnvs()
    vi.unstubAllGlobals()
  })

  it("initializes the browser only after consent, with crash-only collection", async () => {
    const fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ enabled: true }) })
    vi.stubGlobal("fetch", fetch)
    await import("../../sentry.client.config")
    await vi.waitFor(() => expect(init).toHaveBeenCalledOnce())
    expect(fetch).toHaveBeenCalledWith("/api/v1/system/telemetry", {
      credentials: "omit", cache: "no-store",
    })
    expect(init.mock.calls[0][0]).toMatchObject({
      dataCollection: collection, tracesSampleRate: 0,
      replaysSessionSampleRate: 0, replaysOnErrorSampleRate: 0,
    })
    const integrations = init.mock.calls[0][0].integrations([
      { name: "BrowserSession" }, { name: "Modules" }, { name: "ContextLines" },
      { name: "BrowserApiErrors" }, { name: "HttpContext" },
    ])
    expect(integrations.map((integration: { name: string }) => integration.name)).toEqual([
      "BrowserApiErrors", "HttpContext",
    ])
  })

  it.each(["denied", "failed", "offline"])("keeps the browser disabled when consent is %s", async (state) => {
    const fetch = state === "offline"
      ? vi.fn().mockRejectedValue(new Error("offline"))
      : vi.fn().mockResolvedValue({ ok: state !== "failed", json: async () => ({ enabled: false }) })
    vi.stubGlobal("fetch", fetch)
    await import("../../sentry.client.config")
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledOnce())
    // Allow the consent promise and initialization continuation to settle.
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(init).not.toHaveBeenCalled()
  })

  it.each(["browser", "server", "edge"])("keeps %s disabled without a DSN", async (runtime) => {
    vi.stubEnv("NEXT_PUBLIC_SENTRY_DSN", "")
    const fetch = vi.fn()
    vi.stubGlobal("fetch", fetch)
    if (runtime === "browser") await import("../../sentry.client.config")
    if (runtime === "server") await import("../../sentry.server.config")
    if (runtime === "edge") await import("../../sentry.edge.config")
    expect(init).not.toHaveBeenCalled()
    expect(fetch).not.toHaveBeenCalled()
  })

  it.each(["server", "edge"])("uses the same crash-only policy in %s", async (runtime) => {
    if (runtime === "server") await import("../../sentry.server.config")
    else await import("../../sentry.edge.config")
    expect(init).toHaveBeenCalledOnce()
    expect(init.mock.calls[0][0]).toMatchObject({ dataCollection: collection, tracesSampleRate: 0 })
  })
})
