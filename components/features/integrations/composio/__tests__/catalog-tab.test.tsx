import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { CatalogTab } from "@/components/features/integrations/composio/catalog-tab"
import type { ToolkitInfo } from "@/components/features/integrations/composio/types"

const toolkits = (n: number): ToolkitInfo[] =>
  Array.from({ length: n }, (_, i) => ({ slug: `app-${i}`, name: `App ${i}`, meta: {} }))

function renderTab(props: Partial<React.ComponentProps<typeof CatalogTab>>) {
  return render(
    <CatalogTab
      toolkits={toolkits(40)}
      total={120}
      search=""
      onSearch={() => {}}
      loading={false}
      configuredSlugs={new Set()}
      onConnect={() => {}}
      {...props}
    />,
  )
}

describe("CatalogTab scope line", () => {
  it("says how much of the catalog is on the page and offers to show more", () => {
    const onShowMore = vi.fn()
    renderTab({ onShowMore })
    expect(screen.getByTestId("catalog-scope")).toHaveTextContent("Showing 40 of 120 apps.")
    fireEvent.click(screen.getByRole("button", { name: "Show more" }))
    expect(onShowMore).toHaveBeenCalledTimes(1)
  })

  it("points at search once the page is at the gateway's cap", () => {
    renderTab({ toolkits: toolkits(100), onShowMore: undefined })
    expect(screen.getByTestId("catalog-scope")).toHaveTextContent("Showing 100 of 120 apps.")
    expect(screen.getByTestId("catalog-scope")).toHaveTextContent("Search to narrow the rest.")
    expect(screen.queryByRole("button", { name: "Show more" })).toBeNull()
  })

  it("stays quiet when everything fits", () => {
    renderTab({ toolkits: toolkits(12), total: 12, onShowMore: () => {} })
    expect(screen.queryByTestId("catalog-scope")).toBeNull()
  })
})
