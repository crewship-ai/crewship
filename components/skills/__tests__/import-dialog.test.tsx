import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

import { ImportSkillDialog } from "../import-dialog"

// =============================================================================
// Skills → Import, on the shared create surface.
//
// The dialog is the Import archetype: three sources (a URL, a pasted SKILL.md,
// a whole repository) behind one primary, and two flags that only the repo
// source exposes. These tests pin the two things the migration must not move —
// the request each source issues, and the fact that a server refusal stays on
// screen — plus the one thing it must change: the shell it mounts.
// =============================================================================

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetch(...args),
}))

function jsonResponse(body: unknown, status = 200) {
  return { ok: status < 400, status, json: async () => body }
}

const IMPORTED = { skill_id: "sk_1", name: "My Skill", slug: "my-skill", created: true }

function openDialog(over: Partial<React.ComponentProps<typeof ImportSkillDialog>> = {}) {
  const onImported = vi.fn()
  render(<ImportSkillDialog workspaceId="ws_1" onImported={onImported} {...over} />)
  fireEvent.click(screen.getByRole("button", { name: /import skill/i }))
  return { onImported }
}

/**
 * The source switch is a chip row on the shell and was a tab strip before it;
 * clicking by TEXT is the one query that does not care which. Radix's tab
 * trigger activates on mousedown and a chip on click, so fire both — either
 * shape reaches the same state, and neither double-switches.
 */
function chooseSource(label: RegExp) {
  const el = screen.getByText(label)
  fireEvent.mouseDown(el)
  fireEvent.click(el)
}

function surface() {
  return document.querySelector('[data-slot="dialog-content"]')
}

function bodyOf(call: unknown[]) {
  return JSON.parse((call[1] as RequestInit).body as string)
}

beforeEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe("ImportSkillDialog", () => {
  it("mounts the shared create surface at md, not a bespoke dialog", () => {
    openDialog()
    const content = surface()
    expect(content).not.toBeNull()
    // Four fixed widths, and md is the form width. `sm:max-w-lg` was one of the
    // eleven this shell exists to delete.
    expect(content!.className).toContain("sm:max-w-[640px]")
    // The shell owns the padding: header, body and footer pad themselves, so
    // the content box must not carry the shared DialogContent's p-6.
    expect(content!.className).toContain("p-0")
  })

  it("posts the URL source to the single-skill import endpoint", async () => {
    apiFetch.mockResolvedValue(jsonResponse(IMPORTED))
    const { onImported } = openDialog()

    fireEvent.change(screen.getByLabelText(/SKILL\.md URL/i), {
      target: { value: "  https://github.com/org/repo/blob/main/s/SKILL.md  " },
    })
    fireEvent.click(screen.getByRole("button", { name: /^import$/i }))

    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
    const call = apiFetch.mock.calls[0]
    expect(call[0]).toBe("/api/v1/workspaces/ws_1/skills/import")
    expect((call[1] as RequestInit).method).toBe("POST")
    expect(bodyOf(call)).toEqual({
      url: "https://github.com/org/repo/blob/main/s/SKILL.md",
      allow_unsafe_license: false,
    })
    await waitFor(() => expect(onImported).toHaveBeenCalledWith(IMPORTED))
  })

  it("posts the pasted SKILL.md to the same endpoint as content", async () => {
    apiFetch.mockResolvedValue(jsonResponse(IMPORTED))
    openDialog()

    chooseSource(/paste content/i)
    fireEvent.change(screen.getByLabelText(/SKILL\.md Content/i), {
      target: { value: "---\nname: x\n---\n" },
    })
    fireEvent.click(screen.getByRole("button", { name: /^import$/i }))

    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
    const call = apiFetch.mock.calls[0]
    expect(call[0]).toBe("/api/v1/workspaces/ws_1/skills/import")
    // Trimmed, like the URL source.
    expect(bodyOf(call)).toEqual({ content: "---\nname: x\n---", allow_unsafe_license: false })
  })

  it("posts the repository source to bulk-import, carrying both flags", async () => {
    apiFetch.mockResolvedValue(
      jsonResponse({ source: "git", total_found: 2, total_imported: 0, imported: [], skipped: [] }),
    )
    openDialog()

    chooseSource(/from repo/i)
    fireEvent.change(screen.getByLabelText(/git repository url/i), {
      target: { value: "https://github.com/anthropics/skills" },
    })
    fireEvent.change(screen.getByLabelText(/ref \(optional\)/i), { target: { value: "main" } })
    fireEvent.change(screen.getByLabelText(/vendor namespace/i), { target: { value: "community" } })
    // Both default OFF, and both must reach the wire as booleans either way.
    // Dry run is repo-only and sits in the source block; the licence gate is
    // shared by all three sources and sits in the disclosure below them.
    fireEvent.click(screen.getByLabelText(/dry run/i))
    fireEvent.click(screen.getByRole("button", { name: /licensing/i }))
    fireEvent.click(screen.getByLabelText(/allow unrecognised licences/i))

    fireEvent.click(screen.getByRole("button", { name: /^preview$/i }))

    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
    const call = apiFetch.mock.calls[0]
    expect(call[0]).toBe("/api/v1/workspaces/ws_1/skills/bulk-import")
    expect(bodyOf(call)).toEqual({
      git_url: "https://github.com/anthropics/skills",
      git_ref: "main",
      vendor: "community",
      allow_unsafe_license: true,
      dry_run: true,
    })
  })

  it("keeps a server refusal on screen instead of a line of red text", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ detail: "that URL is not a SKILL.md" }, 400))
    openDialog()

    fireEvent.change(screen.getByLabelText(/SKILL\.md URL/i), {
      target: { value: "https://example.com/nope" },
    })
    fireEvent.click(screen.getByRole("button", { name: /^import$/i }))

    // A refusal is an alert, outside the scrollport — not something you can
    // scroll past and not something that fades.
    const refusal = await screen.findByRole("alert")
    expect(refusal.textContent).toContain("that URL is not a SKILL.md")
    // …and it does NOT close the surface.
    expect(surface()).not.toBeNull()
  })

  // The licence gate was reachable from the repository source only, while all
  // three sources send `allow_unsafe_license` — so a URL import of a skill
  // whose licence the SPDX scanner cannot identify (skills.go:355 reads the
  // flag on this route too) was refused by the server with the one control
  // that would let it through rendered on a different tab. /design puts
  // Licensing in a disclosure below the source, where every source has it.
  describe("the licence gate", () => {
    it("is reachable from the URL source, not just the repository one", async () => {
      apiFetch.mockResolvedValue(jsonResponse(IMPORTED))
      openDialog()

      fireEvent.click(screen.getByRole("button", { name: /licensing/i }))
      fireEvent.click(screen.getByLabelText(/allow unrecognised licences/i))

      fireEvent.change(screen.getByLabelText(/SKILL\.md URL/i), {
        target: { value: "https://github.com/org/repo/blob/main/s/SKILL.md" },
      })
      fireEvent.click(screen.getByRole("button", { name: /^import$/i }))

      await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
      expect(bodyOf(apiFetch.mock.calls[0])).toEqual({
        url: "https://github.com/org/repo/blob/main/s/SKILL.md",
        allow_unsafe_license: true,
      })
    })

    it("says which way it is currently set without being opened", () => {
      openDialog()
      expect(screen.getByText(/recognised licences only/i)).toBeInTheDocument()
    })

    it("still defaults to refusing what the scanner cannot identify", async () => {
      apiFetch.mockResolvedValue(jsonResponse(IMPORTED))
      openDialog()

      fireEvent.change(screen.getByLabelText(/SKILL\.md URL/i), {
        target: { value: "https://github.com/org/repo/blob/main/s/SKILL.md" },
      })
      fireEvent.click(screen.getByRole("button", { name: /^import$/i }))

      await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
      expect(bodyOf(apiFetch.mock.calls[0]).allow_unsafe_license).toBe(false)
    })
  })

  it("guards a dismissal once there is unsaved input", async () => {
    openDialog()
    fireEvent.change(screen.getByLabelText(/SKILL\.md URL/i), {
      target: { value: "https://github.com/org/repo/SKILL.md" },
    })

    fireEvent.keyDown(surface()!, { key: "Escape" })

    // The shell asks before throwing typed input away — Esc and the overlay are
    // the two routes a per-dialog guard always misses.
    await waitFor(() => expect(screen.getByText("Discard this import?")).toBeDefined())
    expect(surface()).not.toBeNull()
  })
})
