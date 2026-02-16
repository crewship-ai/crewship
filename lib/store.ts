import { create } from "zustand"

interface AppState {
  currentOrgId: string | null
  sidebarOpen: boolean
  setCurrentOrgId: (id: string | null) => void
  setSidebarOpen: (open: boolean) => void
}

export const useAppStore = create<AppState>((set) => ({
  currentOrgId: null,
  sidebarOpen: true,
  setCurrentOrgId: (id) => set({ currentOrgId: id }),
  setSidebarOpen: (open) => set({ sidebarOpen: open }),
}))
