import { describe, expect, it } from "vitest"
import { slidesFrameStatus, type SlidesFrameState } from "../slides-status"

const base: SlidesFrameState = { ready: true, canWrite: true, shown: 4, latest: 4, refreshFailed: false }

describe("slidesFrameStatus", () => {
  it.each<[string, Partial<SlidesFrameState>, string | null, string?]>([
    ["nothing before the server answered", { ready: false }, null],
    ["Saved with the version the server holds", {}, "saved", "Saved · v4"],
    ["Read-only for Read and Comment", { canWrite: false }, "readonly", undefined],
    ["an older version names itself", { shown: 2 }, "readonly", "Version 2"],
    ["a failed poll is stale, never saved", { refreshFailed: true }, "stale", "Not refreshed"],
  ])("%s", (_name, patch, key, label) => {
    const status = slidesFrameStatus({ ...base, ...patch })
    expect(status?.key ?? null).toBe(key)
    if (label !== undefined) expect(status?.label).toBe(label)
  })
})
