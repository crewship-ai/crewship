import { describe, it, expect, afterEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"
import { EvidenceFacts } from "../evidence-facts"

// The card gave the person deciding one thing: the judge's reason — a case FOR
// the verdict already reached. These facts are what the operator asked for
// instead: the consequences, and then leave me to decide.
//
// The properties worth pinning are all about what this must NOT do.

function item(over: Partial<InboxItem>): InboxItem {
  return {
    id: "i", workspace_id: "ws", source_id: "kr", title: "t", kind: "escalation",
    state: "unread", priority: "high", blocking: true,
    created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
    ...over,
  } as InboxItem
}

afterEach(() => cleanup())

describe("evidence facts on a credential escalation", () => {
  it("renders nothing when the server sent no evidence", () => {
    render(<EvidenceFacts item={item({})} />)
    expect(screen.queryByTestId("keeper-evidence")).not.toBeInTheDocument()
  })

  // A missing field means the query failed and nobody knows. Rendering "no
  // backup" for it would manufacture an argument against approving out of a
  // database outage — the mirror of the failure the evidence package prevents.
  it("says nothing about a fact the server could not compute", () => {
    render(<EvidenceFacts item={item({ evidence: { narrower_credential: { exists: false } } })} />)
    const block = screen.getByTestId("keeper-evidence")
    expect(block.textContent).not.toMatch(/backup/i)
    expect(block.textContent).toMatch(/none for this provider/i)
  })

  // "We looked and there is none" is the answer that matters most on a
  // destructive request, and it must read as a stated fact.
  it("states a missing backup rather than omitting it", () => {
    render(<EvidenceFacts item={item({ evidence: { last_backup: { exists: false, age_hours: 0, scope: "workspace" } } })} />)
    expect(screen.getByTestId("keeper-evidence").textContent).toMatch(/none recorded/i)
  })

  // backup_catalog is scoped to a workspace, never a table. "6h ago" read as
  // "this table can be restored" is the reassuring invention to avoid, so the
  // qualifier is part of the line rather than something the reader must know.
  it("qualifies what the backup actually covers", () => {
    render(<EvidenceFacts item={item({ evidence: { last_backup: { exists: true, age_hours: 6, scope: "workspace" } } })} />)
    expect(screen.getByTestId("keeper-evidence").textContent).toMatch(/6h ago.*workspace-wide, not this table/i)
  })

  it("names the narrower credential so it can be checked", () => {
    render(<EvidenceFacts item={item({
      evidence: { narrower_credential: { exists: true, name: "PROD_DB_READONLY", security_level: 2 } },
    })} />)
    expect(screen.getByTestId("keeper-evidence").textContent).toMatch(/PROD_DB_READONLY \(L2\)/)
  })

  // The load-bearing one. Facts describe consequences; the moment a line tells
  // the reader what to do, the judgement has moved back into the model and the
  // person is agreeing rather than deciding.
  it("never tells the operator what to do", () => {
    render(<EvidenceFacts item={item({
      evidence: {
        last_backup: { exists: false, age_hours: 0, scope: "workspace" },
        narrower_credential: { exists: true, name: "PROD_DB_READONLY", security_level: 2 },
      },
    })} />)
    const text = screen.getByTestId("keeper-evidence").textContent ?? ""
    for (const word of ["should", "recommend", "safer", "better", "advise", "suggest", "deny", "approve"]) {
      expect(text.toLowerCase()).not.toContain(word)
    }
  })
})
