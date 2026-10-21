import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent, within } from "@testing-library/react"

import * as displayOptions from "../issues-display-options"
import { IssuesDisplayOptions } from "../issues-display-options"
import { DEFAULT_ROW_FIELDS } from "../issues-view-model"

// COM-02 (#3481): Issues uses the kit's one Segmented control
// (components/ui/segmented.tsx) instead of its own copy — one radio group
// where the arrow keys move and select and only the chosen segment is in
// the tab order. Its touch target and look are the kit's, tested there.

describe("Issues Display options use the kit Segmented", () => {
  it("the local Segmented copy is gone", () => {
    expect("Segmented" in displayOptions).toBe(false)
  })

  it("Group by and Order are kit segmented groups: arrows move and select, one tab stop", () => {
    const onGroupBy = vi.fn()
    render(
      <IssuesDisplayOptions
        open
        onOpenChange={() => {}}
        groupBy="status"
        onGroupBy={onGroupBy}
        orderBy="manual"
        onOrderBy={() => {}}
        fields={DEFAULT_ROW_FIELDS}
        onFields={() => {}}
      />,
    )
    const group = screen.getByRole("radiogroup", { name: "Group by" })
    expect(group).toHaveAttribute("data-slot", "segmented")
    expect(screen.getByRole("radiogroup", { name: "Order" })).toHaveAttribute("data-slot", "segmented")
    const radios = within(group).getAllByRole("radio")
    expect(radios.filter((r) => r.tabIndex === 0)).toHaveLength(1)
    const status = within(group).getByRole("radio", { name: "Status" })
    expect(status).toHaveAttribute("aria-checked", "true")
    fireEvent.keyDown(status, { key: "ArrowRight" })
    expect(onGroupBy).toHaveBeenCalledWith("project")
  })
})
