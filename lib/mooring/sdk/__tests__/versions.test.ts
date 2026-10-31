/** Contract of the SDK versions module, asserted through the public SDK. */
import { describe, expect, it, vi } from "vitest"
import { VersionsError, artifactProjectVersions, createVersionHistory, type VersionsTransport } from ".."

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

  it("adapts the artifact project's existing query and restore mutation", async () => {
    const restore = vi.fn(async () => ({ version: 8, page_id: "p" }))
    const t = artifactProjectVersions({ read: async () => ({ latest_version: 7, versions: [{ version: 7, created_at: "2026-10-31T10:00:00Z" }, { version: 6, message: "draft" }] }), restore })
    const h = createVersionHistory(t)
    expect((await h.list()).map(v => [v.version, v.current])).toEqual([["7", true], ["6", false]])
    expect(await h.restore("6", { base: "7" })).toEqual({ version: "8", restoredFrom: "6" })
    expect(restore).toHaveBeenCalledWith({ version: 6, base_version: 7 })
  })
})
