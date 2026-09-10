import { describe, expect, it, beforeEach, vi } from "vitest"
import { act, renderHook } from "@testing-library/react"

import { editorRouteHref, readEditorRoute, type EditorRoute } from "@/lib/pages/editor-contract"
import { useEditorRoute } from "@/components/features/pages/editor/use-editor-route"
import { navigationAllowed } from "@/hooks/use-navigation-guard"

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
  it("carries through query state the editor does not own", () => {
    // The panel tab bar writes ?tab= with its own replaceState. Rebuilding
    // the query from scratch dropped it, so opening a Page on its third tab
    // and clicking Edit came back to the first one.
    const href = editorRouteHref({ slug: "ops", mode: "edit", section: "access", pane: "section" }, "?tab=Fleet")
    expect(href).toContain("tab=Fleet")
    expect(href).toContain("mode=edit")
    expect(href).toContain("section=access")
    // And leaving the editor keeps it while dropping the editor's own keys.
    const back = editorRouteHref({ slug: "ops", mode: "view", section: "access", pane: "section" }, "?tab=Fleet&mode=edit&section=access")
    expect(back).toBe("/pages/ops?tab=Fleet")
  })

  it("ignores a preview pane outside Content", () => {
    const url = new URL("/pages/ops?mode=edit&section=access&pane=preview", "http://localhost")
    expect(readEditorRoute(url.pathname, url.search).pane).toBe("section")
  })

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
            // The preview is a workspace inside Content, so the address only
            // carries it there; anywhere else it would claim a state the
            // screen does not have.
            expect(back.pane).toBe(section === "content" ? pane : "section")
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

    it("guards Back that lands on another Page, where the prop moves and popstate is not enough", () => {
      // App Router treats Back as a navigation, so `useUrlSegment` re-reads
      // the location and the slug prop changes. Writing that straight into
      // state skipped the question entirely: the screen jumped to the other
      // Page with the dialog still open over it, naming the one just left.
      const { result, rerender } = renderHook(({ slug }: { slug: string }) => useEditorRoute(slug), {
        initialProps: { slug: "operations-lab" },
      })
      act(() => result.current.setMode("edit"))
      act(() => result.current.setDirty(true))
      go("/pages/fleet-overview")
      rerender({ slug: "fleet-overview" })
      expect(result.current.slug).toBe("operations-lab")
      expect(result.current.pending?.route.slug).toBe("fleet-overview")
      act(() => result.current.pending!.discard())
      expect(result.current.slug).toBe("fleet-overview")
      expect(result.current.mode).toBe("view")
    })

    it("answers one question at a time rather than dropping the first navigation", () => {
      const { result } = renderHook(() => useEditorRoute("operations-lab"))
      act(() => result.current.setMode("edit"))
      act(() => result.current.setSection("access"))
      act(() => result.current.setDirty(true))
      act(() => {
        go("/pages/operations-lab?mode=edit")
        window.dispatchEvent(new PopStateEvent("popstate"))
      })
      const first = result.current.pending
      act(() => {
        go("/pages/operations-lab")
        window.dispatchEvent(new PopStateEvent("popstate"))
      })
      // Still the first question. A second one would strand the first
      // navigation and later restore an address two entries stale.
      expect(result.current.pending).toBe(first)
    })

    it("refuses a workspace switch and performs it once the person agrees", () => {
      // Switching workspace is client state, not a navigation: nothing routes
      // and nothing reloads, so the editor was simply re-keyed and typed
      // edits vanished without a prompt.
      const { result } = renderHook(() => useEditorRoute("operations-lab"))
      act(() => result.current.setMode("edit"))
      act(() => result.current.setDirty(true))

      const retry = vi.fn()
      let allowed = true
      act(() => {
        allowed = navigationAllowed(retry)
      })
      expect(allowed).toBe(false)
      expect(retry).not.toHaveBeenCalled()
      expect(result.current.pending).not.toBeNull()

      act(() => result.current.pending!.discard())
      expect(retry).toHaveBeenCalledTimes(1)
      expect(result.current.dirty).toBe(false)
    })

    it("lets a workspace switch through when nothing is unsaved", () => {
      const { result } = renderHook(() => useEditorRoute("operations-lab"))
      act(() => result.current.setMode("edit"))
      expect(navigationAllowed(vi.fn())).toBe(true)
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
