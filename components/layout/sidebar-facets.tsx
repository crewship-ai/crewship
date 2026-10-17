"use client"

import * as React from "react"

export interface SidebarFacetValue { value: string; label: React.ReactNode; count?: number; text?: string; icon?: React.ReactNode }

export function useSidebarFacets() {
  return { open: false, setOpen: (_: boolean) => {}, triggerProps: {}, blockProps: { id: "", open: false, onOpenChange: (_: boolean) => {} } }
}
export function SidebarFacetBlock(_: Record<string, unknown>) { return null }
export function SidebarInlineFacet(_: Record<string, unknown>) { return null }
