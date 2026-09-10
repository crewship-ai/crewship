import { create } from "zustand"

/** A single breadcrumb entry in the navigation trail. */
export interface BreadcrumbItem {
  label: string
  onClick?: () => void
  /** A destination, when the crumb is a place rather than an in-page action. */
  href?: string
}

// `settingsTab` used to live here: the Settings page mirrored its local active
// tab into the store so the global top bar could render a "Settings / <tab>"
// breadcrumb. That breadcrumb moved into the Settings sub-bar, which reads the
// page's own state, leaving the field with a writer and no reader.
interface AppState {
  currentWorkspaceId: string | null
  sidebarOpen: boolean
  breadcrumbs: BreadcrumbItem[]
  /**
   * The phone navigation sheet. It lives here rather than in AppToolbar
   * because two surfaces open it — the toolbar on a tablet and the phone tab
   * bar's "More" — and they are siblings, not parent and child.
   */
  mobileNavOpen: boolean
  setMobileNavOpen: (open: boolean) => void
  setCurrentWorkspaceId: (id: string | null) => void
  setSidebarOpen: (open: boolean) => void
  setBreadcrumbs: (items: BreadcrumbItem[]) => void
}

/** Global application state store (Zustand) for workspace context, sidebar, and breadcrumbs. */
export const useAppStore = create<AppState>((set) => ({
  currentWorkspaceId: null,
  sidebarOpen: true,
  breadcrumbs: [],
  mobileNavOpen: false,
  setCurrentWorkspaceId: (id) => set({ currentWorkspaceId: id }),
  setSidebarOpen: (open) => set({ sidebarOpen: open }),
  setBreadcrumbs: (items) => set({ breadcrumbs: items }),
  setMobileNavOpen: (open) => set({ mobileNavOpen: open }),
}))
