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
      // v67 `.seg`: 28 px outside, so the segments are the control height less
      // the 2 px padding and 1 px border either side. Coarse pointers keep 44 px.
      className="[--control-xs:calc(var(--control-sm)-6px)]"
      value={value}
      onChange={onChange}
      options={[
        { value: "list", label: "List", icon: List },
        { value: "board", label: "Board", icon: Kanban },
      ]}
    />
  )
}
