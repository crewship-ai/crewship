import { create } from "zustand"

/** A single breadcrumb entry in the navigation trail. */
export interface BreadcrumbItem {
  label: string
  onClick?: () => void
}

interface AppState {
  currentWorkspaceId: string | null
  sidebarOpen: boolean
  settingsTab: string | null
  breadcrumbs: BreadcrumbItem[]
  setCurrentWorkspaceId: (id: string | null) => void
  setSidebarOpen: (open: boolean) => void
  setSettingsTab: (tab: string | null) => void
  setBreadcrumbs: (items: BreadcrumbItem[]) => void
}

/** Global application state store (Zustand) for workspace context, sidebar, settings, and breadcrumbs. */
export const useAppStore = create<AppState>((set) => ({
  currentWorkspaceId: null,
  sidebarOpen: true,
  settingsTab: null,
  breadcrumbs: [],
  setCurrentWorkspaceId: (id) => set({ currentWorkspaceId: id }),
  setSidebarOpen: (open) => set({ sidebarOpen: open }),
  setSettingsTab: (tab) => set({ settingsTab: tab }),
  setBreadcrumbs: (items) => set({ breadcrumbs: items }),
}))
