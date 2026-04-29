"use client"

import { create } from "zustand"

export interface ArtifactTab {
  id: string
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
}

export const useArtifactStore = create<ArtifactState>((set, get) => ({
  open: false,
  tabs: [],
  activeId: null,
  openFile: (tab) => {
    const { tabs } = get()
    const existing = tabs.find((t) => t.path === tab.path)
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
}))
