// The sessions list folds while something takes the surface (an artifact in focus, an expanded Sheet) and comes back
// as the person had it once nothing does.
import { expect, it } from "vitest"
import { act, renderHook } from "@testing-library/react"
import { useState } from "react"
import { useFoldWhile } from "../use-fold-while"

function harness(initial: { wanted: boolean; collapsed: boolean }) {
  return renderHook(({ wanted }) => { const [collapsed, setCollapsed] = useState(initial.collapsed); useFoldWhile(wanted, collapsed, setCollapsed); return { collapsed, setCollapsed } }, { initialProps: { wanted: initial.wanted } })
}
it("folds while wanted and restores an open list after", () => {
  const h = harness({ wanted: false, collapsed: false })
  h.rerender({ wanted: true }); expect(h.result.current.collapsed).toBe(true)
  h.rerender({ wanted: false }); expect(h.result.current.collapsed).toBe(false)
})
it("leaves a list the person had folded folded", () => {
  const h = harness({ wanted: false, collapsed: true })
  h.rerender({ wanted: true }); h.rerender({ wanted: false })
  expect(h.result.current.collapsed).toBe(true)
})
it("lets the person open the list meanwhile, and does not fold it again until next time", () => {
  const h = harness({ wanted: false, collapsed: false })
  h.rerender({ wanted: true })
  act(() => h.result.current.setCollapsed(false))
  h.rerender({ wanted: true }); expect(h.result.current.collapsed).toBe(false)
})
