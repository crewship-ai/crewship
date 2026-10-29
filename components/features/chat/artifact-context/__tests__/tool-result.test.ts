import { describe, expect, it } from "vitest"
import { artifactToolActivity, isArtifactToolName } from "../projects/tool-result"

const createInput = { slug: "deck", name: "Launch deck", entrypoint: "index.html", files: [{ path: "index.html", content: "<h1>SECRET BODY</h1>" }], message: "first" }

describe("isArtifactToolName", () => {
  it.each([
    ["artifact_create", true], ["artifact_publish", true], ["artifact_restore", true],
    ["mcp__crewship__artifact_create", true], ["mcp__my-server__artifact_publish", true],
    ["artifact_delete", false], ["mcp__crewship__page_create", false], ["xartifact_create", false], ["mcp____artifact_create", false],
    ["", false], [null, false], [undefined, false],
  ] as const)("%s → %s", (name, expected) => {
    expect(isArtifactToolName(name)).toBe(expected)
  })
})

describe("artifactToolActivity", () => {
  it("returns null for other tools", () => {
    expect(artifactToolActivity({ toolName: "web_search", input: {} })).toBeNull()
  })

  it("is running without a result and never carries file contents", () => {
    const activity = artifactToolActivity({ toolName: "artifact_create", input: createInput })
    expect(activity).toEqual({ tool: "create", state: "running", slug: "deck", name: "Launch deck" })
    expect(JSON.stringify(activity)).not.toContain("SECRET")
  })

  it("parses a JSON string input and a plain JSON result", () => {
    const activity = artifactToolActivity(
      { toolName: "mcp__crewship__artifact_create", input: JSON.stringify(createInput) },
      { content: JSON.stringify({ page_id: "p1", slug: "deck", version: 1, url: "/p/deck" }) },
    )
    expect(activity).toEqual({ tool: "create", state: "done", slug: "deck", name: "Launch deck", pageId: "p1", version: 1 })
  })

  it("unwraps an MCP content array", () => {
    const content = JSON.stringify([{ type: "text", text: JSON.stringify({ page_id: "p1", slug: "deck", version: 4 }) }])
    expect(artifactToolActivity({ toolName: "artifact_publish", input: { slug: "deck", base_version: 3, files: [] } }, { content }))
      .toEqual({ tool: "publish", state: "done", slug: "deck", baseVersion: 3, pageId: "p1", version: 4 })
  })

  it("unwraps an MCP content object", () => {
    const content = JSON.stringify({ content: [{ type: "text", text: JSON.stringify({ version: 5, actor: "agent" }) }] })
    expect(artifactToolActivity({ toolName: "mcp__s__artifact_restore", input: { slug: "deck", base_version: 4, version: 2 } }, { content }))
      .toEqual({ tool: "restore", state: "done", slug: "deck", baseVersion: 4, restoreTarget: 2, version: 5 })
  })

  it.each([
    ["isError", { content: "version conflict: base 3, latest 4", isError: true }, "version conflict: base 3, latest 4"],
    ["error field", { content: JSON.stringify({ error: "quota exceeded", code: "artifact_history_quota" }) }, "quota exceeded"],
    ["non-JSON", { content: "something broke" }, "something broke"],
    ["wrapped error", { content: JSON.stringify({ content: [{ type: "text", text: "boom" }], isError: true }) }, "boom"],
  ])("a %s result is failed", (_label, result, error) => {
    const activity = artifactToolActivity({ toolName: "artifact_publish", input: { slug: "deck", base_version: 3 } }, result)
    expect(activity).toMatchObject({ tool: "publish", state: "failed", slug: "deck", error })
  })

  it("shortens long errors", () => {
    const activity = artifactToolActivity({ toolName: "artifact_publish", input: {} }, { content: "x".repeat(1000), isError: true })!
    expect(activity.error!.length).toBeLessThanOrEqual(200)
  })

  it("ignores malformed inputs instead of throwing", () => {
    expect(artifactToolActivity({ toolName: "artifact_restore", input: "{nope" })).toEqual({ tool: "restore", state: "running" })
    expect(artifactToolActivity({ toolName: "artifact_restore", input: { slug: 5, version: "2" } })).toEqual({ tool: "restore", state: "running" })
  })
})
