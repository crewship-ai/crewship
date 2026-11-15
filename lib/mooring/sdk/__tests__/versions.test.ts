/** Contract of the SDK versions module, asserted through the public SDK. */
import { describe, expect, it, vi } from "vitest"
import { VersionsError, artifactProjectVersions, createVersionHistory, nativeCraftVersions, type VersionsTransport } from ".."

function transport(head = "3"): VersionsTransport & { restore: ReturnType<typeof vi.fn> } {
  return {
    list: vi.fn(async () => ({ head, versions: [{ version: "1" }, { version: "3", message: "latest" }, { version: "2", restoredFrom: "1" }, { version: "3" }] })),
    restore: vi.fn(async ({ base }: { target: string; base: string }) => ({ version: String(Number(base) + 1) })),
  }
}

describe("Mooring SDK versions", () => {
  it("lists newest first with exactly the server head current", async () => {
    const list = await createVersionHistory(transport()).list()
    expect(list.map(v => v.version)).toEqual(["3", "2", "1"])
    expect(list.filter(v => v.current).map(v => v.version)).toEqual(["3"])
    expect(list[0].message).toBe("latest")
  })

  it("refuses a listing whose head is missing or older than a listed version", async () => {
    await expect(createVersionHistory({ ...transport(), list: async () => ({ head: "4", versions: [{ version: "3" }] }) }).list()).rejects.toBeInstanceOf(VersionsError)
    await expect(createVersionHistory({ ...transport(), list: async () => ({ head: "2", versions: [{ version: "2" }, { version: "3" }] }) }).list()).rejects.toMatchObject({ reason: "invalid_versions" })
    await expect(createVersionHistory({ ...transport(), list: async () => ({ head: "1", versions: [{ version: "0" }] }) }).list()).rejects.toMatchObject({ reason: "invalid_versions" })
  })

  it("restores as a new version on top of the held base", async () => {
    const t = transport()
    const receipt = await createVersionHistory(t).restore("1", { base: "3" })
    expect(t.restore).toHaveBeenCalledWith({ target: "1", base: "3" }, undefined)
    expect(receipt).toEqual({ version: "4", restoredFrom: "1" })
    await createVersionHistory(t).restore("2", { base: "4", generation: "7" })
    expect(t.restore).toHaveBeenLastCalledWith({ target: "2", base: "4", generation: "7" }, undefined)
    await expect(createVersionHistory(t).restore("2", { base: "4", generation: "0" })).rejects.toMatchObject({ reason: "unknown_version" })
  })

  it("never accepts a rewind, a skipped version or an unknown target", async () => {
    const h = createVersionHistory(transport())
    await expect(h.restore("4", { base: "3" })).rejects.toMatchObject({ reason: "unknown_version" })
    await expect(h.restore("3", { base: "3" })).rejects.toMatchObject({ reason: "already_current" })
    for (const version of ["1", "3", "5", ""]) {
      const t = transport(); t.restore.mockResolvedValueOnce({ version })
      await expect(createVersionHistory(t).restore("1", { base: "3" })).rejects.toMatchObject({ reason: "restore_unconfirmed" })
    }
  })

  it("refuses an imprecise artifact version before sending the restore", async () => {
    const restore = vi.fn()
    const h = createVersionHistory(artifactProjectVersions({ read: async () => ({ latest_version: 2 ** 53, versions: [{ version: 2 ** 53 }] }), restore }))
    await expect(h.list()).rejects.toMatchObject({ reason: "invalid_versions" })
    await expect(h.restore("9007199254740993", { base: "9007199254740994" })).rejects.toMatchObject({ reason: "unknown_version" })
    await expect(h.restore("1", { base: "9007199254740991" })).rejects.toMatchObject({ reason: "unknown_version" })
    expect(restore).not.toHaveBeenCalled()
  })

  it("adapts the artifact project's existing query and restore mutation", async () => {
    const restore = vi.fn(async () => ({ version: 8, page_id: "p" }))
    const t = artifactProjectVersions({ read: async () => ({ latest_version: 7, versions: [{ version: 7, created_at: "2026-10-31T10:00:00Z" }, { version: 6, message: "draft" }] }), restore })
    const h = createVersionHistory(t)
    expect((await h.list()).map(v => [v.version, v.current])).toEqual([["7", true], ["6", false]])
    expect(await h.restore("6", { base: "7" })).toEqual({ version: "8", restoredFrom: "6" })
    expect(restore).toHaveBeenCalledWith({ version: 6, base_version: 7 })
  })
})

describe("Mooring SDK native Craft versions", () => {
  const binding = { workspaceId: "w", pageId: "p", applicationId: "wordcraft", enginePin: "e".repeat(64), sessionId: "s" }
  const author = { actorId: "u", kind: "human" as const, displayName: "Alice" }
  const listing = { currentVersion: "3", truncated: true, versions: [{ version: "3", createdAt: "2026-11-01T10:00:00Z", author, current: true }, { version: "2", createdAt: "2026-11-01T09:00:00Z", author, restoredFrom: "1", current: false }] }

  it("reuses the host client's validated listing and fenced restore", async () => {
    const client = { versions: vi.fn(async () => listing), restore: vi.fn(async () => ({ version: "4", durability: "node" as const, issuedBy: "crewship" as const, phase: "committed" as const })) }
    const h = createVersionHistory(nativeCraftVersions(client, binding))
    expect((await h.list()).map(v => [v.version, v.current, v.restoredFrom])).toEqual([["3", true, undefined], ["2", false, "1"]])
    expect(h.truncated).toBe(true)
    expect(await h.restore("2", { base: "3", generation: "7" })).toEqual({ version: "4", restoredFrom: "2" })
    expect(client.restore).toHaveBeenCalledWith({ ...binding, generation: "7", baseVersion: "3", targetVersion: "2" }, expect.any(AbortSignal))
  })

  it("is unavailable while the history flag is off and never restores without a lease generation", async () => {
    const client = { versions: vi.fn(async () => null), restore: vi.fn() }
    await expect(createVersionHistory(nativeCraftVersions(client, binding)).list()).rejects.toMatchObject({ reason: "versions_unavailable" })
    await expect(createVersionHistory(nativeCraftVersions(client, binding)).restore("1", { base: "3" })).rejects.toMatchObject({ reason: "unknown_version" })
    expect(client.restore).not.toHaveBeenCalled()
  })
})
