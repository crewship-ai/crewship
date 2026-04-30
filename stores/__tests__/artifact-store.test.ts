import { describe, it, expect, beforeEach } from "vitest"
import { useArtifactStore, type ArtifactTab } from "@/stores/artifact-store"

function makeTab(id: string, agentId = "agent_1", path = "/file/" + id): ArtifactTab {
  return { id, agentId, path, title: "tab " + id }
}

beforeEach(() => {
  // Zustand stores leak state across tests unless explicitly reset.
  useArtifactStore.setState({ open: false, tabs: [], activeId: null })
})

describe("useArtifactStore", () => {
  it("openFile inserts a new tab and activates it", () => {
    useArtifactStore.getState().openFile(makeTab("t1"))
    const s = useArtifactStore.getState()
    expect(s.open).toBe(true)
    expect(s.tabs).toHaveLength(1)
    expect(s.activeId).toBe("t1")
  })

  it("openFile dedupes by (agentId, path) — re-opens existing tab instead of duplicating", () => {
    const tab = makeTab("t1", "agent_1", "/foo")
    useArtifactStore.getState().openFile(tab)
    // Same agent + path but different id → should reuse t1.
    useArtifactStore.getState().openFile({ ...tab, id: "t1_dup" })
    const s = useArtifactStore.getState()
    expect(s.tabs).toHaveLength(1)
    expect(s.activeId).toBe("t1")
  })

  it("openFile keeps tabs separate when path is same but agent differs", () => {
    useArtifactStore.getState().openFile(makeTab("t1", "agent_a", "/foo"))
    useArtifactStore.getState().openFile(makeTab("t2", "agent_b", "/foo"))
    const s = useArtifactStore.getState()
    expect(s.tabs).toHaveLength(2)
    expect(s.activeId).toBe("t2")
  })

  it("closeTab removes by id and activates the new last tab", () => {
    const store = useArtifactStore.getState()
    store.openFile(makeTab("t1"))
    store.openFile(makeTab("t2"))
    store.openFile(makeTab("t3")) // active = t3
    useArtifactStore.getState().closeTab("t3")
    expect(useArtifactStore.getState().activeId).toBe("t2")
    expect(useArtifactStore.getState().tabs.map((t) => t.id)).toEqual(["t1", "t2"])
  })

  it("closeTab on inactive tab keeps activeId stable", () => {
    const store = useArtifactStore.getState()
    store.openFile(makeTab("t1"))
    store.openFile(makeTab("t2"))
    store.openFile(makeTab("t3")) // active = t3
    useArtifactStore.getState().closeTab("t1")
    expect(useArtifactStore.getState().activeId).toBe("t3")
  })

  it("closeTab on the last tab closes the panel", () => {
    useArtifactStore.getState().openFile(makeTab("t1"))
    useArtifactStore.getState().closeTab("t1")
    const s = useArtifactStore.getState()
    expect(s.open).toBe(false)
    expect(s.activeId).toBeNull()
    expect(s.tabs).toEqual([])
  })

  it("closeAll resets every state field", () => {
    useArtifactStore.getState().openFile(makeTab("t1"))
    useArtifactStore.getState().closeAll()
    const s = useArtifactStore.getState()
    expect(s.open).toBe(false)
    expect(s.tabs).toEqual([])
    expect(s.activeId).toBeNull()
  })

  it("pruneToAgent drops tabs from other agents (security boundary)", () => {
    const store = useArtifactStore.getState()
    store.openFile(makeTab("t1", "agent_a"))
    store.openFile(makeTab("t2", "agent_b"))
    store.openFile(makeTab("t3", "agent_a"))
    useArtifactStore.getState().pruneToAgent("agent_a")
    const s = useArtifactStore.getState()
    expect(s.tabs.map((t) => t.id)).toEqual(["t1", "t3"])
  })

  it("pruneToAgent reactivates a kept tab when active was pruned", () => {
    const store = useArtifactStore.getState()
    store.openFile(makeTab("t1", "agent_a"))
    store.openFile(makeTab("t2", "agent_b")) // active
    useArtifactStore.getState().pruneToAgent("agent_a")
    const s = useArtifactStore.getState()
    expect(s.activeId).toBe("t1")
  })

  it("pruneToAgent on a single-agent store is a no-op", () => {
    useArtifactStore.getState().openFile(makeTab("t1", "agent_a"))
    useArtifactStore.getState().openFile(makeTab("t2", "agent_a"))
    const before = useArtifactStore.getState().tabs.length
    useArtifactStore.getState().pruneToAgent("agent_a")
    expect(useArtifactStore.getState().tabs).toHaveLength(before)
  })

  it("setActive opens the panel and switches active id", () => {
    useArtifactStore.getState().openFile(makeTab("t1"))
    useArtifactStore.getState().openFile(makeTab("t2"))
    useArtifactStore.getState().setOpen(false)
    useArtifactStore.getState().setActive("t1")
    const s = useArtifactStore.getState()
    expect(s.activeId).toBe("t1")
    expect(s.open).toBe(true)
  })
})
