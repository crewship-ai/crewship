import type { SoundCandidate } from "@/lib/notification-sound-coordinator"

interface Conversation { id: string; workspace_id: string; muted: boolean; last_read_sequence: number }
interface Message { id: string; sequence: number; author_user_id?: string; author_agent_id?: string; kind?: string; source_kind?: string; created_at: string }
interface InboxRow { id: string; workspace_id: string; kind: string; state: string; priority: string; blocking: boolean; source_missing?: boolean; created_at: string; sender_type?: string; payload?: { chat_id?: string; replied_at?: string; conversation_id?: string } }
export interface SoundControllerDependencies {
  get: <T>(path: string, signal: AbortSignal) => Promise<T>
  enabled: () => boolean
  deliver: (candidate: SoundCandidate, stillCurrent: () => boolean) => Promise<boolean>
  now?: () => number
}

/** WS frames only invalidate. Audible decisions always use the authorized API. */
export class NotificationSoundController {
  private abort = new AbortController()
  private since: number
  private generation = 0
  private pending = new Set<string>()
  private dirty = new Set<string>()
  private processed = new Map<string, number>()
  private now: () => number
  constructor(private workspace: string, private user: string, private deps: SoundControllerDependencies) {
    this.now = deps.now ?? Date.now
    this.since = this.now()
  }
  reset() {
    this.abort.abort()
    this.abort = new AbortController()
    this.since = this.now()
    this.generation++
    this.pending.clear()
    this.dirty.clear()
    this.processed.clear()
  }
  dispose() { this.reset(); this.abort.abort() }
  private fresh(stamp: string) {
    const time = Date.parse(stamp)
    return Number.isFinite(time) && time > this.since && time <= this.now() + 5000 && this.now() - time < 60000
  }
  async handle(type: string, payload: Record<string, unknown> = {}) {
    if (type === "realtime.reconnected") { this.reset(); return }
    if (this.abort.signal.aborted || !this.deps.enabled()) return
    if (payload.workspace_id && payload.workspace_id !== this.workspace) return
    const id = typeof payload.conversation_id === "string" ? payload.conversation_id : ""
    const chat = type === "conversation.updated"
    // The inbox projection of a chat is deliberately not a second sound source.
    if ((chat && !id) || (!chat && id)) return
    const key = chat ? "chat:" + id : "inbox"
    if (this.pending.has(key)) { this.dirty.add(key); return }
    this.pending.add(key)
    const generation = this.generation
    const signal = this.abort.signal
    const current = () => !signal.aborted && generation === this.generation && this.deps.enabled()
    const get = <T>(path: string) => this.deps.get<T>(path + (path.includes("?") ? "&" : "?") + "workspace_id=" + encodeURIComponent(this.workspace), signal)
    try {
      if (chat) {
        const room = await get<Conversation>(`conversations/${encodeURIComponent(id)}`)
        if (!current() || room.workspace_id !== this.workspace || room.muted) return
        const { messages } = await get<{ messages: Message[] }>(`conversations/${encodeURIComponent(id)}/messages?limit=50`)
        const eligible = messages.filter(m => m.kind === "message" && !!m.author_user_id && m.author_user_id !== this.user && !m.author_agent_id && m.source_kind !== "activity" && m.sequence > room.last_read_sequence && this.fresh(m.created_at))
        // Collapse a burst into one cue; old messages are never queued for later.
        const newest = eligible.sort((a, b) => b.sequence - a.sequence)[0]
        if (newest && current()) await this.deliver({ key: "message:" + id + ":" + newest.id, category: "chat", conversation: id }, current)
      } else {
        const { rows } = await get<{ rows: InboxRow[] }>("inbox?state=unread&limit=100")
        const candidates = rows.flatMap<{ stamp: string; candidate: SoundCandidate }>(row => {
          if (row.workspace_id !== this.workspace || row.state !== "unread" || row.source_missing) return []
          const reply = row.payload
          if (row.kind === "message") {
            // Agent DMs use one aggregate Inbox row across replies. The persisted
            // reply timestamp, also carried by live done, identifies an occurrence.
            if (row.sender_type !== "agent" || !reply?.chat_id || reply.conversation_id || !reply.replied_at || !this.fresh(reply.replied_at)) return []
            return [{ stamp: reply.replied_at, candidate: { key: `agent-reply:${reply.chat_id}:${Date.parse(reply.replied_at)}`, category: "chat" as const } }]
          }
          if (!(row.blocking || row.priority === "high" || row.priority === "urgent") || !this.fresh(row.created_at)) return []
          return [{ stamp: row.created_at, candidate: { key: "inbox:" + row.id, category: "inbox" as const } }]
        })
        const newest = candidates.sort((a, b) => Date.parse(b.stamp) - Date.parse(a.stamp))[0]
        if (newest && current()) await this.deliver(newest.candidate, current)
      }
    } catch { /* A failed or revoked read must never produce an alert. */ }
    finally {
      if (generation === this.generation) {
        this.pending.delete(key)
        if (this.dirty.delete(key)) void this.handle(type, payload)
      }
    }
  }
  private async deliver(candidate: SoundCandidate, current: () => boolean) {
    if (this.processed.has(candidate.key)) return
    this.processed.set(candidate.key, this.now())
    if (this.processed.size > 256) this.processed.delete(this.processed.keys().next().value!)
    await this.deps.deliver(candidate, current)
  }
}
