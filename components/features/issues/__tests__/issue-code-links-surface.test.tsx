// Git links, wired.
//
// The card is a renderer; this file is about the half that talks to the
// server. It asserts the contract in internal/api/issue_code_links.go from the
// browser's side: the four routes are the four routes, and the RFC 7807
// `detail` — the only part of a failure that names a remedy — survives the
// trip to the popover instead of being replaced by a sentence of ours.
//
// It renders IssueDetailSurface rather than the card, which is also how it
// covers both places an issue opens: /issues/<identifier> and
// /issues?issue=<identifier> render this one component (#1766), so wiring it
// here is wiring it on both.

import * as React from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"

vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {} }))
vi.mock("@/hooks/use-pipelines", () => ({
  usePipelines: () => ({ pipelines: [], loading: false }),
}))
vi.mock("@/components/features/issues/tiptap-editor", () => ({
  TiptapEditor: () => <div data-testid="tiptap" />,
}))
vi.mock("@/components/features/activity/run-activity-timeline", () => ({
  RunActivityTimeline: () => null,
  RUN_WORK_ENTRY_TYPES: ["exec"],
}))
vi.mock("next/link", () => ({
  default: ({ children, href }: { children: React.ReactNode; href: string }) => (
    <a href={href}>{children}</a>
  ),
}))

const toasted = vi.hoisted(() => ({ errors: [] as string[], successes: [] as string[] }))
vi.mock("sonner", () => ({
  toast: {
    error: (m: string) => toasted.errors.push(m),
    success: (m: string) => toasted.successes.push(m),
  },
}))

import { IssueDetailSurface } from "../issue-detail-surface"

const ISSUE = {
  id: "id-ENG-4",
  identifier: "ENG-4",
  title: "Ship the widget",
  description: "",
  status: "IN_PROGRESS",
  crew_id: "crew-1",
  created_at: "2026-08-01T12:00:00Z",
  updated_at: "2026-08-01T12:00:00Z",
  labels: [],
}

const LINK = {
  id: "link-1",
  mission_id: "id-ENG-4",
  workspace_id: "ws1",
  provider: "GITHUB",
  host: "github.com",
  owner: "acme",
  repo: "thing",
  number: 7,
  kind: "PULL_REQUEST",
  url: "https://github.com/acme/thing/pull/7",
  title: "Add the widget",
  state: "MERGED",
  author: "octocat",
  source_branch: "feat/widget",
  target_branch: "main",
  remote_created_at: null,
  remote_updated_at: null,
  remote_merged_at: "2026-08-03T10:00:00Z",
  remote_closed_at: null,
  credential_id: "cred1",
  last_synced_at: "2026-08-04T18:31:03Z",
  last_sync_error: null,
  created_at: "2026-08-04T18:31:03Z",
  updated_at: "2026-08-04T18:31:03Z",
}

interface Call {
  url: string
  method: string
  body: string | undefined
}

/**
 * A server that answers the issue and its sub-resources, and lets one test
 * decide what a write replies.
 */
function stubServer(opts: {
  links?: unknown[]
  write?: { status: number; body: unknown }
} = {}) {
  const calls: Call[] = []
  let links = opts.links ?? []

  global.fetch = vi.fn((input: unknown, init?: RequestInit) => {
    const url = String(input)
    const method = (init?.method ?? "GET").toUpperCase()
    calls.push({ url, method, body: init?.body as string | undefined })

    const json = (body: unknown, status = 200) =>
      Promise.resolve({
        ok: status < 400,
        status,
        json: () => Promise.resolve(body),
      } as Response)

    if (url.includes("/code-links")) {
      if (method === "GET") return json(links)
      const reply = opts.write ?? { status: 200, body: { status: "ok" } }
      if (reply.status < 400 && method === "DELETE") links = []
      return json(reply.body, reply.status)
    }
    if (/\/api\/v1\/issues\/ENG-4\?/.test(url)) return json(ISSUE)
    return json([])
  }) as unknown as typeof fetch

  return { calls }
}

function openAttach() {
  fireEvent.click(screen.getByRole("button", { name: /attach a pull request/i }))
  return screen.getByRole("dialog")
}

beforeEach(() => {
  toasted.errors.length = 0
  toasted.successes.length = 0
})

describe("IssueDetailSurface — git links", () => {
  it("reads the links from the crew/issue route and renders them", async () => {
    const { calls } = stubServer({ links: [LINK] })
    render(<IssueDetailSurface workspaceId="ws1" identifier="ENG-4" />)

    expect(await screen.findByTestId("issue-code-links")).toBeInTheDocument()
    await waitFor(() => expect(screen.getByTestId("code-link-state")).toHaveTextContent("Merged"))
    expect(screen.getByText("Add the widget")).toBeInTheDocument()

    expect(
      calls.some(
        (c) =>
          c.method === "GET" &&
          c.url.includes("/api/v1/crews/crew-1/issues/ENG-4/code-links") &&
          c.url.includes("workspace_id=ws1"),
      ),
    ).toBe(true)
  })

  it("says plainly when nothing is attached", async () => {
    stubServer({ links: [] })
    render(<IssueDetailSurface workspaceId="ws1" identifier="ENG-4" />)
    expect(await screen.findByText(/no pull request attached/i)).toBeInTheDocument()
  })

  it("posts a pasted URL to the attach route", async () => {
    const { calls } = stubServer({ links: [], write: { status: 201, body: LINK } })
    render(<IssueDetailSurface workspaceId="ws1" identifier="ENG-4" />)
    await screen.findByTestId("issue-code-links")

    const menu = openAttach()
    fireEvent.change(within(menu).getByLabelText(/pull request url/i), {
      target: { value: "https://github.com/acme/thing/pull/7" },
    })
    fireEvent.click(within(menu).getByRole("button", { name: /^attach$/i }))

    await waitFor(() => {
      const post = calls.find((c) => c.method === "POST" && c.url.includes("/code-links"))
      expect(post).toBeTruthy()
      expect(post!.url).toContain("/api/v1/crews/crew-1/issues/ENG-4/code-links")
      expect(JSON.parse(post!.body!)).toEqual({ url: "https://github.com/acme/thing/pull/7" })
    })
  })

  // The one that matters. 412 `no-credential` — the detail names the
  // credential to add AND the account label to put on it. If this test ever
  // goes green against "Failed to attach link", the feature has lost the only
  // sentence that tells a reader what to do.
  it("surfaces the 412's own sentence, not a sentence of ours", async () => {
    const detail =
      'No ACTIVE GITHUB credential in this workspace can reach ghe.acme.internal. Add one, and for a self-hosted instance set its account label to "ghe.acme.internal" so it is matched by host.'
    stubServer({
      links: [],
      write: {
        status: 412,
        body: {
          type: "https://crewship.ai/problems/code-link/no-credential",
          title: "Precondition Failed",
          status: 412,
          code: "no-credential",
          detail,
        },
      },
    })
    render(<IssueDetailSurface workspaceId="ws1" identifier="ENG-4" />)
    await screen.findByTestId("issue-code-links")

    const menu = openAttach()
    fireEvent.change(within(menu).getByLabelText(/pull request url/i), {
      target: { value: "https://ghe.acme.internal/platform/gw/pull/12" },
    })
    fireEvent.click(within(menu).getByRole("button", { name: /^attach$/i }))

    const alert = await screen.findByTestId("code-link-attach-error")
    expect(alert).toHaveTextContent(detail)
  })

  it("surfaces blocked-host the same way", async () => {
    stubServer({
      links: [],
      write: {
        status: 422,
        body: {
          code: "blocked-host",
          detail: "blocked host: 10.0.0.5 is a private address; see git_links.allow_private_hosts",
        },
      },
    })
    render(<IssueDetailSurface workspaceId="ws1" identifier="ENG-4" />)
    await screen.findByTestId("issue-code-links")

    const menu = openAttach()
    fireEvent.change(within(menu).getByLabelText(/pull request url/i), {
      target: { value: "https://10.0.0.5/acme/thing/pull/7" },
    })
    fireEvent.click(within(menu).getByRole("button", { name: /^attach$/i }))

    expect(await screen.findByTestId("code-link-attach-error")).toHaveTextContent(
      /allow_private_hosts/,
    )
  })

  it("deletes through the link route", async () => {
    const { calls } = stubServer({ links: [LINK] })
    render(<IssueDetailSurface workspaceId="ws1" identifier="ENG-4" />)
    await screen.findByTestId("issue-code-links")

    fireEvent.click(await screen.findByRole("button", { name: /remove link to acme\/thing#7/i }))
    await waitFor(() => {
      const del = calls.find((c) => c.method === "DELETE")
      expect(del?.url).toContain("/api/v1/crews/crew-1/issues/ENG-4/code-links/link-1")
    })
  })

  it("reports a failed removal with the server's sentence", async () => {
    stubServer({
      links: [LINK],
      write: { status: 404, body: { code: "link-not-found", detail: "Code link not found" } },
    })
    render(<IssueDetailSurface workspaceId="ws1" identifier="ENG-4" />)
    await screen.findByTestId("issue-code-links")

    fireEvent.click(await screen.findByRole("button", { name: /remove link to acme\/thing#7/i }))
    await waitFor(() => expect(toasted.errors).toContain("Code link not found"))
  })

  it("refreshes through the refresh route", async () => {
    const { calls } = stubServer({ links: [LINK] })
    render(<IssueDetailSurface workspaceId="ws1" identifier="ENG-4" />)
    await screen.findByTestId("issue-code-links")

    fireEvent.click(await screen.findByRole("button", { name: /refresh acme\/thing#7/i }))
    await waitFor(() => {
      const post = calls.find((c) => c.method === "POST" && c.url.includes("/refresh"))
      expect(post?.url).toContain("/api/v1/crews/crew-1/issues/ENG-4/code-links/link-1/refresh")
    })
  })

  // A failed refresh writes last_sync_error on the row server-side and keeps
  // the state it had. Re-reading the list is what puts that on the card —
  // without it the reason lives only in a toast that is gone in five seconds.
  it("re-reads the list even when the refresh failed, so the reason lands on the row", async () => {
    const { calls } = stubServer({
      links: [LINK],
      write: {
        status: 502,
        body: { code: "credential-rejected", detail: "github.com answered 401" },
      },
    })
    render(<IssueDetailSurface workspaceId="ws1" identifier="ENG-4" />)
    await screen.findByTestId("issue-code-links")
    const before = calls.filter((c) => c.method === "GET" && c.url.includes("/code-links")).length

    fireEvent.click(await screen.findByRole("button", { name: /refresh acme\/thing#7/i }))
    await waitFor(() => expect(toasted.errors).toContain("github.com answered 401"))
    await waitFor(() =>
      expect(
        calls.filter((c) => c.method === "GET" && c.url.includes("/code-links")).length,
      ).toBeGreaterThan(before),
    )
  })

  it("offers no write affordance to a read-only reader", async () => {
    stubServer({ links: [LINK] })
    render(<IssueDetailSurface workspaceId="ws1" identifier="ENG-4" editable={false} />)
    await screen.findByTestId("issue-code-links")

    // The links themselves still read.
    expect(await screen.findByText("Add the widget")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /attach a pull request/i })).toBeNull()
    expect(screen.queryByRole("button", { name: /remove link to/i })).toBeNull()
  })
})
