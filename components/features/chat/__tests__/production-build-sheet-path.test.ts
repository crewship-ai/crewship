import { describe, expect, it, vi, afterEach } from "vitest"
import { buildProfile } from "../../../../e2e/build-profile"
import { chatArtifactProjects } from "@/lib/feature-flags"
import { liftArtifactToolNodes } from "../artifact-context/projects/artifact-tool-card"
import { groupTurnParts } from "../turn-grouping"

afterEach(() => { vi.unstubAllEnvs() })

// What a client gets is what .env.production says, so the Sheet card is checked against that file.
describe("the Sheet card under the checked-in .env.production", () => {
  it("shows without the artifact projects provider", () => {
    const { env } = buildProfile()
    for (const [key, value] of Object.entries(env)) if (key.startsWith("NEXT_PUBLIC_")) vi.stubEnv(key, value ?? "")
    const tool = "mcp__crewship__craft_sheet_apply"
    const nodes = groupTurnParts([{ id: "a", type: "tool_call", content: tool, metadata: { tool_id: "a", tool_name: tool, input: {} }, timestamp: new Date(0) }])
    const kinds = { projects: chatArtifactProjects(), sheets: true }
    expect(liftArtifactToolNodes(nodes, kinds).map(node => node.kind)).toEqual(["artifact"])
  })
})
