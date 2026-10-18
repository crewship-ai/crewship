"use client"

import { Kanban, List } from "lucide-react"

import { Segmented } from "@/components/ui/segmented"

export type IssuesViewMode = "list" | "board"

/** The Issues List | Board toggle in the page bar. */
export function IssuesViewToggle({ value, onChange }: { value: IssuesViewMode; onChange: (mode: IssuesViewMode) => void }) {
  return (
    <Segmented
      ariaLabel="View"
      size="xs"
      value={value}
      onChange={onChange}
      options={[
        { value: "list", label: "List", icon: List },
        { value: "board", label: "Board", icon: Kanban },
      ]}
    />
  )
}
