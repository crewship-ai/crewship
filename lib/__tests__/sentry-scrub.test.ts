// Pinning test for the frontend Sentry BeforeSend scrubber. Mirrors
// internal/crashreport/crashreport_test.go::TestScrubEvent_DropsLeakyContexts
// on the Go side — same shape, same intent. If a future @sentry/nextjs
// upgrade adds a new auto-collected field under one of the known leaky
// context keys, this test still passes because we drop them wholesale;
// if someone removes a delete() / clear in scrubEvent, this fails loudly.
//
// The test imports scrubEvent directly rather than triggering a real
// Sentry capture. We don't want to depend on the SDK's runtime — that
// would require an init() call (and a DSN, and global handlers patched).
// Pulling the function out lets us assert on the deterministic shape
// transformation in isolation.

import { describe, it, expect } from "vitest"
import type { ErrorEvent } from "@sentry/nextjs"
import { scrubEvent } from "@/sentry.client.config"

// newLeakyEvent builds an Event maximally populated with every field
// the scrubber strips. New fields added in future SDK versions can be
// appended here when we extend scrubEvent.
function newLeakyEvent(): ErrorEvent {
  return {
    type: undefined,
    contexts: {
      device: { arch: "arm64", memory: 32 } as never,
      runtime: { name: "browser", version: "126" } as never,
      culture: { locale: "en-US" } as never,
      os: { name: "macOS" } as never,
    },
    user: {
      id: "u-123",
      email: "test@example.com",
      username: "pavel",
      ip_address: "10.0.0.42",
    },
    breadcrumbs: [
      {
        message: "did a thing",
        data: { path: "/api/v1/secret", token: "abc" },
      },
    ],
    // @ts-expect-error -- modules is a legacy field, still attached by some integrations
    modules: { "react-dom": "19.2.6" },
  } as ErrorEvent
}

describe("scrubEvent (frontend Sentry BeforeSend)", () => {
  it("drops device/runtime/culture contexts but preserves os", () => {
    const event = scrubEvent(newLeakyEvent())
    // Contexts is non-null because we still have an "os" entry.
    expect(event.contexts).toBeDefined()
    expect(event.contexts?.device).toBeUndefined()
    expect(event.contexts?.runtime).toBeUndefined()
    expect(event.contexts?.culture).toBeUndefined()
    // os IS retained (just GOOS-equivalent — harmless and helpful for triage)
    expect(event.contexts?.os).toBeDefined()
  })

  it("clears the user field wholesale (id, email, username, ip)", () => {
    const event = scrubEvent(newLeakyEvent())
    expect(event.user).toBeUndefined()
  })

  it("clears breadcrumb.data but keeps breadcrumb.message", () => {
    const event = scrubEvent(newLeakyEvent())
    expect(event.breadcrumbs).toHaveLength(1)
    expect(event.breadcrumbs![0].data).toBeUndefined()
    expect(event.breadcrumbs![0].message).toBe("did a thing")
  })

  it("clears the modules field", () => {
    const event = scrubEvent(newLeakyEvent())
    // @ts-expect-error -- runtime check on legacy field
    expect(event.modules).toBeUndefined()
  })

  it("handles an event with no contexts/breadcrumbs/user without throwing", () => {
    const minimal = {} as ErrorEvent
    expect(() => scrubEvent(minimal)).not.toThrow()
    const out = scrubEvent(minimal)
    expect(out.user).toBeUndefined()
  })

  it("handles an event with null entries in breadcrumbs", () => {
    const evt = {
      breadcrumbs: [null as unknown as { message?: string }, { message: "ok", data: { x: 1 } }],
    } as unknown as ErrorEvent
    const out = scrubEvent(evt)
    expect(out.breadcrumbs![1].data).toBeUndefined()
  })
})
