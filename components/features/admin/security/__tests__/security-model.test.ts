import { describe, it, expect } from "vitest"
import type { KeeperLogEntry, KeeperStatus } from "@/app/(dashboard)/admin/types"
import { accessLabel, filterActivity, findings, healthToReport, isSection, judgeState, secondApprover, setupRows, streamCounts, streamOf, watchdogFinding, CREDENTIAL_LEVELS, type Posture } from "../security-model"
import type { InstanceGovRow, InstanceHealthRow } from "../use-instance-keeper"

const base: Posture = {
  environment: "prod", encryption_key_configured: true, plaintext_secrets_allowed: false,
  private_endpoints_ceiling: false, signup_open: false, oauth_configured: true, email_configured: true,
  rate_limit_disabled: false, rate_limit_effectively_disabled: false, warnings: [],
}
const status = (over: Partial<KeeperStatus> = {}): KeeperStatus => ({
  enabled: true, ollama_url: "http://x:11434", model: "qwen2.5:7b", ollama_online: true, gatekeeper_configured: true,
  total_requests: 4, allow_count: 4, deny_count: 0, escalate_count: 0, ...over,
})
const entry = (request_type: string, decision: string | null = "ALLOW") => ({ id: request_type + decision, request_type, decision } as unknown as KeeperLogEntry)
const value = (p: Posture, label: string) => setupRows(p).find((r) => r.label === label)!

describe("server setup rows", () => {
  it("says an insecure value is insecure, in words", () => {
    const p = { ...base, encryption_key_configured: false, plaintext_secrets_allowed: true, signup_open: true }
    expect(value(p, "Encryption key")).toMatchObject({ value: "NOT configured", tone: "bad" })
    expect(value(p, "Plaintext secrets")).toMatchObject({ value: "ALLOWED (insecure)", tone: "bad" })
    expect(value(p, "Signup")).toMatchObject({ value: "OPEN", tone: "bad" })
  })
  it("keeps a rate-limit flag production ignores apart from a limiter really off", () => {
    expect(value({ ...base, rate_limit_disabled: true }, "Rate limiter").value).toMatch(/IGNORED in production/)
    expect(value({ ...base, rate_limit_disabled: true, rate_limit_effectively_disabled: true }, "Rate limiter")).toMatchObject({ value: "DISABLED", tone: "bad" })
  })
  it("labels an unset environment", () => {
    expect(value({ ...base, environment: "" }, "Environment").value).toBe("(unset)")
  })
})

describe("needs attention", () => {
  it("puts the Keeper's own state first, then posture warnings by severity", () => {
    const p = { ...base, warnings: [
      { key: "no_backup_recorded", severity: "medium", message: "No backup has ever been recorded. More words." },
      { key: "seed_account_default_password", severity: "high", message: "The seed account keeps its password." },
    ] }
    const f = findings(p, status({ enabled: false }), { no_backup_recorded: { label: "Create a backup", href: "/admin?tab=backups" } })
    expect(f.map((x) => x.key)).toEqual(["keeper_off", "seed_account_default_password", "no_backup_recorded"])
    expect(f[2]).toMatchObject({ title: "No backup recorded", action: { label: "Create a backup" } })
  })
  it("warns when Keeper is on but its judge does not answer", () => {
    expect(findings(base, status({ ollama_online: false }))[0].key).toBe("judge_down")
    expect(findings(base, status())).toEqual([])
  })
  it("names an unknown warning by the server's first sentence", () => {
    const f = findings({ ...base, warnings: [{ key: "new_thing", severity: "info", message: "Something new. With detail." }] }, null)
    expect(f[0].title).toBe("Something new.")
  })
})

describe("activity", () => {
  const entries = [entry("access"), entry("execute", "DENY"), entry("behavior"), entry("skill_review", null), entry("mystery")]
  it("sorts every row into one of five streams", () => {
    expect(streamOf(entries[1])).toBe("requests")
    expect(streamOf(entries[4])).toBeNull()
    expect(streamCounts(entries)).toMatchObject({ requests: 2, behavior: 1, skill_review: 1, memory_health: 0 })
  })
  it("filters by stream and by decision", () => {
    expect(filterActivity(entries, "requests", "DENY")).toHaveLength(1)
    expect(filterActivity(entries, "all", "all")).toHaveLength(5)
  })
})

describe("judge state and sections", () => {
  it("says what the judge is doing in one line", () => {
    expect(judgeState(status()).text).toBe("Judge answering · qwen2.5:7b")
    expect(judgeState(status({ ollama_online: false, ollama_url: "" })).text).toBe("No judge server set")
    expect(judgeState(status({ enabled: false })).tone).toBe("bad")
  })
  it("knows its sections", () => {
    expect(isSection("watchdog")).toBe(true)
    expect(isSection("posture")).toBe(false)
  })
})

describe("how a workspace reads", () => {
  it("names the second approver as enforced, not as the switch", () => {
    expect(secondApprover({ require_second_approver: true })).toMatchObject({ text: "Required", required: true })
    expect(secondApprover({ require_second_approver: false, effective_second_approver: { min_security_level: 4, source: "tier" } })).toMatchObject({ text: "From level 4", required: false })
    expect(secondApprover({ require_second_approver: false })).toMatchObject({ text: "No", fromLevel: null })
  })
  it("says how long access lasts in the word the page uses", () => {
    expect([0, 900, 3600, 28800, 86400, 172800].map(accessLabel)).toEqual(["Until revoked", "15 min", "1 h", "8 h", "1 d", "2 d"])
  })
  it("reads the four levels from the tier table: reasons of 10, 15, 25 and 35 characters", () => {
    expect(CREDENTIAL_LEVELS.map((l) => l.minReason)).toEqual([10, 15, 25, 35])
  })
  it("turns the workspaces with the watchdog off into one finding", () => {
    expect(watchdogFinding([{ enabled: true }])).toBeNull()
    expect(watchdogFinding([{ enabled: false }, { enabled: false }, { enabled: true }])).toMatchObject({ title: "2 workspaces have the watchdog off", action: { href: "/admin/security?view=workspaces&watch=off" } })
  })
})

describe("judge health keeps only what has something to report", () => {
  const h = (id: string, over: Partial<InstanceHealthRow> = {}): InstanceHealthRow => ({ workspace_id: id, workspace_name: id, workspace_slug: id, samples: 40, min_samples: 20, progressed_rate: 1, judge_failure_rate: 0, p95_latency_ms: 100, alarm_judge_failure_rate: 0.3, ...over })
  const r = (id: string, own = false) => ({ workspace_id: id, gov_model_provider: own ? "anthropic" : "" }) as InstanceGovRow
  it("keeps an alarm, and an own judge that fails past the alarm line, and nothing else", () => {
    const out = healthToReport(
      [h("quiet"), h("alarm", { alarm: { kind: "x", summary: "y" } }), h("own-bad", { judge_failure_rate: 0.5 }), h("inst-bad", { judge_failure_rate: 0.5 }), h("own-thin", { samples: 3, judge_failure_rate: 0.5 })],
      [r("quiet"), r("alarm"), r("own-bad", true), r("inst-bad"), r("own-thin", true)],
    )
    expect(out.map((x) => x.workspace_id)).toEqual(["alarm", "own-bad"])
  })
})
