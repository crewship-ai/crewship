"use client"

import { create } from "zustand"

/**
 * UI-local state for the admin backups panel. Persistent data lives
 * in React Query; this store only tracks which dialogs are open and
 * which row the user selected. Kept small on purpose — once it grows
 * past 3-4 fields, split by concern.
 */
type DialogKind = "create" | "restore" | "inspect" | null

interface BackupUIState {
  selectedPath: string | null
  dialog: DialogKind
  openCreate: () => void
  openRestore: (path: string) => void
  openInspect: (path: string) => void
  close: () => void
}

export const useBackupStore = create<BackupUIState>((set) => ({
  selectedPath: null,
  dialog: null,
  openCreate: () => set({ dialog: "create", selectedPath: null }),
  openRestore: (path) => set({ dialog: "restore", selectedPath: path }),
  openInspect: (path) => set({ dialog: "inspect", selectedPath: path }),
  close: () => set({ dialog: null, selectedPath: null }),
}))
