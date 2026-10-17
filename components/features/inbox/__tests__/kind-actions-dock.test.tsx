import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"

// Harbor 2 (#3385): every action row is a dock — ghost, then secondary, then
// the primary as the right-most button — so the decision is always in the
// same place. DOM order is the visual order, so the tab order agrees.

vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }))

import { failedOnCredentials, KindActions } from "../kind-actions"

afterEach(cleanup)

function item(over: Partial<InboxItem> & Pick<InboxItem, "kind">): InboxItem {
  return {
    id: "i", workspace_id: "ws", source_id: "src", title: "t", state: "unread", priority: "medium",
    blocking: false, created_at: new Date().toISOString(), updated_at: new Date().toISOString(), ...over,
  } as InboxItem
}

function labels(i: InboxItem, extra: Partial<React.ComponentProps<typeof KindActions>> = {}) {
  render(<KindActions item={i} onResolve={vi.fn()} onRefresh={vi.fn()} disabled={false} {...extra} />)
  return [...document.querySelectorAll("button, a")].map((b) => b.textContent?.trim())
}

describe("the decision dock", () => {
  it("failed_run renders Retry as primary in the dock, Replace token secondary", () => {
    const run = item({ kind: "failed_run", body_md: "Step 2 returned 401 Unauthorized", payload: { pipeline_slug: "triage", pipeline_run_id: "r1" } })
    expect(labels(run)).toEqual(["Cancel", "Replace token", "Retry"])
    expect(screen.getByRole("link", { name: "Replace token" })).toHaveAttribute("href", "/credentials")
  })

  it("offers Replace token only when the failure was an authentication one", () => {
    expect(failedOnCredentials(item({ kind: "failed_run", body_md: "timeout after 30s" }))).toBe(false)
    expect(failedOnCredentials(item({ kind: "failed_run", payload: { error: "linear → 401 Unauthorized" } }))).toBe(true)
    expect(labels(item({ kind: "failed_run", body_md: "timeout", payload: { pipeline_slug: "triage", pipeline_run_id: "r1" } }))).toEqual(["Cancel", "Retry"])
  })

  it("puts Approve last on a waitpoint and Deny before it", () => {
    expect(labels(item({ kind: "waitpoint" }))).toEqual(["Deny", "Approve"])
  })

  it("orders a hire as Open crew · Deny · Approve hire", () => {
    expect(labels(item({ kind: "waitpoint", payload: { kind: "hire" } }), { crewHref: "/crews?crew=ops", onDenyHire: vi.fn() }))
      .toEqual(["Open crew", "Deny", "Approve hire"])
  })

  it("orders a paused schedule as Open routines · Re-enable schedule", () => {
    expect(labels(item({ kind: "schedule_circuit_breaker_tripped", payload: { schedule_id: "s1" } }))).toEqual(["Open routines", "Re-enable schedule"])
  })
})
