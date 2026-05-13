"use client"

// Routines-feature aliases over the global skeleton library.
// The patterns originated here, were promoted to components/ui/skeletons.tsx
// during the UI unification migration, and re-exported here so existing
// imports keep working.

export {
  ListRowSkeleton as RoutineListSkeleton,
  DetailPanelSkeleton as RoutineDetailSkeleton,
  ListRowSkeleton as RoutineRunsSkeleton,
} from "@/components/ui/skeletons"
