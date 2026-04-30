import { describe, it, expect, beforeEach } from "vitest"
import { useBackupStore } from "@/stores/backup-store"

beforeEach(() => {
  useBackupStore.setState({ selectedPath: null, dialog: null })
})

describe("useBackupStore", () => {
  it("openCreate sets dialog=create and clears selectedPath", () => {
    // Pre-pollute state so we can verify the clear.
    useBackupStore.setState({ selectedPath: "/tmp/x", dialog: "inspect" })
    useBackupStore.getState().openCreate()
    const s = useBackupStore.getState()
    expect(s.dialog).toBe("create")
    expect(s.selectedPath).toBeNull()
  })

  it("openRestore stores path and switches dialog", () => {
    useBackupStore.getState().openRestore("/var/lib/backup-2026-04-30.tgz")
    const s = useBackupStore.getState()
    expect(s.dialog).toBe("restore")
    expect(s.selectedPath).toBe("/var/lib/backup-2026-04-30.tgz")
  })

  it("openInspect stores path and switches dialog", () => {
    useBackupStore.getState().openInspect("/var/lib/backup-2026-04-29.tgz")
    expect(useBackupStore.getState().dialog).toBe("inspect")
    expect(useBackupStore.getState().selectedPath).toBe("/var/lib/backup-2026-04-29.tgz")
  })

  it("close resets both fields", () => {
    useBackupStore.getState().openRestore("/foo")
    useBackupStore.getState().close()
    const s = useBackupStore.getState()
    expect(s.dialog).toBeNull()
    expect(s.selectedPath).toBeNull()
  })
})
