import { readFileSync, readdirSync, writeFileSync } from "node:fs"
import { join } from "node:path"
import { describe, expect, it } from "vitest"
import { z } from "zod"
import { E2CardSchema, E2CommandSchemas, E2CardPageSchema, E2VersionConflictSchema, e2CardJSONSchema } from "../chat-e2-cards"

const directory = join(process.cwd(), "internal/api/testdata/chat-e2")
const load = (name: string) => JSON.parse(readFileSync(join(directory, name), "utf8"))
describe("E2 shared projection contract", () => {
  it("accepts every actual Go handler fixture, including terminal receipts", () => {
    const names = readdirSync(directory).filter((name) => /^(proposal|decision)-.*\.json$/.test(name))
    expect(names).toHaveLength(17)
    for (const name of names) expect(E2CardSchema.safeParse(load(name)), name).toMatchObject({ success: true })
    expect(E2CardSchema.safeParse(load("unavailable.json")).success).toBe(false)
  })
  it("keeps exported JSON schemas reproducible", () => {
    const schema = { cards: e2CardJSONSchema(), page: z.toJSONSchema(E2CardPageSchema, { reused: "ref" }), conflict: z.toJSONSchema(E2VersionConflictSchema, { reused: "ref" }), commands: Object.fromEntries(Object.entries(E2CommandSchemas).map(([key, value]) => [key, z.toJSONSchema(value, { reused: "ref" })])) }
    if (process.env.UPDATE_CHAT_E2_SCHEMAS === "1") writeFileSync(join(directory, "schema.json"), JSON.stringify(schema) + "\n")
    expect(schema).toEqual(load("schema.json"))
  })
  it("rejects arbitrary widgets, source confusion and active terminal controls", () => {
    const card = load("decision-escalation-pending.json")
    card.decision.fields[0].type = "ReactComponent"
    expect(E2CardSchema.safeParse(card).success).toBe(false)
    const approval = load("decision-run_approval-pending.json")
    approval.decision.source.type = "escalation"
    expect(E2CardSchema.safeParse(approval).success).toBe(false)
    const answered = load("decision-waitpoint-answered.json")
    answered.decision.status = "approved"
    expect(E2CardSchema.safeParse(answered).success).toBe(false)
    const accepted = load("proposal-accepted.json")
    accepted.proposal.allowed_actions.push("confirm")
    expect(E2CardSchema.safeParse(accepted).success).toBe(false)
  })
  it("retains Unicode limits and rejects source identity in command bodies", () => {
    const card = load("proposal-draft.json")
    card.proposal.version = Number.MAX_SAFE_INTEGER + 1
    expect(E2CardSchema.safeParse(card).success).toBe(false)
    card.proposal.version = 1
    card.proposal.items[0].title = "🙂".repeat(240)
    expect(E2CardSchema.safeParse(card).success).toBe(true)
    card.proposal.items[0].title += "🙂"
    expect(E2CardSchema.safeParse(card).success).toBe(false)
    expect(E2CommandSchemas.proposal_confirm.safeParse({ proposal_id: "p", reviewed_version: 1 }).success).toBe(true)
    expect(E2CommandSchemas.proposal_confirm.safeParse({ proposal_id: "p", reviewed_version: 1, actor_id: "admin" }).success).toBe(false)
    expect(E2CommandSchemas.decision_answer.safeParse({ source_id: "s", expected_version: "opaque", action: "answer", values: { note: { html: "unsafe" } } }).success).toBe(false)
  })
})
