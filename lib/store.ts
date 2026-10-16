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
  /** The ⌘K palette. Opened by the key (AppToolbar), the sidebar's search
   *  field and the phone top bar's search button. */
  /** Where a page's header (SubBar) renders its identity and actions: a slot
   *  inside the top bar (Harbor 2), registered by AppToolbar. Null on phones
   *  and outside the app shell, where SubBar renders in place. */
  pageBarSlot: HTMLElement | null
  setPageBarSlot: (el: HTMLElement | null) => void
  commandPaletteOpen: boolean
  setCommandPaletteOpen: (open: boolean | ((prev: boolean) => boolean)) => void
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
  commandPaletteOpen: false,
  pageBarSlot: null,
  setPageBarSlot: (el) => set({ pageBarSlot: el }),
  setCurrentWorkspaceId: (id) => set({ currentWorkspaceId: id }),
  setSidebarOpen: (open) => set({ sidebarOpen: open }),
  setBreadcrumbs: (items) => set({ breadcrumbs: items }),
  setMobileNavOpen: (open) => set({ mobileNavOpen: open }),
  setCommandPaletteOpen: (open) => set((s) => ({ commandPaletteOpen: typeof open === "function" ? open(s.commandPaletteOpen) : open })),
}))
