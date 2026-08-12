/**
 * The in-app page editor — PRD `docs/prd/pages.md` §10b.1.
 *
 * Four things are worth a test here, and they are the four that were got wrong
 * somewhere else in this repo first:
 *
 *  1. **The document shape is not the wire shape.** A human writes
 *     `apiVersion`/`kind`/`metadata`/`spec` with `sla: 5m`; `POST /api/v1/pages`
 *     takes flat `{slug, name, description, panels}` with `sla_seconds: 300`
 *     (§11b decision 3). The CLI translates; so must the editor, and a
 *     round trip through both halves must not lose a field.
 *  2. **A refusal is shown in the server's own words** (#1563 rule 2). The
 *     missing-SLA one quotes §4 at you, and that sentence is the product.
 *  3. **A refusal destroys nothing** (#1563 rule 3). What was typed is what a
 *     retry needs.
 *  4. **An edit is a PATCH**, because every save is a version (§10b.1) and a
 *     delete-and-recreate has no history, a new id and no grants.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, fireEvent, cleanup, waitFor, act } from "@testing-library/react"

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}))

// The same FileEditor stand-in the routine editor's tests use, for the same
// reason: `code` is what CodeMirror is CONSTRUCTED from, and no user-facing
// query can see it — the DOM after a rebuild looks exactly like the DOM before
// one. `saveRef` is wired because the save path flushes the buffer through it.
const codeProps: string[] = []
let emitDocChange: ((text: string) => void) | null = null
let liveBuffer = ""
let lastCode: string | null = null

vi.mock("@/components/features/files/file-editor", () => ({
  FileEditor: ({
    code,
    onSave,
    onDocChange,
    onDirtyChange,
    saveRef,
  }: {
    code: string
    onSave: (t: string) => void
    onDocChange?: (t: string) => void
    onDirtyChange?: (d: boolean) => void
    saveRef?: { current: (() => void) | null }
  }) => {
    codeProps.push(code)
    if (lastCode !== code) {
      lastCode = code
      liveBuffer = code
    }
    emitDocChange = (t: string) => {
      liveBuffer = t
      onDirtyChange?.(true)
      onDocChange?.(t)
    }
    if (saveRef) saveRef.current = () => onSave(liveBuffer)
    return <div data-testid="editor" />
  },
}))

import {
  PageEditor,
  formatSlaSeconds,
  newPageTemplate,
  pageDiagnostics,
  pageDocumentText,
  parsePageBuffer,
  sealedPanelCount,
  slaSecondsFrom,
  type PageWriteBody,
} from "@/components/features/pages/page-editor"
import type { WirePage } from "@/hooks/use-pages"

// A page as `GET /api/v1/pages/{slug}` returns it, carrying every writable
// field a panel has — including the two nothing draws (`public`) and the one
// that is sugar in only one direction (`sla_seconds`).
const FLEET: WirePage = {
  id: "cpage1",
  slug: "fleet-201",
  name: "Flotila .201",
  description: "Stav flotily",
  owner: "crew/lookout",
  panels: [
    {
      id: "sluzby",
      schema: "status.v1",
      title: "Jede to?",
      owner: "crew/lookout",
      producer: "script/watch-services.sh",
      sla_seconds: 300,
      span: 8,
      public: true,
      state: "fresh",
      data: { items: [] },
      provenance: { producer: "script/watch-services.sh", run_id: "r1", produced_at: "2026-08-12T11:00:00Z" },
    },
    {
      id: "incident",
      schema: "narrative.v1",
      owner: "crew/devops",
      producer: "routine/incident-rozbor",
      sla_seconds: 3600,
      span: 12,
      state: "never_produced",
    },
  ],
}

// ── 1. The sugar (§11b decision 3) ─────────────────────────────────────────

describe("sla is sugar in the document and an integer on the wire", () => {
  it.each([
    ["5m", 300],
    ["30s", 30],
    ["1h", 3600],
    ["1h30m", 5400],
    ["90s", 90],
    ["0", 0],
    // Truncated to whole seconds exactly as `int(sla.Seconds())` does in
    // cmd_page.go — the server must refuse both spellings identically rather
    // than one of them quietly becoming a one-second SLA.
    ["500ms", 0],
  ])("reads %s as %i seconds", (input, want) => {
    expect(slaSecondsFrom(input)).toBe(want)
  })

  it("reads a bare number as seconds", () => {
    expect(slaSecondsFrom(300)).toBe(300)
  })

  it.each(["5 minutes", "1hour", "5m junk", "", "  ", "soon"])(
    "refuses to guess at %o",
    (input) => {
      // null, not 0: a zero would be a number nobody typed, and it would reach
      // the server as a deliberate SLA of zero rather than as an absence.
      expect(slaSecondsFrom(input)).toBeNull()
    },
  )

  it.each([
    [300, "5m"],
    [30, "30s"],
    [3600, "1h"],
    [5400, "1h30m"],
    [0, "0s"],
  ])("writes %i seconds back as %s", (input, want) => {
    expect(formatSlaSeconds(input)).toBe(want)
  })
})

// ── 2. The round trip ──────────────────────────────────────────────────────

describe("a stored page round-trips through the document and back to the wire", () => {
  it("renders the manifest envelope a human (and the CLI) reads", () => {
    const text = pageDocumentText(FLEET)
    expect(text).toContain("apiVersion: crewship/v1")
    expect(text).toContain("kind: Page")
    expect(text).toContain("slug: fleet-201")
    // The document carries the sugar, never the integer.
    expect(text).toContain("sla: 5m")
    expect(text).not.toContain("sla_seconds")
  })

  it("comes back as the flat wire shape, with sla: 5m as sla_seconds: 300", () => {
    const parsed = parsePageBuffer(pageDocumentText(FLEET))
    expect(parsed.ok).toBe(true)
    const body = (parsed as { ok: true; body: PageWriteBody }).body

    expect(body.slug).toBe("fleet-201")
    expect(body.name).toBe("Flotila .201")
    expect(body.description).toBe("Stav flotily")
    expect(body.panels).toEqual([
      {
        id: "sluzby",
        schema: "status.v1",
        title: "Jede to?",
        owner: "crew/lookout",
        producer: "script/watch-services.sh",
        sla_seconds: 300,
        span: 8,
        public: true,
      },
      {
        id: "incident",
        schema: "narrative.v1",
        owner: "crew/devops",
        producer: "routine/incident-rozbor",
        sla_seconds: 3600,
        span: 12,
      },
    ])
  })

  it("carries no server-attached field back onto the wire", () => {
    // §4 rule 5 / §7.1b: state, provenance and payload are the server's to
    // write. A round trip that sent them back would be a producer claiming an
    // identity and a timestamp through the editing path.
    const body = JSON.stringify(
      (parsePageBuffer(pageDocumentText(FLEET)) as { ok: true; body: PageWriteBody }).body,
    )
    for (const field of ["state", "provenance", "produced_at", "run_id", "data"]) {
      expect(body).not.toContain(field)
    }
  })

  it("omits sla_seconds rather than inventing one when the document has no sla", () => {
    // §4: there is no default that means "never mind", and the SERVER is the
    // one that gets to say so — in a sentence that quotes §4.
    const parsed = parsePageBuffer(
      [
        "apiVersion: crewship/v1",
        "kind: Page",
        "metadata:",
        "  name: P",
        "  slug: p",
        "spec:",
        "  panels:",
        "    - id: a",
        "      schema: status.v1",
        "      owner: crew/lookout",
        "      producer: routine/x",
        "",
      ].join("\n"),
    )
    expect(parsed.ok).toBe(true)
    expect((parsed as { ok: true; body: PageWriteBody }).body.panels[0]).not.toHaveProperty(
      "sla_seconds",
    )
  })

  it("refuses a document that is not a Page, in the server's own wording", () => {
    // apiVersion/kind never reach the server — the client flattens the
    // document before sending (§11b decision 2) — so unchecked here they would
    // be silently ignored and a routine DSL would be POSTed as a page.
    const parsed = parsePageBuffer("apiVersion: crewship/v1\nkind: Routine\nsteps: []\n")
    expect(parsed.ok).toBe(false)
    expect((parsed as { ok: false; message: string }).message).toContain('kind "Routine"')
  })

  it("reports the line of a YAML syntax error", () => {
    const parsed = parsePageBuffer("apiVersion: crewship/v1\nkind: Page\n\tbad: indent\n")
    expect(parsed.ok).toBe(false)
    expect((parsed as { ok: false; line?: number }).line).toBeGreaterThan(0)
  })

  it("never drops a sealed panel silently", () => {
    // §11b.14: the placeholder carries no schema, no producer and no SLA, so
    // the document cannot describe it — and a PATCH built from that document
    // would delete another crew's panel.
    const withSealed: WirePage = {
      ...FLEET,
      panels: [...(FLEET.panels as object[]), { panel_id: "secret", span: 4, sealed: true, owner_crew_name: "Finance" }],
    }
    expect(sealedPanelCount(withSealed)).toBe(1)
    expect(sealedPanelCount(FLEET)).toBe(0)
  })
})

describe("the starter document", () => {
  it("is a Page, and every field the gate requires is visibly a placeholder", () => {
    const parsed = parsePageBuffer(newPageTemplate("crew/lookout"))
    expect(parsed.ok).toBe(true)
    const body = (parsed as { ok: true; body: PageWriteBody }).body
    expect(body.panels[0].owner).toBe("crew/lookout")
    expect(body.panels[0].sla_seconds).toBe(300)
    expect(body.panels[0].producer).toContain("CHANGEME")
  })
})

describe("the inline linter is additive", () => {
  it("questions an unknown schema and a missing sla without blocking either", () => {
    const warnings = pageDiagnostics(
      [
        "apiVersion: crewship/v1",
        "kind: Page",
        "metadata:",
        "  name: P",
        "  slug: p",
        "spec:",
        "  panels:",
        "    - id: a",
        "      schema: gauge.v1",
        "      owner: user/bob",
        "      producer: sql/select-1",
        "",
      ].join("\n"),
    )
    const messages = warnings.map((w) => w.message).join(" | ")
    expect(messages).toContain("gauge.v1")
    expect(messages).toContain("crew/<slug>")
    expect(messages).toContain("sla")
    // Every one of them is a hint. The verdict belongs to the server (§10b.1),
    // and a client that refuses what the server would accept is a second gate
    // that can disagree with the first.
    expect(warnings.every((w) => w.severity === "warning")).toBe(true)
  })
})

// ── 3. The component ───────────────────────────────────────────────────────

function okJSON(body: unknown, status = 200): Response {
  const text = JSON.stringify(body)
  return {
    ok: true,
    status,
    headers: new Headers(),
    json: async () => body,
    text: async () => text,
  } as unknown as Response
}

function refusal(message: string, status = 400): Response {
  const body = { error: message }
  const text = JSON.stringify(body)
  return {
    ok: false,
    status,
    headers: new Headers(),
    json: async () => body,
    text: async () => text,
  } as unknown as Response
}

interface Call {
  url: string
  method: string
  body: unknown
}

function renderEditor(
  props: Partial<React.ComponentProps<typeof PageEditor>>,
  respond: (call: Call) => Response,
) {
  const calls: Call[] = []
  const mockFetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const call: Call = {
      url: String(input),
      method: init?.method ?? "GET",
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    }
    calls.push(call)
    return respond(call)
  })
  vi.stubGlobal("fetch", mockFetch)
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  render(
    <QueryClientProvider client={qc}>
      <PageEditor
        workspaceId="ws-1"
        mode="create"
        onClose={vi.fn()}
        {...props}
      />
    </QueryClientProvider>,
  )
  return { calls }
}

describe("<PageEditor>", () => {
  beforeEach(() => {
    cleanup()
    codeProps.length = 0
    emitDocChange = null
    liveBuffer = ""
    lastCode = null
  })
  afterEach(() => vi.unstubAllGlobals())

  it("POSTs the flat wire shape when creating, with sla: 5m sent as sla_seconds: 300", async () => {
    const { calls } = renderEditor({ mode: "create" }, () => okJSON({ slug: "new-page" }))
    fireEvent.click(screen.getByRole("button", { name: /create page/i }))

    await waitFor(() => expect(calls.length).toBe(1))
    expect(calls[0].method).toBe("POST")
    expect(calls[0].url).toContain("/api/v1/pages?workspace_id=ws-1")
    const body = calls[0].body as PageWriteBody
    expect(body.slug).toBe("new-page")
    expect(body.panels[0].sla_seconds).toBe(300)
    // The envelope has already done its job; the server takes the flat shape.
    expect(body).not.toHaveProperty("apiVersion")
    expect(body).not.toHaveProperty("spec")
  })

  it("PATCHes when editing — every save is a version, so nothing is recreated", async () => {
    const onSaved = vi.fn()
    const { calls } = renderEditor({ mode: "edit", page: FLEET, onSaved }, () =>
      okJSON({ slug: "fleet-201" }),
    )
    fireEvent.click(screen.getByRole("button", { name: /^save$/i }))

    await waitFor(() => expect(calls.length).toBe(1))
    expect(calls[0].method).toBe("PATCH")
    expect(calls[0].url).toContain("/api/v1/pages/fleet-201")
    // A delete-and-recreate loses the version history, the page id and every
    // grant on it (§10b.1, §7.2).
    expect(calls.some((c) => c.method === "DELETE")).toBe(false)
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith("fleet-201"))
  })

  it("shows the server's refusal verbatim and keeps the text that was typed", async () => {
    // The real sentence `internal/pages/spec.go` produces for a panel whose
    // SLA did not survive the translation. It quotes §4, and paraphrasing it
    // would replace an explanation with a restatement.
    const SERVER_WORDS =
      "panel \"sluzby\" declares sla \"0s\"; an SLA of zero is the default that means 'never mind', and §4 says there is not one"

    const typed = [
      "apiVersion: crewship/v1",
      "kind: Page",
      "metadata:",
      "  name: Flotila",
      "  slug: fleet-202",
      "spec:",
      "  panels:",
      "    - id: sluzby",
      "      schema: status.v1",
      "      owner: crew/lookout",
      "      producer: routine/watch",
      "",
    ].join("\n")

    const { calls } = renderEditor({ mode: "create" }, () => refusal(SERVER_WORDS))
    act(() => emitDocChange!(typed))
    const codeBeforeSave = codeProps[codeProps.length - 1]

    fireEvent.click(screen.getByRole("button", { name: /create page/i }))
    await waitFor(() => expect(screen.getByTestId("page-editor-refusal")).toBeTruthy())

    // Rule 2 — the server's own words, not ours.
    expect(screen.getByTestId("page-editor-refusal").textContent).toContain(SERVER_WORDS)

    // Rule 3 — nothing a retry needs was destroyed. The editor is still open,
    // still constructed from the same document, and the buffer still holds
    // what was typed.
    expect(screen.getByTestId("editor")).toBeTruthy()
    expect(codeProps[codeProps.length - 1]).toBe(codeBeforeSave)
    expect(liveBuffer).toBe(typed)

    // And a retry sends exactly that document again.
    fireEvent.click(screen.getByRole("button", { name: /create page/i }))
    await waitFor(() => expect(calls.length).toBe(2))
    expect((calls[1].body as PageWriteBody).slug).toBe("fleet-202")
    expect((calls[1].body as PageWriteBody).panels[0]).not.toHaveProperty("sla_seconds")
  })

  it("does not rebuild the editor while you type", () => {
    // routine-editor-remount.test.tsx's lesson, which cost a shredded routine:
    // FileEditor rebuilds its EditorState when `code` changes, and a rebuild
    // puts the caret back at position 0.
    renderEditor({ mode: "create" }, () => okJSON({}))
    const initial = codeProps[codeProps.length - 1]
    act(() => emitDocChange!(initial + "\n# typing"))
    act(() => emitDocChange!(initial + "\n# typing more"))
    expect(codeProps[codeProps.length - 1]).toBe(initial)
  })

  it("mounts exactly one editor", () => {
    renderEditor({ mode: "edit", page: FLEET }, () => okJSON({}))
    expect(screen.getAllByTestId("editor")).toHaveLength(1)
  })

  it("refuses to save a page carrying a panel the viewer may not see", async () => {
    const withSealed: WirePage = {
      ...FLEET,
      panels: [
        ...(FLEET.panels as object[]),
        { panel_id: "secret", span: 4, sealed: true, owner_crew_name: "Finance" },
      ],
    }
    const { calls } = renderEditor({ mode: "edit", page: withSealed }, () => okJSON({}))
    const save = screen.getByRole("button", { name: /^save$/i })
    expect(save).toBeDisabled()
    fireEvent.click(save)
    await waitFor(() => expect(calls.length).toBe(0))
    expect(screen.getByText(/owned by a crew you are not in/i)).toBeTruthy()
  })

  it("asks before throwing away an unsaved document", async () => {
    // Everything else in this component goes to some length not to destroy a
    // buffer a retry needs; a stray click on the backdrop would undo all of
    // it, and a spec that was never on disk has nowhere to come back from.
    const onClose = vi.fn()
    renderEditor({ mode: "create", onClose }, () => okJSON({}))
    act(() => emitDocChange!("apiVersion: crewship/v1\nkind: Page\n"))

    fireEvent.click(screen.getByLabelText("Close the page editor"))
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByText(/unsaved changes/i)).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: /discard/i }))
    expect(onClose).toHaveBeenCalled()
  })

  it("closes straight away when nothing has been typed", () => {
    const onClose = vi.fn()
    renderEditor({ mode: "create", onClose }, () => okJSON({}))
    fireEvent.click(screen.getByRole("button", { name: /cancel/i }))
    expect(onClose).toHaveBeenCalled()
  })

  it("reports a transport failure as a transport failure, not as a refusal", async () => {
    // #1563 rule 4: `catch` covers transport only. Telling someone their spec
    // was rejected when the network dropped sends them to edit a document the
    // server never read.
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new Error("Failed to fetch")
      }),
    )
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
    render(
      <QueryClientProvider client={qc}>
        <PageEditor workspaceId="ws-1" mode="create" onClose={vi.fn()} />
      </QueryClientProvider>,
    )
    fireEvent.click(screen.getByRole("button", { name: /create page/i }))
    await waitFor(() =>
      expect(screen.getByTestId("page-editor-refusal").textContent).toContain(
        "Could not reach the server",
      ),
    )
  })
})
