"use client"

import { create } from "zustand"

export interface ArtifactTab {
  id: string
  /** Agent the path resolves against. Tabs are scoped per-agent so a
   *  tab opened for agent A can't be loaded/saved through agent B's
   *  endpoints after a context swap. */
  agentId: string
  path: string
  title: string
  language?: string
  /** Optional pre-edit snapshot (for diff view). */
  baseline?: string
}

interface ArtifactState {
  open: boolean
  tabs: ArtifactTab[]
  activeId: string | null
  openFile: (tab: ArtifactTab) => void
  closeTab: (id: string) => void
  setActive: (id: string) => void
  setOpen: (v: boolean) => void
  closeAll: () => void
  /** Drop every tab whose agentId differs from the one passed in.
   *  Called when the active agent changes so stale tabs can't be
   *  silently re-targeted at the new agent. */
  pruneToAgent: (agentId: string) => void
}

export const useArtifactStore = create<ArtifactState>((set, get) => ({
  open: false,
  tabs: [],
  activeId: null,
  openFile: (tab) => {
    const { tabs } = get()
    // Composite key — same path on different agents = different tabs.
    const existing = tabs.find((t) => t.agentId === tab.agentId && t.path === tab.path)
    if (existing) {
      set({ open: true, activeId: existing.id })
      return
    }
    set({ open: true, tabs: [...tabs, tab], activeId: tab.id })
  },
  closeTab: (id) => {
    const { tabs, activeId } = get()
    const next = tabs.filter((t) => t.id !== id)
    const nextActive =
      activeId === id ? next[next.length - 1]?.id ?? null : activeId
    set({ tabs: next, activeId: nextActive, open: next.length > 0 })
  },
  setActive: (id) => set({ activeId: id, open: true }),
  setOpen: (open) => set({ open }),
  closeAll: () => set({ open: false, tabs: [], activeId: null }),
  pruneToAgent: (agentId) => {
    const { tabs, activeId } = get()
    const kept = tabs.filter((t) => t.agentId === agentId)
    if (kept.length === tabs.length) return
    const stillActive = activeId && kept.some((t) => t.id === activeId)
    set({
      tabs: kept,
      activeId: stillActive ? activeId : (kept[kept.length - 1]?.id ?? null),
      open: kept.length > 0 ? get().open : false,
    })
  },
}))
