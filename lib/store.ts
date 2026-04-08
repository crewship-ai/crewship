import { create } from "zustand"

interface AppState {
  currentWorkspaceId: string | null
  sidebarOpen: boolean
  settingsTab: string | null
  setCurrentWorkspaceId: (id: string | null) => void
  setSidebarOpen: (open: boolean) => void
  setSettingsTab: (tab: string | null) => void
}

export const useAppStore = create<AppState>((set) => ({
  currentWorkspaceId: null,
  sidebarOpen: true,
  settingsTab: null,
  setCurrentWorkspaceId: (id) => set({ currentWorkspaceId: id }),
  setSidebarOpen: (open) => set({ sidebarOpen: open }),
  setSettingsTab: (tab) => set({ settingsTab: tab }),
}))
