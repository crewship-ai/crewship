import { describe, it, expect } from "vitest"
import type { KeeperLogEntry, KeeperStatus } from "@/app/(dashboard)/admin/types"
import { filterActivity, findings, isSection, judgeState, setupRows, streamCounts, streamOf, type Posture } from "../security-model"

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
