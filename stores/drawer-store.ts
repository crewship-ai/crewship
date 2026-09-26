"use client"

import { create } from "zustand"
import { persist, createJSONStorage } from "zustand/middleware"

// "context" is no longer a chat-side tab (moved to agent canvas), but
// older persisted user state may still hold it as activeTab. Keep it
// in the union so persisted JSON deserialises cleanly; the rail just
// doesn't render a button for it any more, and the rail migrates it to Artifacts.
export type DrawerTab = "files" | "artifacts" | "work" | "triggers" | "team" | "context"
export type DrawerMode = "overlay" | "push"

interface DrawerState {
  workSource: { workspaceId: string; agentId: string; source: import("@/components/features/chat/chat-tree-data").ChatWorkSource } | null

  open: boolean
  activeTab: DrawerTab
  mode: DrawerMode
  width: number
  toggle: (tab?: DrawerTab) => void
  setOpen: (v: boolean) => void
  setActiveTab: (tab: DrawerTab) => void
  setMode: (m: DrawerMode) => void
  setWidth: (w: number) => void
}

export const useDrawerStore = create<DrawerState>()(
  persist(
    (set, get) => ({
      workSource: null,
      open: true,
      activeTab: "artifacts",
      mode: "push",
      width: 340,
      toggle: (tab) => {
        const { open, activeTab } = get()
        if (tab && tab !== activeTab) {
          set({ open: true, activeTab: tab })
          return
        }
        set({ open: !open })
      },
      setOpen: (open) => set({ open }),
      setActiveTab: (activeTab) => set({ activeTab, open: true }),
      setMode: (mode) => set({ mode }),
      setWidth: (width) => set({ width: Math.max(280, Math.min(520, width)) }),
    }),
    {
      name: "crewship-chat-drawer",
      storage: createJSONStorage(() => localStorage),
      // Width belongs to the signed-in user's server preference, not this
      // browser-wide store. Old persisted widths are ignored on hydration.
      partialize: (s) => ({ mode: s.mode, activeTab: s.activeTab }),
      merge: (persisted, current) => {
        const saved = persisted as Partial<DrawerState> | undefined
        return { ...current, mode: saved?.mode ?? current.mode, activeTab: saved?.activeTab ?? current.activeTab }
      },
    },
  ),
)
