import { describe, expect, it } from "vitest"

import { RESTRICTED_FE_ENDPOINTS, isRestrictedAllowedRequest } from "@/lib/restricted-endpoints"
import { goRestrictedAllowlist, serverAllowsRestricted } from "./restricted-route-oracle"

describe("RESTRICTED_FE_ENDPOINTS", () => {
  it("is exactly the allowlist in internal/api/restricted_access.go", () => {
    expect([...RESTRICTED_FE_ENDPOINTS].sort()).toEqual(goRestrictedAllowlist())
  })

  it.each([
    ["GET", "/api/v1/workspaces"],
    ["GET", "/api/v1/workspaces/ws-1/restricted-routines"],
    ["GET", "/api/v1/workspaces/ws-1/restricted-routine-runs/run-9?x=1"],
    ["GET", "/api/v1/chats/c-1/messages?workspace_id=ws-1"],
    ["POST", "/api/v1/chats/c-1/restricted-run?workspace_id=ws-1"],
    ["GET", "http://localhost:8082/api/v1/agents?workspace_id=ws-1"],
    ["GET", "/api/auth/session"],
    ["POST", "/api/auth/token/refresh"],
  ])("allows %s %s", (method, url) => {
    expect(serverAllowsRestricted(method, url)).toBe(true)
  })

  it.each([
    ["GET", "/api/v1/ws-token"],
    ["GET", "/api/v1/journal/lookup?workspace_id=ws-1"],
    ["GET", "/api/v1/inbox/count?workspace_id=ws-1"],
    ["GET", "/api/v1/system/runtime"],
    ["GET", "/api/v1/system/version"],
    ["GET", "/api/v1/system/license"],
    ["GET", "/api/v1/workspaces/ws-1/pipelines/runs?status=all"],
    ["GET", "/api/v1/crewshipd?workspace_id=ws-1"],
    ["GET", "/api/v1/crews?workspace_id=ws-1"],
    ["POST", "/api/v1/workspaces"],
    ["DELETE", "/api/v1/chats/c-1/messages"],
  ])("refuses %s %s", (method, url) => {
    expect(serverAllowsRestricted(method, url)).toBe(false)
  })

  it("follows Go's most-specific-pattern rule: a literal route shadows an allowed wildcard", () => {
    // `GET /api/v1/agents/{agentId}` is allowed, but the server registers
    // `GET /api/v1/agents/crews-status`, which wins and is denied.
    expect(isRestrictedAllowedRequest("GET", "/api/v1/agents/crews-status")).toBe(true)
    expect(serverAllowsRestricted("GET", "/api/v1/agents/crews-status")).toBe(false)
    expect(serverAllowsRestricted("GET", "/api/v1/agents/agent-7")).toBe(true)
  })
})
