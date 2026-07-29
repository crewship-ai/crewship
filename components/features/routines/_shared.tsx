"use client"

// The routine detail primitives moved to components/ui/detail — they were the
// best-designed surface in the product and every other screen was re-inventing
// them at its own size. This file stays as the import path routines already
// use, so promoting the kit changed nothing here.
//
// `Card` maps to `DetailCard bare`: the original had no body padding, its
// children own their own, and adding padding would have shifted every routine
// tab by 16px.

import * as React from "react"

import { DetailCard, type DetailCardProps } from "@/components/ui/detail"

export { EmptyState, FieldLabel, Pill } from "@/components/ui/detail"
export type { EmptyStateProps, FieldLabelProps, PillProps } from "@/components/ui/detail"

export function Card(props: Omit<DetailCardProps, "bare">) {
  return <DetailCard bare {...props} />
}
