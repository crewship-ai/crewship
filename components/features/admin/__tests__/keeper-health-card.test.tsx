import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, waitFor } from "@testing-library/react"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api-fetch")>()),
  apiFetch: (...a: unknown[]) => apiFetch(...a),
}))

import { KeeperHealthCard } from "../keeper-health-card"

// The Keeper had no metric on its own decisions until #1664, and #1624 — the
// judge denying everything — survived several milestones because nothing was
// watching. The readout has been on the API and in the CLI ever since and
// nowhere in the product, which is the same failure one level up: nobody runs
// `keeper health` on a hunch.
//
// Everything pinned here is a distinction that would be cheap to flatten and
// expensive to have flattened.

const base = {
  samples: 100, allow: 60, deny: 20, escalate: 15, judge_failures: 5,
  progressed_rate: 0.75, judge_failure_rate: 0.05, p95_latency_ms: 2800,
  min_samples: 20, alarm_progressed_rate: 0.7, alarm_judge_failure_rate: 0.1,
}

function serve(body: unknown, ok = true, status = 200) {
  apiFetch.mockResolvedValue({ ok, status, json: async () => body })
}

beforeEach(() => apiFetch.mockReset())
afterEach(() => cleanup())

describe("keeper health readout", () => {
  it("counts travel with the rate", async () => {
    serve(base)
    render(<KeeperHealthCard />)
    // A percentage alone cannot be judged: 0% over four samples is noise, over
    // four hundred it is an outage.
    await waitFor(() =>
      expect(screen.getByTestId("keeper-health").textContent).toMatch(/75% of 100/))
  })

  // ESCALATE is not a refusal. Pooling it with DENY would hide the exact failure
  // this exists to catch — a judge that escalates a lot is cautious, one that
  // denies everything is broken.
  it("counts escalate as progress, not as refusal", async () => {
    serve({ ...base, allow: 0, deny: 0, escalate: 100, progressed_rate: 1 })
    render(<KeeperHealthCard />)
    await waitFor(() =>
      expect(screen.getByTestId("keeper-health").textContent).toMatch(/100% of 100/))
  })

  // An empty window is a real answer. Rendering 0% over zero samples would say
  // the judge refused everything when it decided nothing.
  it("says the window is empty rather than reporting 0%", async () => {
    serve({ ...base, samples: 0, allow: 0, deny: 0, escalate: 0, progressed_rate: 0 })
    render(<KeeperHealthCard />)
    await waitFor(() => {
      const t = screen.getByTestId("keeper-health").textContent ?? ""
      expect(t).toMatch(/window is empty/i)
      expect(t).not.toMatch(/0% of/)
    })
  })

  // Below the server's minimum the alarm is withheld, and the card must say so
  // rather than letting the reader draw a conclusion the server refused to.
  it("marks a sample count too small to conclude from", async () => {
    serve({ ...base, samples: 5, min_samples: 20 })
    render(<KeeperHealthCard />)
    await waitFor(() =>
      expect(screen.getByTestId("keeper-health").textContent).toMatch(/noise, not a signal/i))
  })

  // A server without the endpoint must produce silence, not reassurance.
  // "Nothing rendered" and "all clear" must never look the same.
  it("renders nothing against a server that has no readout", async () => {
    serve({}, false, 404)
    const { container } = render(<KeeperHealthCard />)
    await waitFor(() => expect(container.textContent).toBe(""))
  })

  it("surfaces the server's alarm verbatim", async () => {
    serve({ ...base, progressed_rate: 0.1, alarm: { kind: "keeper.denying_everything", summary: "10% progressed over 100 decisions" } })
    render(<KeeperHealthCard />)
    await waitFor(() =>
      expect(screen.getByTestId("keeper-health").textContent).toMatch(/denying_everything/))
  })
})
