// The pull-requests card on the issue detail.
//
// Two halves, and the second is the one that matters most.
//
//   1. It reads. The state is drawn BEFORE the title, because a merged pull
//      request, a closed one, an open draft and an open ready-to-review one
//      are four different things and that is what the reader is scanning for.
//      An issue with no links says so in a sentence, not with an empty box.
//      A failure says what to do — the server's own sentence, not ours.
//
//   2. It cannot be used to inject anything. The title, author and branch
//      names in that payload are written by whoever opened the pull request on
//      the forge; on a public repository, by anyone. They are text here and
//      nothing else — not markdown, not HTML, and the URL never reaches an
//      href without its scheme being re-checked.

import * as React from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"

import { IssueCodeLinksCard, type CodeLinkEdit } from "../issue-code-links-card"
import { IssueCardDetail } from "../issue-card-detail"
import type { IssueCodeLink } from "@/lib/types/mission"

vi.mock("next/link", () => ({
  default: ({ children, href }: { children: React.ReactNode; href: string }) => (
    <a href={href}>{children}</a>
  ),
}))
vi.mock("@/components/features/issues/tiptap-editor", () => ({
  TiptapEditor: () => <div data-testid="tiptap" />,
}))

function link(over: Partial<IssueCodeLink> = {}): IssueCodeLink {
  return {
    id: "link-1",
    mission_id: "m1",
    workspace_id: "ws1",
    provider: "GITHUB",
    host: "github.com",
    owner: "acme",
    repo: "thing",
    number: 7,
    kind: "PULL_REQUEST",
    url: "https://github.com/acme/thing/pull/7",
    title: "Add the widget",
    state: "OPEN",
    author: "octocat",
    source_branch: "feat/widget",
    target_branch: "main",
    remote_created_at: null,
    remote_updated_at: null,
    remote_merged_at: null,
    remote_closed_at: null,
    credential_id: "cred1",
    last_synced_at: "2026-08-04T18:31:03Z",
    last_sync_error: null,
    created_at: "2026-08-04T18:31:03Z",
    updated_at: "2026-08-04T18:31:03Z",
    ...over,
  }
}

function editStub(over: Partial<CodeLinkEdit> = {}): CodeLinkEdit {
  return {
    attach: vi.fn(async () => ({ ok: true as const })),
    remove: vi.fn(async () => {}),
    refresh: vi.fn(async () => {}),
    ...over,
  }
}

/** Opens the attach popover and returns its content. */
function openAttach() {
  fireEvent.click(screen.getByRole("button", { name: /attach a pull request/i }))
  return screen.getByRole("dialog")
}

describe("IssueCodeLinksCard — what it shows", () => {
  it("draws the state before the title, so the column can be scanned", () => {
    render(<IssueCodeLinksCard links={[link()]} />)

    const row = screen.getByTestId("code-link-row")
    const state = within(row).getByTestId("code-link-state")
    expect(state).toHaveTextContent("Open")

    // Document order, not visual order — the assertion that survives a
    // restyle. `compareDocumentPosition` returns FOLLOWING when the state
    // comes first.
    const title = within(row).getByText("Add the widget")
    expect(state.compareDocumentPosition(title) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it("tells the four states apart", () => {
    render(
      <IssueCodeLinksCard
        links={[
          link({ id: "a", number: 1, state: "OPEN", title: "ready" }),
          link({ id: "b", number: 2, state: "DRAFT", title: "not yet" }),
          link({ id: "c", number: 3, state: "MERGED", title: "landed" }),
          link({ id: "d", number: 4, state: "CLOSED", title: "abandoned" }),
        ]}
      />,
    )
    const states = screen.getAllByTestId("code-link-state").map((n) => n.textContent?.trim())
    expect(states).toEqual(["Open", "Draft", "Merged", "Closed"])

    // Four states, four visual treatments. If a restyle collapses two of them
    // the card stops answering the question it exists to answer.
    const classes = screen.getAllByTestId("code-link-state").map((n) => n.className)
    expect(new Set(classes).size).toBe(4)
  })

  it("shows the ref, the author and the branch pair", () => {
    render(<IssueCodeLinksCard links={[link()]} />)
    const row = screen.getByTestId("code-link-row")
    expect(within(row).getByText("acme/thing#7")).toBeInTheDocument()
    expect(within(row).getByText(/octocat/)).toBeInTheDocument()
    expect(within(row).getByText("feat/widget → main")).toBeInTheDocument()
  })

  it("links out in a new tab without handing the opener over", () => {
    render(<IssueCodeLinksCard links={[link()]} />)
    const a = screen.getByRole("link", { name: /add the widget/i })
    expect(a).toHaveAttribute("href", "https://github.com/acme/thing/pull/7")
    expect(a).toHaveAttribute("target", "_blank")
    expect(a.getAttribute("rel")).toMatch(/noopener/)
    expect(a.getAttribute("rel")).toMatch(/noreferrer/)
  })

  it("falls back to the ref when the provider gave no title", () => {
    render(<IssueCodeLinksCard links={[link({ title: null })]} />)
    expect(screen.getByRole("link", { name: /acme\/thing#7/ })).toBeInTheDocument()
  })

  // A refresh that fails keeps the state it already had and records why. The
  // card has to say so, or it presents week-old truth as current.
  it("says when a row is showing stale state, and why", () => {
    render(
      <IssueCodeLinksCard
        links={[link({ state: "MERGED", last_sync_error: "401 from github.com" })]}
      />,
    )
    expect(screen.getByTestId("code-link-stale")).toHaveTextContent(/401 from github\.com/)
    // The state it knew is still shown — losing it would be worse than
    // showing it with a caveat.
    expect(screen.getByTestId("code-link-state")).toHaveTextContent("Merged")
  })

  it("says nothing about staleness on a healthy row", () => {
    render(<IssueCodeLinksCard links={[link()]} />)
    expect(screen.queryByTestId("code-link-stale")).toBeNull()
  })

  // In the voice the other cards use: "Nothing blocks this and nothing hangs
  // off it", "No routine bound. Starting this issue hands it to …".
  it("says plainly that nothing is attached, and how to change that", () => {
    render(<IssueCodeLinksCard links={[]} edit={editStub()} />)
    expect(screen.getByText(/no pull request attached/i)).toBeInTheDocument()
    expect(screen.getByText(/paste one/i)).toBeInTheDocument()
  })

  it("drops the invitation for a reader who cannot write", () => {
    render(<IssueCodeLinksCard links={[]} />)
    expect(screen.getByText(/no pull request attached/i)).toBeInTheDocument()
    expect(screen.queryByText(/paste one/i)).toBeNull()
    expect(screen.queryByRole("button", { name: /attach a pull request/i })).toBeNull()
  })

  it("hides every write affordance from a reader who cannot write", () => {
    render(<IssueCodeLinksCard links={[link()]} />)
    expect(screen.queryByRole("button", { name: /remove link/i })).toBeNull()
    expect(screen.queryByRole("button", { name: /refresh/i })).toBeNull()
  })
})

describe("IssueCodeLinksCard — attaching", () => {
  it("posts the pasted URL and closes on success", async () => {
    const edit = editStub()
    render(<IssueCodeLinksCard links={[]} edit={edit} />)

    const menu = openAttach()
    fireEvent.change(within(menu).getByLabelText(/pull request url/i), {
      target: { value: " https://github.com/acme/thing/pull/7 " },
    })
    fireEvent.click(within(menu).getByRole("button", { name: /^attach$/i }))

    await waitFor(() =>
      expect(edit.attach).toHaveBeenCalledWith("https://github.com/acme/thing/pull/7"),
    )
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  })

  it("submits on Enter", async () => {
    const edit = editStub()
    render(<IssueCodeLinksCard links={[]} edit={edit} />)
    const menu = openAttach()
    const box = within(menu).getByLabelText(/pull request url/i)
    fireEvent.change(box, { target: { value: "https://github.com/acme/thing/pull/7" } })
    fireEvent.keyDown(box, { key: "Enter" })
    await waitFor(() => expect(edit.attach).toHaveBeenCalled())
  })

  // A CJK reader presses Enter to CONFIRM an IME candidate, not to submit.
  // Without the composition guard that keystroke posts a half-composed URL and
  // the popover answers with a parse failure for something they never finished
  // typing — a failure message that is worse than useless, because it describes
  // a URL that was never intended.
  it("lets an IME candidate be confirmed without submitting", async () => {
    const edit = editStub()
    render(<IssueCodeLinksCard links={[]} edit={edit} />)
    const menu = openAttach()
    const box = within(menu).getByLabelText(/pull request url/i)

    fireEvent.change(box, { target: { value: "https://github.com/acme/thin" } })
    fireEvent.keyDown(box, { key: "Enter", isComposing: true })
    expect(edit.attach).not.toHaveBeenCalled()

    // The same key, once composition has ended, still submits.
    fireEvent.change(box, { target: { value: "https://github.com/acme/thing/pull/7" } })
    fireEvent.keyDown(box, { key: "Enter" })
    await waitFor(() =>
      expect(edit.attach).toHaveBeenCalledWith("https://github.com/acme/thing/pull/7"),
    )
  })

  it("does not post an empty box", () => {
    const edit = editStub()
    render(<IssueCodeLinksCard links={[]} edit={edit} />)
    const menu = openAttach()
    fireEvent.click(within(menu).getByRole("button", { name: /^attach$/i }))
    expect(edit.attach).not.toHaveBeenCalled()
  })

  // THE failure. Its detail names the credential to add and the account label
  // to put on it — the entire fix. "Failed to attach link" would be a dead end
  // with the remedy thrown away.
  it("shows the server's own sentence when no credential can reach the host", async () => {
    const detail =
      'No ACTIVE GITHUB credential in this workspace can reach ghe.acme.internal. Add one, and for a self-hosted instance set its account label to "ghe.acme.internal" so it is matched by host.'
    const edit = editStub({ attach: vi.fn(async () => ({ ok: false as const, message: detail })) })
    render(<IssueCodeLinksCard links={[]} edit={edit} />)

    const menu = openAttach()
    fireEvent.change(within(menu).getByLabelText(/pull request url/i), {
      target: { value: "https://ghe.acme.internal/platform/gw/pull/12" },
    })
    fireEvent.click(within(menu).getByRole("button", { name: /^attach$/i }))

    const alert = await screen.findByTestId("code-link-attach-error")
    expect(alert).toHaveTextContent(/account label/i)
    expect(alert).toHaveTextContent(/ghe\.acme\.internal/)
    // Stays open, with the URL still in the box: the reader is one paste away
    // from retrying once they have added the credential.
    expect(screen.getByRole("dialog")).toBeInTheDocument()
    expect(within(menu).getByLabelText(/pull request url/i)).toHaveValue(
      "https://ghe.acme.internal/platform/gw/pull/12",
    )
  })

  it("shows blocked-host and already-linked the same way", async () => {
    const edit = editStub({
      attach: vi.fn(async () => ({
        ok: false as const,
        message: "https://github.com/acme/thing/pull/7 is already linked to this issue",
      })),
    })
    render(<IssueCodeLinksCard links={[]} edit={edit} />)
    const menu = openAttach()
    fireEvent.change(within(menu).getByLabelText(/pull request url/i), {
      target: { value: "https://github.com/acme/thing/pull/7" },
    })
    fireEvent.click(within(menu).getByRole("button", { name: /^attach$/i }))
    expect(await screen.findByTestId("code-link-attach-error")).toHaveTextContent(
      /already linked to this issue/,
    )
  })

  it("clears a previous failure when the next attempt starts", async () => {
    let fail = true
    const edit = editStub({
      attach: vi.fn(async () =>
        fail ? { ok: false as const, message: "unsupported url" } : { ok: true as const },
      ),
    })
    render(<IssueCodeLinksCard links={[]} edit={edit} />)
    const menu = openAttach()
    const box = within(menu).getByLabelText(/pull request url/i)
    fireEvent.change(box, { target: { value: "https://github.com/acme/thing" } })
    fireEvent.click(within(menu).getByRole("button", { name: /^attach$/i }))
    await screen.findByTestId("code-link-attach-error")

    fail = false
    fireEvent.change(box, { target: { value: "https://github.com/acme/thing/pull/7" } })
    fireEvent.click(within(menu).getByRole("button", { name: /^attach$/i }))
    await waitFor(() => expect(screen.queryByTestId("code-link-attach-error")).toBeNull())
  })
})

describe("IssueCodeLinksCard — removing and refreshing", () => {
  it("removes the row it names, without a dialog", async () => {
    const edit = editStub()
    render(<IssueCodeLinksCard links={[link()]} edit={edit} />)
    fireEvent.click(screen.getByRole("button", { name: /remove link to acme\/thing#7/i }))
    await waitFor(() => expect(edit.remove).toHaveBeenCalledWith("link-1"))
  })

  it("re-reads the row it names", async () => {
    const edit = editStub()
    render(<IssueCodeLinksCard links={[link()]} edit={edit} />)
    fireEvent.click(screen.getByRole("button", { name: /refresh acme\/thing#7/i }))
    await waitFor(() => expect(edit.refresh).toHaveBeenCalledWith("link-1"))
  })
})

/* ------------------------------------------------------------------ *
 *  Untrusted content                                                  *
 * ------------------------------------------------------------------ */

describe("IssueCodeLinksCard — the forge does not get to write markup", () => {
  let container: HTMLElement

  beforeEach(() => {
    container = document.createElement("div")
  })

  it("renders a pull-request title as text, never as HTML", () => {
    const hostile = '<img src=x onerror="alert(1)"><b>bold</b>'
    const { container: c } = render(<IssueCodeLinksCard links={[link({ title: hostile })]} />)
    container = c

    expect(container.querySelector("img")).toBeNull()
    expect(container.querySelector("b")).toBeNull()
    expect(container.querySelector("script")).toBeNull()
    // Present, and present as the literal characters somebody typed.
    expect(screen.getByText(hostile)).toBeInTheDocument()
  })

  // The description and comments on this page go through MarkdownContent. A
  // pull-request title must NOT — a forge-supplied string reaching a markdown
  // renderer is a forge-supplied string reaching a link factory.
  it("does not run a title through the markdown renderer", () => {
    const { container: c } = render(
      <IssueCodeLinksCard
        links={[link({ title: "[click me](javascript:alert(1)) and `code` and **bold**" })]}
      />,
    )
    container = c
    expect(screen.getByText(/\[click me\]\(javascript:alert\(1\)\)/)).toBeInTheDocument()
    expect(container.querySelector("strong")).toBeNull()
    expect(container.querySelector("code")).toBeNull()
    // The only anchor in the row is the one WE built, pointing at the stored
    // URL — never one the title conjured.
    const hrefs = Array.from(container.querySelectorAll("a")).map((a) => a.getAttribute("href"))
    expect(hrefs).toEqual(["https://github.com/acme/thing/pull/7"])
  })

  it("renders author and branch names as text", () => {
    const { container: c } = render(
      <IssueCodeLinksCard
        links={[
          link({
            author: '<script>alert("author")</script>',
            source_branch: "<iframe src=evil>",
            target_branch: "main",
          }),
        ]}
      />,
    )
    container = c
    expect(container.querySelector("script")).toBeNull()
    expect(container.querySelector("iframe")).toBeNull()
    expect(screen.getByText(/<iframe src=evil> → main/)).toBeInTheDocument()
  })

  it("renders a recorded sync error as text", () => {
    const { container: c } = render(
      <IssueCodeLinksCard links={[link({ last_sync_error: "<img src=x onerror=alert(1)>" })]} />,
    )
    container = c
    expect(container.querySelector("img")).toBeNull()
    expect(screen.getByTestId("code-link-stale")).toHaveTextContent("<img src=x onerror=alert(1)>")
  })

  // The server reconstructs the URL from parsed parts, so it cannot be a
  // javascript: URL today. This is the assertion that keeps that true if
  // anything else ever writes the column.
  it("refuses to put a non-http(s) URL in an href", () => {
    const hostileUrl = ["javascript", "alert(1)"].join(":")
    const { container: c } = render(<IssueCodeLinksCard links={[link({ url: hostileUrl })]} />)
    container = c
    expect(container.querySelector("a")).toBeNull()
    // The row still renders — the link is a fact about the issue even when it
    // cannot be opened.
    expect(screen.getByTestId("code-link-row")).toHaveTextContent("acme/thing#7")
    expect(screen.getByText("Add the widget")).toBeInTheDocument()
  })

  it("does not let a hostile state string reach the badge", () => {
    render(<IssueCodeLinksCard links={[link({ state: "<img src=x onerror=alert(1)>" })]} />)
    expect(screen.getByTestId("code-link-state")).toHaveTextContent("Unknown")
  })
})

/* ------------------------------------------------------------------ *
 *  Mounted by the issue detail                                        *
 * ------------------------------------------------------------------ */

// issue-detail-surface.test.tsx proves the wiring end to end. This proves the
// smaller thing that a refactor of the surface cannot: the CARD is on the
// page, and its write affordances are gated on the page being writable at all.
describe("IssueCardDetail — the pull-requests card is on the page", () => {
  const issue = {
    id: "cms20ikph011ab4683c02",
    workspace_id: "ws1",
    crew_id: "crew1",
    title: "Ship the widget",
    description: "",
    status: "IN_PROGRESS",
    identifier: "ENG-4",
    created_at: "2026-08-01T12:00:00Z",
    updated_at: "2026-08-04T10:00:00Z",
    labels: [],
    tasks: [],
  } as unknown as import("@/lib/types/mission").Mission

  function renderDetail(props: Record<string, unknown> = {}) {
    return render(
      <IssueCardDetail
        issue={issue}
        comments={[]}
        activities={[]}
        relations={[]}
        codeLinks={[link()]}
        {...props}
      />,
    )
  }

  it("renders the links it is handed", () => {
    renderDetail()
    expect(screen.getByTestId("issue-code-links")).toBeInTheDocument()
    expect(screen.getByText("Add the widget")).toBeInTheDocument()
  })

  // Handing over a `codeLinkEdit` is not enough: a card rendering every
  // property read-only must not carry a live delete.
  it("ignores codeLinkEdit on a read-only card", () => {
    renderDetail({ codeLinkEdit: editStub() })
    expect(screen.queryByRole("button", { name: /attach a pull request/i })).toBeNull()
    expect(screen.queryByRole("button", { name: /remove link to/i })).toBeNull()
  })
})
