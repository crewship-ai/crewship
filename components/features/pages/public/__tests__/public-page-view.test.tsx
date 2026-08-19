/**
 * The public page, rendered (PRD `docs/prd/pages.md` §7.3).
 *
 * The server tests (`internal/api/pages_public_test.go`) prove what crosses the
 * boundary. These prove what the browser does with it, and there are exactly
 * three questions worth asking of a renderer on this surface:
 *
 *   1. Are there buttons in the grid? (§7.3.2 rule 1 — there must never be.)
 *   2. Does a failed panel show the age and hide the reason? (§7.3.2b.)
 *   3. Does the password prompt distinguish a wrong password from an unknown
 *      link? (§7.3.3 — it must not, and the UI is the easiest place to break
 *      a property the API got right.)
 */
import { describe, it, expect, vi } from "vitest"
import { render, screen, within, fireEvent } from "@testing-library/react"

import { PublicPageView, publicPanelProps } from "../public-page-view"
import type { PublicPage } from "../types"

const NOW = new Date("2026-08-12T13:00:00Z")

function page(overrides: Partial<PublicPage> = {}): PublicPage {
  return {
    slug: "uzaverka",
    name: "Uzávěrka",
    description: "Monthly close",
    generated_at: "2026-08-12T13:00:00Z",
    expires_at: "2026-09-11T13:00:00Z",
    show_provenance: false,
    panels: [
      {
        id: "sluzby",
        schema: "status.v1",
        title: "Stav služeb",
        span: 6,
        state: "fresh",
        produced_at: "2026-08-12T12:40:00Z",
        data: { items: [{ name: "api", state: "ok", label: "200 OK" }] },
      },
    ],
    ...overrides,
  }
}

function renderReady(p: PublicPage) {
  return render(
    <PublicPageView
      status="ready"
      page={p}
      message={null}
      submitting={false}
      onSubmitPassword={() => {}}
      now={NOW}
    />,
  )
}

// ── Rule 1 — read-only. No buttons. ────────────────────────────────────────

describe("§7.3.2 rule 1 — a public page renders no buttons", () => {
  it("puts no interactive control in the panel grid", () => {
    const { container } = renderReady(page())
    const grid = container.querySelector('[data-slot="public-panel-grid"]')
    expect(grid).not.toBeNull()

    // Every shape a click target takes, not just <button>. "A button behind a
    // public link is remote code execution with a URL for a credential", and a
    // link or a submit input is the same click.
    const interactive = grid!.querySelectorAll(
      'button, a[href], input, select, textarea, [role="button"], [role="link"], form, [onclick]',
    )
    expect(
      Array.from(interactive).map((el) => el.outerHTML),
      "§7.3.2 rule 1: the public panel grid must contain no interactive control",
    ).toEqual([])
  })

  it("renders nothing for an action a payload smuggled through", () => {
    // The server strips these before serialisation and the wire type has no
    // field for one — so if a payload key called `actions` ever reaches the
    // client, the renderer must ignore it rather than draw it.
    const withAction = page({
      panels: [
        {
          id: "sluzby",
          schema: "status.v1",
          title: "Stav služeb",
          span: 6,
          state: "fresh",
          produced_at: "2026-08-12T12:40:00Z",
          data: {
            items: [{ name: "api", state: "ok" }],
            actions: [{ id: "wipe", kind: "call", routine: "drop-database", label: "Run it" }],
          },
        },
      ],
    })
    const { container } = renderReady(withAction)
    expect(container.textContent).not.toContain("Run it")
    expect(container.textContent).not.toContain("drop-database")
    expect(container.querySelectorAll("button")).toHaveLength(0)
  })
})

// ── Rule 5 / §7.3.2b — the age, never the reason or the org chart. ─────────

describe("§7.3.2b — show the age, hide the reason", () => {
  it("shows when a failed panel last produced, and no failure text", () => {
    const failed = page({
      panels: [
        {
          id: "sluzby",
          schema: "status.v1",
          title: "Stav služeb",
          span: 6,
          state: "failed",
          // No `data` — the server omits the payload on a failed panel because
          // that is where the producer's own failure text lives.
          produced_at: "2026-08-12T12:40:00Z",
        },
      ],
    })
    const { container } = renderReady(failed)

    const age = container.querySelector('[data-slot="panel-age"]')
    expect(age, "a failed public panel must still say when its last value was produced").not.toBeNull()
    expect(age!.textContent).toContain("12 Aug 12:40")

    expect(container.textContent).toContain("Data are not current")
    // Nothing that names a container, a routine or a crew.
    for (const internal of ["OOM", "container", "routine", "crew/", "producer"]) {
      expect(container.textContent?.toLowerCase()).not.toContain(internal.toLowerCase())
    }
  })

  it("draws no provenance line when the publisher did not opt in", () => {
    const { container } = renderReady(page())
    const prov = container.querySelector('[data-slot="panel-provenance"]')
    // The footer may exist to carry the timestamp, but never a producer or a
    // run id.
    expect(prov?.textContent ?? "").not.toContain("script/")
    expect(prov?.textContent ?? "").not.toContain("push:")
    expect(container.textContent).not.toContain("watch-services.sh")
  })

  it("passes producer and run id through only when show_provenance is set", () => {
    const panel = {
      id: "sluzby",
      schema: "status.v1",
      state: "fresh" as const,
      produced_at: "2026-08-12T12:40:00Z",
      provenance: { producer: "script/watch-services.sh", run_id: "run_1" },
    }
    const off = publicPanelProps(panel, page({ show_provenance: false }), NOW)
    expect(off.data.provenance.producer).toBeNull()
    expect(off.data.provenance.run_id).toBeNull()
    expect(off.data.provenance.produced_at).toBe("2026-08-12T12:40:00Z")

    const on = publicPanelProps(panel, page({ show_provenance: true }), NOW)
    expect(on.data.provenance.producer).toBe("script/watch-services.sh")

    // publicView is not a prop the caller chooses. Every panel on this surface
    // is rendered as a public one.
    expect(off.publicView).toBe(true)
    expect(on.publicView).toBe(true)
    // And there is never a failure to render.
    expect(off.data.failure).toBeNull()
  })
})

// ── Rule 4 — the reader is told the link expires. ──────────────────────────

describe("§7.3.2 rule 4 — the link expires and says so", () => {
  it("prints an absolute expiry, never a vague one", () => {
    const { container } = renderReady(page())
    const footer = container.querySelector('[data-slot="public-page-expiry"]')
    expect(footer).not.toBeNull()
    expect(footer!.textContent).toContain("11 Sep 13:00")
    expect(footer!.textContent).not.toMatch(/in a while|soon|shortly/i)
  })
})

// ── The unavailable case ───────────────────────────────────────────────────

describe("an unavailable link", () => {
  it("says one thing for expired, revoked, mistyped and never-existed", () => {
    render(
      <PublicPageView
        status="unavailable"
        page={null}
        message={null}
        submitting={false}
        onSubmitPassword={() => {}}
      />,
    )
    expect(screen.getByText("This link is not available")).toBeTruthy()
    // The copy may name the LIKELY causes, but never asserts which one it was —
    // expired, revoked, mistyped and never-existed are facts about a workspace
    // this reader is outside of, and the server answers all four the same way.
    const body = document.body.textContent ?? ""
    expect(body).toMatch(/may have/i)
    expect(body).not.toMatch(/this link expired|was revoked on|no such page/i)
    // And nowhere to log in: there is no account behind this page.
    expect(screen.queryByText(/sign in|log in|login/i)).toBeNull()
  })
})

// ── §7.3.3 — the password prompt ───────────────────────────────────────────

describe("§7.3.3 — the password", () => {
  it("offers a password field and nothing that looks like an account", () => {
    render(
      <PublicPageView
        status="password"
        page={null}
        message={null}
        submitting={false}
        onSubmitPassword={() => {}}
      />,
    )
    const form = screen.getByText("This page is protected").closest("form")
    expect(form).not.toBeNull()
    const input = within(form!).getByLabelText(/password/i) as HTMLInputElement
    expect(input.type).toBe("password")

    // No account furniture: there is no account.
    expect(within(form!).queryByText(/email/i)).toBeNull()
    expect(within(form!).queryByText(/forgot/i)).toBeNull()
    expect(within(form!).queryByText(/sign up|create an account/i)).toBeNull()
  })

  it("repeats the server's refusal rather than diagnosing it", () => {
    render(
      <PublicPageView
        status="password"
        page={null}
        message="that link and password do not match"
        submitting={false}
        onSubmitPassword={() => {}}
      />,
    )
    const alert = screen.getByRole("alert")
    expect(alert.textContent).toBe("that link and password do not match")
    // The UI must not improve on it: "no such link" vs "wrong password" being
    // distinguishable is exactly what §7.3.3 forbids, and the renderer is the
    // easiest place to break a property the API got right.
    expect(alert.textContent).not.toMatch(/no such link|unknown link|wrong password|does not exist/i)
  })

  it("hands the typed password to the caller and never to a URL", () => {
    const onSubmit = vi.fn()
    render(
      <PublicPageView
        status="password"
        page={null}
        message={null}
        submitting={false}
        onSubmitPassword={onSubmit}
      />,
    )
    const form = screen.getByText("This page is protected").closest("form")!
    const input = within(form).getByLabelText(/password/i)
    fireEvent.change(input, { target: { value: "uzaverka-2026" } })
    fireEvent.submit(form)
    expect(onSubmit).toHaveBeenCalledWith("uzaverka-2026")
    // A form that navigated would put the field in the query string.
    expect(form.getAttribute("action")).toBeNull()
    expect(form.getAttribute("method")).toBeNull()
  })
})
