// The notifications landing with nothing connected used to be four zero
// tiles over a 260px empty box. It is one line with the action now.

import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { ConnectionsView } from "../views/connections-view"

function renderView(over: Partial<React.ComponentProps<typeof ConnectionsView>> = {}) {
  const onOpenAdd = vi.fn()
  render(
    <ConnectionsView
      rows={[]}
      totalRows={0}
      loading={false}
      error={null}
      canSeeDeliveries
      canManageWorkspace
      search=""
      onOpenAdd={onOpenAdd}
      onToggleEnabled={vi.fn()}
      onTest={vi.fn()}
      onDelete={vi.fn()}
      onSelect={vi.fn()}
      catalogMatches={0}
      {...over}
    />,
  )
  return { onOpenAdd }
}

describe("ConnectionsView with nothing connected", () => {
  it("collapses the zero tiles and the empty box into one line with the action", () => {
    const { onOpenAdd } = renderView()
    expect(screen.queryByText("Delivering")).not.toBeInTheDocument()
    const line = document.querySelector("[data-slot=inline-empty]")
    expect(line).toHaveTextContent(/No connections yet/)
    fireEvent.click(screen.getByRole("button", { name: /add an integration/i }))
    expect(onOpenAdd).toHaveBeenCalled()
  })

  it("keeps the tiles when filters hide rows that exist", () => {
    renderView({ totalRows: 3 })
    expect(screen.getByText("Delivering")).toBeInTheDocument()
    expect(document.querySelector("[data-slot=inline-empty]")).toHaveTextContent(/Nothing matches/)
  })
})
