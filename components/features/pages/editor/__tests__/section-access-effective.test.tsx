/**
 * Effective access — the read-only card that says who reaches a Page and how.
 *
 * What is pinned here is what a wrong sentence costs someone:
 *
 *   · A path the server sent that the card silently drops understates who
 *     gets in; a path the card invents overstates it. The sentences are
 *     built from the server's vocabulary and nothing else, in its order.
 *   · The word `withheld` is the server refusing to name a crew the reader
 *     cannot see. The row must say that plainly and must not print the
 *     marker as if it were a slug.
 *   · A 403 is an answer. The card stays, the refusal is the server's own
 *     sentence, and nothing about the section disappears.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, cleanup, waitFor } from "@testing-library/react"

import {
  EffectiveAccessCard,
  effectiveAccessSentence,
  toEffectiveAccessSubject,
} from "@/components/features/pages/editor/section-access-effective"

// ── Sentences ──────────────────────────────────────────────────────────────

describe("the sentence for one subject", () => {
  const sentence = (raw: Parameters<typeof toEffectiveAccessSubject>[0]) =>
    effectiveAccessSentence(toEffectiveAccessSubject(raw))

  it("reads a user's paths in the server's order, with one 'through' per run", () => {
    expect(
      sentence({
        subject_type: "user",
        subject_id: "u1",
        label: "petr@example.com",
        paths: ["crew:ops", "grant:page:read"],
      }),
    ).toBe("petr@example.com · reaches through crew ops and a read grant")

    expect(
      sentence({
        subject_type: "user",
        subject_id: "u2",
        label: "ada@example.com",
        paths: ["owner", "role", "panel_crew:lookout", "grant:page:read", "grant:page:write"],
      }),
    ).toBe(
      "ada@example.com · reaches as the owner, through the workspace role, a panel owned by crew lookout, a read grant and a write grant",
    )

    expect(
      sentence({ subject_type: "user", subject_id: "u3", label: "dave@example.com", paths: ["grant:page:read"] }),
    ).toBe("dave@example.com · reaches through a read grant")
  })

  it("reads a crew's and an agent's standing as what they hold", () => {
    expect(
      sentence({ subject_type: "crew", subject_id: "c1", label: "engine", paths: ["owner", "grant:page:read"] }),
    ).toBe("crew engine · owns this Page and holds a read grant")
    expect(
      sentence({ subject_type: "crew", subject_id: "c2", label: "lookout", paths: ["panel_crew:lookout"] }),
    ).toBe("crew lookout · owns a panel on it")
    expect(
      sentence({ subject_type: "agent", subject_id: "a1", label: "watcher", paths: ["grant:page:produce"] }),
    ).toBe("agent watcher · holds a produce grant")
  })

  it("says a withheld crew is withheld, and never prints the marker as a name", () => {
    const text = sentence({ subject_type: "crew", subject_id: "withheld", paths: ["panel_crew:withheld"] })
    expect(text).toBe("A crew you cannot see · owns a panel on this Page. Which crew it is, is withheld from you.")
    expect(text).not.toContain("crew withheld")
  })

  it("prints a path it does not recognise verbatim rather than dropping it", () => {
    expect(
      sentence({ subject_type: "user", subject_id: "u9", label: "x@example.com", paths: ["folder:ops"] }),
    ).toBe("x@example.com · reaches through folder:ops")
  })
})

// ── The card ───────────────────────────────────────────────────────────────

function jsonResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: { get: () => null },
    json: async () => body,
    text: async () => JSON.stringify(body),
  } as unknown as Response
}

function mount(status: number, body: unknown) {
  const calls: string[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      calls.push(String(input))
      return jsonResponse(status, body)
    }),
  )
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  render(
    <QueryClientProvider client={qc}>
      <EffectiveAccessCard workspaceId="ws-1" slug="fleet-201" />
    </QueryClientProvider>,
  )
  return calls
}

function cardText(): string {
  return document.querySelector("[data-slot='page-effective-access']")!.textContent ?? ""
}

beforeEach(() => cleanup())
afterEach(() => vi.unstubAllGlobals())

describe("the Effective access card", () => {
  it("lists each subject as a sentence and reads the page's access route", async () => {
    const calls = mount(200, {
      page: "fleet-201",
      subjects: [
        { subject_type: "user", subject_id: "u1", label: "petr@example.com", paths: ["crew:ops", "grant:page:read"] },
        { subject_type: "crew", subject_id: "withheld", paths: ["panel_crew:withheld"] },
      ],
    })
    await waitFor(() =>
      expect(document.querySelectorAll("[data-slot='page-effective-access-row']")).toHaveLength(2),
    )
    expect(calls[0]).toContain("/api/v1/pages/fleet-201/access?workspace_id=ws-1")
    const text = cardText()
    expect(text).toContain("Effective access")
    expect(text).toContain("petr@example.com · reaches through crew ops and a read grant")
    expect(text).toContain("A crew you cannot see · owns a panel on this Page")
    expect(text).toContain("2 subjects")
    // Read-only: nothing to press.
    expect(document.querySelector("[data-slot='page-effective-access'] button")).toBeNull()
    expect(document.querySelector("[data-slot='page-effective-access-row'][data-withheld='true']")).not.toBeNull()
  })

  it("keeps the card and shows the server's refusal on a 403", async () => {
    const refusal =
      "only the page owner or a workspace admin may read who reaches this page; a grant of any level does not include reading the page's access (§7.1 rule 3)"
    mount(403, { error: refusal })
    await waitFor(() => expect(cardText()).toContain(refusal))
    expect(cardText()).toContain("Effective access")
    expect(cardText()).toContain("not yours to read")
    expect(document.querySelector("[data-slot='page-settings-refusal']")).not.toBeNull()
    expect(document.querySelectorAll("[data-slot='page-effective-access-row']")).toHaveLength(0)
  })

  it("says when the listing is longer than what it shows", async () => {
    mount(200, {
      page: "fleet-201",
      subjects: [{ subject_type: "user", subject_id: "u1", label: "petr@example.com", paths: ["owner"] }],
      next_cursor: "djE6dXNlcnx1MQ",
    })
    await waitFor(() => expect(cardText()).toContain("crewship page access fleet-201"))
    expect(cardText()).toContain("1+ subjects")
  })
})
