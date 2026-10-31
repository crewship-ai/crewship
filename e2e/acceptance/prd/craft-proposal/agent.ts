// Drives the seeded agent through the normal product path for native
// documents: the owner writes the instruction in a chat with the agent, then
// starts an explicit whole-document native task bound to that message (the
// API behind `crewship craft task start --whole-document`).
import { expect, type APIRequestContext, type Page } from "@playwright/test"
import type { NativePage, TaskStatus } from "./native"

export interface SeededAgent {
  id: string
  slug: string
  name: string
  workspaceId: string
}

export async function findAgent(api: APIRequestContext, slug: string, workspaceId: string): Promise<SeededAgent> {
  const res = await api.get(`/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}`)
  expect(res.status(), "list agents in PRD_AGENT_WORKSPACE_ID").toBe(200)
  const body = (await res.json()) as unknown
  const list = (Array.isArray(body) ? body : ((body as { agents?: unknown[] }).agents ?? [])) as Array<{ id: string; slug?: string; name?: string }>
  const agent = list.find((a) => a.slug === slug)
  expect(agent, `agent ${slug} must exist in PRD_AGENT_WORKSPACE_ID`).toBeTruthy()
  return { id: agent!.id, slug, name: agent!.name ?? slug, workspaceId }
}

export async function openChat(api: APIRequestContext, agent: SeededAgent): Promise<string> {
  const res = await api.post(`/api/v1/agents/${encodeURIComponent(agent.id)}/chats?workspace_id=${encodeURIComponent(agent.workspaceId)}`, {
    data: { origin: "UI" },
  })
  expect(res.status(), "create chat with the agent").toBe(201)
  return ((await res.json()) as { id: string }).id
}

/**
 * Sends the owner's instruction over the product WebSocket (as the chat UI
 * does) and returns the server-assigned message id the native task binds to.
 */
export async function sendInstruction(
  page: Page,
  api: APIRequestContext,
  agent: SeededAgent,
  chatId: string,
  ownerUserId: string,
  instruction: string,
): Promise<{ messageId: string; assistantSeen: boolean; ackError: boolean }> {
  const issued = await api.get(`/api/v1/ws-token?workspace_id=${encodeURIComponent(agent.workspaceId)}`)
  expect(issued.status(), "ws-token").toBe(200)
  const ticket = ((await issued.json()) as { token: string }).token
  const r = await page.evaluate(
    ({ ticket, chatId, agentId, instruction, ownerUserId }) =>
      new Promise<{
        messageId?: string
        assistantSeen: boolean
        ackError: boolean
      }>((resolve, reject) => {
        let messageId: string | undefined
        let assistantSeen = false
        let settled = false
        let ackError = false
        const socket = new WebSocket(location.origin.replace(/^http/, "ws") + "/ws")
        const timer = setTimeout(() => finish(new Error("chat ack timeout")), 240_000)
        function finish(e?: Error) {
          if (settled) return
          settled = true
          clearTimeout(timer)
          socket.close()
          if (e) reject(e)
          else resolve({ messageId, assistantSeen, ackError })
        }
        socket.onopen = () => {
          socket.send(JSON.stringify({ type: "auth", token: ticket }))
          socket.send(JSON.stringify({ type: "subscribe", channel: "session:" + chatId }))
          socket.send(
            JSON.stringify({
              type: "send_message",
              channel: "agent:" + agentId,
              payload: {
                session_id: chatId,
                content: instruction,
                max_turns: 2,
              },
            }),
          )
        }
        socket.onerror = () => finish(new Error("ws error"))
        socket.onclose = () => {
          if (!settled) finish(new Error("ws closed early"))
        }
        socket.onmessage = (ev) => {
          let f: { type?: string; payload?: unknown }
          try {
            f = JSON.parse(String(ev.data))
          } catch {
            return
          }
          if (f.type === "error" || f.type === "session_revoked") return finish(new Error("chat refused " + f.type))
          if (f.type !== "chat_event") return
          const e = (typeof f.payload === "string" ? JSON.parse(f.payload) : f.payload) as {
            type?: string
            content?: unknown
            metadata?: { author_user_id?: string; message_id?: string }
          } | null
          if (!e) return
          if (e.type === "user_message" && e.content === instruction && e.metadata?.author_user_id === ownerUserId) messageId = e.metadata.message_id
          if (e.type === "text" && typeof e.content === "string" && e.content.trim()) assistantSeen = true
          if (e.type === "error") {
            if (messageId) {
              ackError = true
              return finish()
            }
            return finish(new Error("chat run error"))
          }
          if (e.type === "done" && messageId) finish()
        }
      }),
    { ticket, chatId, agentId: agent.id, instruction, ownerUserId },
  )
  expect(r.messageId, "the chat must echo the owner's message id").toBeTruthy()
  return {
    messageId: r.messageId!,
    assistantSeen: r.assistantSeen,
    ackError: r.ackError,
  }
}

const TERMINAL = new Set(["succeeded", "failed", "cancelled", "expired"])

export async function waitForTask(doc: NativePage, workId: string, timeoutMs = 10 * 60_000): Promise<TaskStatus> {
  const deadline = Date.now() + timeoutMs
  let last: TaskStatus = {}
  while (Date.now() < deadline) {
    const r = await doc.task(workId)
    if (r.status === 200) {
      last = r.body
      if ((last.state && TERMINAL.has(last.state) && last.operation_inflight === false) || last.state === "needs_reconciliation") return last
    }
    await new Promise((res) => setTimeout(res, 3000))
  }
  throw new Error(`native task ${workId} did not finish in ${timeoutMs / 1000}s (last state ${last.state ?? "unknown"})`)
}

/** Strips anything token-like before a server object becomes evidence. */
export function redact<T>(o: T): T {
  return JSON.parse(JSON.stringify(o, (k, v) => (/token|password|secret|setup_url|cookie/i.test(k) ? "[REDACTED]" : v))) as T
}

const PREFIX =
  "Úloha pro nativní dokument (whole-document task přes nástroje crewship-craft; v běžném chatu stačí jednou větou potvrdit, úpravu provede samostatná nativní úloha). " +
  "Postup: craft_acquire, craft_read pro aktuální version, pak úprava s generation z acquire, base_version z read a novým request_id; po potvrzeném receiptu craft_release. "

export function wordInstruction(marker: string): string {
  const commands = [
    { command: "caret.docEnd", params: {} },
    { command: "text.insert", params: { text: " " + marker } },
  ]
  return PREFIX + `Přidej na konec dokumentu text " ${marker}" přesně tímto balíkem příkazů: ${JSON.stringify(commands)}. Nic jiného neměň.`
}

export function sheetInstruction(cell: string, marker: string): string {
  return (
    PREFIX +
    `Nastav buňku ${cell} na text "${marker}" přesně tímto balíkem příkazů: ${JSON.stringify([{ command: "cell.set", params: { cell, input: marker } }])}. Nic jiného neměň.`
  )
}
