import { describe, expect, it } from "vitest"
import { designFrameStatus, type DesignFrameState } from "../design-status"

const base: DesignFrameState = { ready: true, connection: "connected", canEdit: true, editable: true, unsynced: false, checkpointPending: false, historyFull: false }

describe("designFrameStatus", () => {
  it.each<[string, Partial<DesignFrameState>, string | null, string?]>([
    ["nothing until the canvas loaded", { ready: false }, null],
    ["Live for an editor on a confirmed canvas", {}, "live"],
    ["Saving… while work is unconfirmed", { unsynced: true }, "saving"],
    ["Saving… while a checkpoint is on its way", { checkpointPending: true }, "saving"],
    ["Saved only with the version the server returned", { checkpointVersion: 7 }, "saved", "Saved · v7"],
    ["Saving… wins over an older receipt", { checkpointVersion: 7, unsynced: true }, "saving"],
    ["Read-only for Read and Comment", { canEdit: false, editable: false }, "readonly", undefined],
    ["Read-only while a historical version is open", { version: 3, editable: false }, "readonly", "Version 3"],
    ["unsent work after the edit right went is read-only, never saved", { canEdit: false, editable: false, unsynced: true }, "readonly"],
    ["offline keeps the work in the browser", { connection: "offline", editable: false }, "offline"],
    ["connecting is in flight, not saved", { connection: "connecting", editable: false }, "refreshing", "Connecting…"],
    ["a refused change is an error", { connection: "rejected" }, "error", "Changes refused"],
    ["changed access is an error", { connection: "denied" }, "error", "Access changed"],
    ["a full history is not confirmed", { historyFull: true, checkpointVersion: 4 }, "unconfirmed", "History full"],
    ["full storage is not confirmed", { connection: "quota" }, "unconfirmed", "Storage full"],
  ])("%s", (_name, patch, key, label) => {
    const status = designFrameStatus({ ...base, ...patch })
    expect(status?.key ?? null).toBe(key)
    if (label !== undefined) expect(status?.label).toBe(label)
  })
})
