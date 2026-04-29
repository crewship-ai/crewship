"use client"

import { create } from "zustand"
import { persist, createJSONStorage } from "zustand/middleware"

// "context" is no longer a chat-side tab (moved to agent canvas), but
// older persisted user state may still hold it as activeTab. Keep it
// in the union so persisted JSON deserialises cleanly; the rail just
// doesn't render a button for it any more, and the right-panel
// migrate effect on mount silently rewrites context → files.
export type DrawerTab = "files" | "triggers" | "team" | "context"
export type DrawerMode = "overlay" | "push"

interface DrawerState {
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
      open: false,
      activeTab: "files",
      mode: "overlay",
      width: 380,
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
      setWidth: (width) => set({ width: Math.max(280, Math.min(720, width)) }),
    }),
    {
      name: "crewship-chat-drawer",
      storage: createJSONStorage(() => localStorage),
      partialize: (s) => ({ mode: s.mode, width: s.width, activeTab: s.activeTab }),
    },
  ),
)
