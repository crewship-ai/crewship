import { describe, it, expect } from "vitest"
import { deriveCrewNeeds, withDevcontainerFeature, type NeedAgent } from "@/components/features/crews/crew-needs"

const agent = (id: string, status = "IDLE", extra: Partial<NeedAgent> = {}): NeedAgent => ({
  id, name: id[0].toUpperCase() + id.slice(1), slug: id, status, role_title: "Engineer", ...extra,
})
const gap = (tool: string, credential_name: string) => ({
  credential_id: `c-${credential_name}`, credential_name, tool, feature: `ghcr.io/devcontainers/features/${tool}:1`, feature_id: tool,
})
const integration = (name: string, auth_status: string, n = 3) => ({
  id: `i-${name}`, name, display_name: name[0].toUpperCase() + name.slice(1), transport: "streamable-http", auth_status, agent_binding_count: n,
})

describe("deriveCrewNeeds", () => {
  it("is empty for a healthy crew", () => {
    expect(deriveCrewNeeds({ crewSlug: "eng", agents: [agent("alex")], gaps: [], integrations: [integration("github", "connected")] })).toEqual([])
  })

  it("gives every row a verb, danger before warn", () => {
    const rows = deriveCrewNeeds({
      crewSlug: "eng",
      agents: [agent("alex", "PENDING_REVIEW"), agent("sam", "ERROR")],
      provisioning: "needs_provision",
      gaps: [gap("gh", "github-acme")],
      integrations: [integration("linear", "missing"), integration("github", "expired", 1)],
    })
    expect(rows.map((r) => r.action.label)).toEqual(["Inspect", "Reconnect", "Build now", "Install gh", "Connect", "Review"])
    expect(rows.map((r) => r.tone)).toEqual(["danger", "danger", "warn", "warn", "warn", "warn"])
    expect(rows[0].action).toEqual({ kind: "link", label: "Inspect", href: "/crews?agent=sam" })
    expect(rows[3].action).toEqual({ kind: "install", label: "Install gh", feature: "ghcr.io/devcontainers/features/gh:1", featureId: "gh", tool: "gh" })
    expect(rows[4].action).toEqual({ kind: "link", label: "Connect", href: "/integrations?tab=tools&section=crew-tools&server=i-linear" })
    expect(rows[5].action).toEqual({ kind: "link", label: "Review", href: "/inbox-v2?agent=alex" })
  })

  it("folds credentials that wait on the same tool into one row", () => {
    const rows = deriveCrewNeeds({ crewSlug: "eng", agents: [], gaps: [gap("gh", "github-acme"), gap("gh", "github-globex"), gap("aws", "aws-sandbox")], integrations: [] })
    expect(rows.map((r) => r.title)).toEqual(["gh is missing from the image", "aws is missing from the image"])
    expect(rows[0].detail).toMatch(/github-acme, github-globex are bound/)
  })

  it("names the failed build's error and offers a retry", () => {
    const [row] = deriveCrewNeeds({ crewSlug: "eng", agents: [], provisioning: "failed", provisioningError: "exit 127: gh not found", gaps: [], integrations: [] })
    expect(row.tone).toBe("danger")
    expect(row.detail).toBe("exit 127: gh not found")
    expect(row.action).toEqual({ kind: "build", label: "Retry build" })
  })

  it("ignores expired hires — a ghost in error is not a decision", () => {
    expect(deriveCrewNeeds({ crewSlug: "eng", agents: [agent("old", "ERROR", { expired_at: "2026-01-01T00:00:00Z" })], gaps: [], integrations: [] })).toEqual([])
  })
})

describe("withDevcontainerFeature", () => {
  it("adds the feature and keeps everything else", () => {
    const out = JSON.parse(withDevcontainerFeature('{"image":"debian:trixie-slim","features":{"ghcr.io/x/node:1":{"version":"22"}}}', "ghcr.io/devcontainers/features/github-cli:1"))
    expect(out.image).toBe("debian:trixie-slim")
    expect(out.features).toEqual({ "ghcr.io/x/node:1": { version: "22" }, "ghcr.io/devcontainers/features/github-cli:1": {} })
  })
  it("starts from nothing when there is no config, and does not duplicate", () => {
    const once = withDevcontainerFeature(null, "f:1")
    expect(JSON.parse(once)).toEqual({ features: { "f:1": {} } })
    expect(JSON.parse(withDevcontainerFeature(once, "f:1"))).toEqual({ features: { "f:1": {} } })
  })
  it("survives unparseable config", () => {
    expect(JSON.parse(withDevcontainerFeature("{not json", "f:1"))).toEqual({ features: { "f:1": {} } })
  })
})
