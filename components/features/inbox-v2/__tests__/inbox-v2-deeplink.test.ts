import { describe, expect, it } from "vitest"
import { renderHook, act } from "@testing-library/react"
import { useState } from "react"

import { EMPTY_INBOX_V2_FILTERS, type InboxV2Filters } from "../inbox-v2-derive"
import { useInboxV2DeepLink } from "../inbox-v2-deeplink"

/**
 * Drives the hook the way the route does: the container owns the state, the
 * URL values arrive as props, and a navigation is a rerender with new ones.
 */
function harness(initial: { item: string | null; search: string }) {
  return renderHook(
    ({ item, search }: { item: string | null; search: string }) => {
      const [selectedKey, setSelectedKey] = useState<string | null>(
        item ? `request:${item}` : null,
      )
      const [filters, setFilters] = useState<InboxV2Filters>({
        ...EMPTY_INBOX_V2_FILTERS,
        search,
      })
      useInboxV2DeepLink(item, search, setSelectedKey, setFilters)
      return { selectedKey, filters, setSelectedKey, setFilters }
    },
    { initialProps: initial },
  )
}

describe("inbox v2 deep links track the URL after mount", () => {
  it("opens the row a same-route push names", () => {
    const { result, rerender } = harness({ item: null, search: "" })
    rerender({ item: "itm-9", search: "" })
    expect(result.current.selectedKey).toBe("request:itm-9")
  })

  it("clears the pane when the new URL names no row", () => {
    // Leaving ?item= for the bare route is a request to show nothing. Holding
    // the previous row is how the wrong request gets decided.
    const { result, rerender } = harness({ item: "itm-9", search: "" })
    expect(result.current.selectedKey).toBe("request:itm-9")
    rerender({ item: null, search: "" })
    expect(result.current.selectedKey).toBeNull()
  })

  it("follows a changed agent filter", () => {
    const { result, rerender } = harness({ item: null, search: "casey" })
    rerender({ item: null, search: "riley" })
    expect(result.current.filters.search).toBe("riley")
  })

  it("does not clobber a search the user typed", () => {
    // The URL value has not changed, so neither effect re-runs. Only a real
    // navigation may overwrite what is in the box.
    const { result, rerender } = harness({ item: null, search: "casey" })
    act(() => {
      result.current.setFilters((current) => ({ ...current, search: "invoice" }))
    })
    rerender({ item: null, search: "casey" })
    expect(result.current.filters.search).toBe("invoice")
  })

  it("does not clobber a row the user clicked", () => {
    const { result, rerender } = harness({ item: "itm-9", search: "" })
    act(() => {
      result.current.setSelectedKey("inbox:itm-3")
    })
    rerender({ item: "itm-9", search: "" })
    expect(result.current.selectedKey).toBe("inbox:itm-3")
  })
})
