import { describe, expect, it, beforeEach, vi } from "vitest"
import { act, renderHook } from "@testing-library/react"

import { editorRouteHref, readEditorRoute, type EditorRoute } from "@/lib/pages/editor-contract"
import { useEditorRoute } from "@/components/features/pages/editor/use-editor-route"

/**
 * The address is the editor's only persistent state. Reload, Back, Forward and
 * a pasted link all have to land on the same Page and the same section, and
 * unsaved work has to be able to stop all four — including Back, where the
 * browser has already moved by the time we hear about it.
 */

function go(href: string) {
  window.history.replaceState(null, "", href)
}

describe("readEditorRoute", () => {
  const cases: Array<{ name: string; href: string; want: EditorRoute }> = [
    {
      name: "the overview has no page and cannot be in edit mode",
      href: "/pages",
      want: { slug: null, mode: "view", section: "content", pane: "section" },
    },
    {
      name: "a plain page link stays in view mode",
      href: "/pages/operations-lab",
      want: { slug: "operations-lab", mode: "view", section: "content", pane: "section" },
    },
    {
      name: "a trailing slash is the same page",
      href: "/pages/operations-lab/",
      want: { slug: "operations-lab", mode: "view", section: "content", pane: "section" },
    },
    {
      name: "mode and section are read from the query",
      href: "/pages/operations-lab?mode=edit&section=access",
      want: { slug: "operations-lab", mode: "edit", section: "access", pane: "section" },
    },
    {
      name: "an unknown section falls back rather than 404s",
      href: "/pages/operations-lab?mode=edit&section=wharrgarbl",
      want: { slug: "operations-lab", mode: "edit", section: "content", pane: "section" },
    },
    {
      name: "edit mode without a page is not a state",
      href: "/pages?mode=edit&section=access",
      want: { slug: null, mode: "view", section: "access", pane: "section" },
    },
    {
      name: "the preview pane only exists inside the editor",
      href: "/pages/operations-lab?mode=edit&pane=preview",
      want: { slug: "operations-lab", mode: "edit", section: "content", pane: "preview" },
    },
    {
      name: "a preview pane without edit mode is ignored",
      href: "/pages/operations-lab?pane=preview",
      want: { slug: "operations-lab", mode: "view", section: "content", pane: "section" },
    },
    {
      name: "a percent-encoded slug decodes",
      href: "/pages/q4%20review",
      want: { slug: "q4 review", mode: "view", section: "content", pane: "section" },
    },
  ]

  for (const c of cases) {
    it(c.name, () => {
      const url = new URL(c.href, "http://localhost")
      expect(readEditorRoute(url.pathname, url.search)).toEqual(c.want)
    })
  }
})

describe("editorRouteHref", () => {
  it("keeps the plain page link people already share", () => {
    expect(editorRouteHref({ slug: "operations-lab", mode: "view", section: "access", pane: "section" })).toBe(
      "/pages/operations-lab",
    )
  })

  it("omits the default section but keeps a chosen one", () => {
    expect(editorRouteHref({ slug: "ops", mode: "edit", section: "content", pane: "section" })).toBe("/pages/ops?mode=edit")
    expect(editorRouteHref({ slug: "ops", mode: "edit", section: "history", pane: "section" })).toBe(
      "/pages/ops?mode=edit&section=history",
    )
  })

  it("round-trips every route it can produce", () => {
    for (const mode of ["view", "edit"] as const) {
      for (const section of ["content", "data", "access", "history"] as const) {
        for (const pane of ["section", "preview"] as const) {
          const route: EditorRoute = { slug: "ops", mode, section, pane }
          const url = new URL(editorRouteHref(route), "http://localhost")
          const back = readEditorRoute(url.pathname, url.search)
          // In view mode the section and pane are not carried; the rest is.
          expect(back.slug).toBe("ops")
          expect(back.mode).toBe(mode)
          if (mode === "edit") {
            expect(back.section).toBe(section)
            expect(back.pane).toBe(pane)
          }
        }
      }
    }
  })
})

describe("useEditorRoute", () => {
  beforeEach(() => {
    go("/pages/operations-lab")
  })

  it("reads the editor out of the address on a cold arrival", () => {
    go("/pages/operations-lab?mode=edit&section=access")
    const { result } = renderHook(() => useEditorRoute("operations-lab"))
    expect(result.current.mode).toBe("edit")
    expect(result.current.section).toBe("access")
  })

  it("writes the address without navigating away", () => {
    const push = vi.spyOn(window.history, "pushState")
    const { result } = renderHook(() => useEditorRoute("operations-lab"))
    act(() => result.current.setMode("edit"))
    expect(window.location.search).toBe("?mode=edit")
    act(() => result.current.setSection("history"))
    expect(window.location.search).toBe("?mode=edit&section=history")
    expect(push).toHaveBeenCalled()
    push.mockRestore()
  })

  it("leaving the editor returns to the plain page link", () => {
    const { result } = renderHook(() => useEditorRoute("operations-lab"))
    act(() => result.current.setMode("edit"))
    act(() => result.current.setMode("view"))
    expect(window.location.pathname).toBe("/pages/operations-lab")
    expect(window.location.search).toBe("")
  })

  it("the preview pane replaces rather than stacks, so Back is not a walk back through it", () => {
    const push = vi.spyOn(window.history, "pushState")
    const replace = vi.spyOn(window.history, "replaceState")
    const { result } = renderHook(() => useEditorRoute("operations-lab"))
    act(() => result.current.setMode("edit"))
    push.mockClear()
    replace.mockClear()
    act(() => result.current.setPane("preview"))
    expect(replace).toHaveBeenCalled()
    expect(push).not.toHaveBeenCalled()
    push.mockRestore()
    replace.mockRestore()
  })

  it("follows Back to the section the address now names", () => {
    const { result } = renderHook(() => useEditorRoute("operations-lab"))
    act(() => result.current.setMode("edit"))
    act(() => result.current.setSection("access"))
    act(() => {
      go("/pages/operations-lab?mode=edit")
      window.dispatchEvent(new PopStateEvent("popstate"))
    })
    expect(result.current.section).toBe("content")
  })

  describe("unsaved work", () => {
    it("holds a section change until the question is answered", () => {
      const { result } = renderHook(() => useEditorRoute("operations-lab"))
      act(() => result.current.setMode("edit"))
      act(() => result.current.setDirty(true))
      act(() => result.current.setSection("access"))

      // Nothing moved yet: not the state, not the address.
      expect(result.current.section).toBe("content")
      expect(window.location.search).toBe("?mode=edit")
      expect(result.current.pending?.route.section).toBe("access")

      act(() => result.current.pending!.discard())
      expect(result.current.section).toBe("access")
      expect(result.current.dirty).toBe(false)
      expect(window.location.search).toBe("?mode=edit&section=access")
    })

    it("staying leaves the address exactly where it was", () => {
      const { result } = renderHook(() => useEditorRoute("operations-lab"))
      act(() => result.current.setMode("edit"))
      act(() => result.current.setDirty(true))
      act(() => result.current.setSection("access"))
      act(() => result.current.pending!.keep())

      expect(result.current.section).toBe("content")
      expect(result.current.dirty).toBe(true)
      expect(window.location.search).toBe("?mode=edit")
      expect(result.current.pending).toBeNull()
    })

    it("puts the address back when Back is refused, because Back already moved", () => {
      const { result } = renderHook(() => useEditorRoute("operations-lab"))
      act(() => result.current.setMode("edit"))
      act(() => result.current.setSection("access"))
      act(() => result.current.setDirty(true))

      act(() => {
        // The browser has already gone back by the time popstate fires.
        go("/pages/operations-lab?mode=edit")
        window.dispatchEvent(new PopStateEvent("popstate"))
      })
      expect(result.current.section).toBe("access")
      expect(result.current.pending).not.toBeNull()

      act(() => result.current.pending!.keep())
      // The screen still shows Access, so the address has to say Access too.
      expect(window.location.search).toBe("?mode=edit&section=access")
      expect(result.current.section).toBe("access")
    })

    it("guards leaving the Page as well as changing section", () => {
      const { result } = renderHook(() => useEditorRoute("operations-lab"))
      act(() => result.current.setMode("edit"))
      act(() => result.current.setDirty(true))
      act(() => result.current.openPage("fleet-overview"))
      expect(result.current.slug).toBe("operations-lab")
      act(() => result.current.pending!.discard())
      expect(result.current.slug).toBe("fleet-overview")
      expect(result.current.mode).toBe("view")
    })

    it("clears the flag once a navigation completes, so the next move is not blocked", () => {
      const { result } = renderHook(() => useEditorRoute("operations-lab"))
      act(() => result.current.setMode("edit"))
      act(() => result.current.setDirty(true))
      act(() => result.current.setSection("access"))
      act(() => result.current.pending!.discard())
      act(() => result.current.setSection("history"))
      expect(result.current.pending).toBeNull()
      expect(result.current.section).toBe("history")
    })
  })
})
