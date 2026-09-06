import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"
import LegacyInboxPage from "@/app/(dashboard)/inbox-v2/page"
import { entryIdentity, filterInboxEntries } from "../inbox-entry-identity"
import { EMPTY_INBOX_V2_FILTERS, inboxEntry, selectEntry } from "../inbox-v2-derive"
import { InboxTriage } from "../inbox-v2-triage"
import type { InboxLookup } from "../inbox-v2-types"
import type { InboxItem } from "@/hooks/use-inbox"

const replace = vi.hoisted(() => vi.fn())
vi.mock("next/navigation", () => ({ useRouter: () => ({ replace }) }))
vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => <span /> }))
afterEach(() => { cleanup(); vi.clearAllMocks(); window.history.replaceState({}, "", "/") })
const crew = { id: "crew-eng", name: "Engineering", slug: "engineering", color: "blue", icon: "terminal" }
const agent = { id: "sam-id", name: "Sam Developer", slug: "sam", crew: { name: crew.name, slug: crew.slug } }
const lookup: InboxLookup = { crewById: new Map([[crew.id, crew]]), agentById: new Map([[agent.id, agent]]), agentBySlug: new Map([[agent.slug, agent]]), ready: true }
const entry = inboxEntry({ id: "item-1", workspace_id: "ws", kind: "message", source_id: "run-1", title: "Work ready", state: "unread", priority: "low", blocking: false, sender_type: "agent", sender_name: "sam", created_at: "2026-09-06T10:00:00Z", updated_at: "2026-09-06T10:00:00Z", payload: {} } as InboxItem)

describe("canonical client inbox", () => {
  it("redirects legacy URLs without losing the selected item, filter or hash", () => {
    window.history.replaceState({}, "", "/inbox-v2?item=a%2Fb&agent=sam#evidence")
    render(<LegacyInboxPage />)
    expect(replace).toHaveBeenCalledWith("/inbox?item=a%2Fb&agent=sam#evidence")
  })
  it("filters and searches by the crew and agent names actually shown", () => {
    expect(entryIdentity(entry, lookup).crew?.icon).toBe("terminal")
    expect(filterInboxEntries([entry], { ...EMPTY_INBOX_V2_FILTERS, crew: crew.id, search: "Engineering" }, lookup)).toEqual([entry])
    expect(filterInboxEntries([entry], { ...EMPTY_INBOX_V2_FILTERS, search: "Sam Developer" }, lookup)).toEqual([entry])
    expect(filterInboxEntries([entry], { ...EMPTY_INBOX_V2_FILTERS, crew: "another-crew" }, lookup)).toEqual([])
  })
  it("uses the issue assignee, never the crew lead, for a review notification", () => {
    const mission = { id: "issue-1", crew_id: crew.id, lead_agent_id: "lead-id" } as import("@/lib/types/mission").Mission
    const issue = { ...mission, assignee_type: "agent", assignee_id: agent.id } as import("@/lib/types/mission").Mission
    const notification = { ...entry, inboxItem: { ...entry.inboxItem!, sender_type: "system", sender_name: "Mission engine", payload: { mission_id: mission.id } } }
    const withIssue = { ...lookup, missionById: new Map([[mission.id, mission]]), issueById: new Map([[issue.id, issue]]) }
    expect(entryIdentity(notification, withIssue).agent?.id).toBe(agent.id)
    expect(entryIdentity(notification, { ...withIssue, issueById: new Map() }).agent).toBeNull()
  })
  it("does not replace an explicit unknown crew with an agent's current crew", () => {
    const historical = { ...entry, inboxItem: { ...entry.inboxItem!, payload: { crew_id: "deleted-crew" } } }
    expect(entryIdentity(historical, lookup).crew).toBeUndefined()
  })
  it("can restore composite group/mission keys but never substitutes a missing decision", () => {
    const group = { ...entry, key: "group:curator:ops" }
    expect(selectEntry([group], "request:group:curator:ops")).toBe(group)
    expect(selectEntry([entry], "request:missing")).toBeNull()
  })
  it("does not claim all caught up when a source is unavailable", () => {
    render(<InboxTriage action={[]} updates={[]} history={[]} lookup={lookup} live={false} incomplete onOpen={() => {}} onCrew={() => {}} />)
    expect(screen.queryByText("All caught up")).toBeNull()
    expect(screen.getByText("Partial information")).toBeTruthy()
  })
})
