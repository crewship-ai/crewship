import { describe, expect, it } from "vitest"

import { decisionMetaFor } from "../inbox-derive"

function accessItem(over: Record<string, unknown> = {}) {
  return {
    id: "ibx_1",
    kind: "escalation",
    title: "Keeper escalation: Riley requested PROD_DB_ADMIN (risk 9)",
    body_md: "no corroboration in the conversation history",
    state: "unread",
    payload: { request_type: "access", credential_name: "PROD_DB_ADMIN", risk_score: 9 },
    ...over,
  } as never
}

// A credential escalation is resolved with roleManage — OWNER or ADMIN. The card
// said "MANAGER+ can decide this", which was wrong in the direction that
// matters: it told a MANAGER the decision was theirs to make when the server
// would refuse them, and it justified addressing the item to an audience wider
// than the people who can act on it.
//
// The server side of this pair is pinned by TestKeeperInboxTarget_* in
// internal/api. Both had to move together — the audience and the authority are
// the same fact stated twice.
describe("deriveMeta for a credential access request", () => {
  it("says the decision needs manage, matching the server", () => {
    expect(decisionMetaFor(accessItem())?.requires).toBe("manage")
  })

  // Skill and routine proposals are also `escalation` and are NOT credential
  // decisions: no credential is named, and a team lead approving a proposed
  // routine is the intended flow. Tightening those would be a different change,
  // made for a different reason.
  it("leaves a routine proposal at create", () => {
    const meta = decisionMetaFor(accessItem({ payload: { kind: "routine_proposal" } }))
    expect(meta?.requires).toBe("create")
  })
})
