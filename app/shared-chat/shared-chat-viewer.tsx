"use client"

import { useEffect, useRef, useState, type FormEvent } from "react"

import { withServerBase } from "@/lib/server-base"

type SharedMessage = { id: string; role: "user" | "assistant"; content: string; created_at: string }
type ViewState = "idle" | "loading" | "ready" | "error"
class SharedChatViewError extends Error {}

function parseMessages(value: unknown): SharedMessage[] {
  if (!value || typeof value !== "object" || !Array.isArray((value as { messages?: unknown }).messages)) {
    throw new SharedChatViewError("The transcript response was invalid.")
  }
  return (value as { messages: unknown[] }).messages.map((item) => {
    if (!item || typeof item !== "object") throw new SharedChatViewError("The transcript response was invalid.")
    const row = item as Record<string, unknown>
    if (typeof row.id !== "string" || (row.role !== "user" && row.role !== "assistant") ||
      typeof row.content !== "string" || typeof row.created_at !== "string") {
      throw new SharedChatViewError("The transcript response was invalid.")
    }
    return { id: row.id, role: row.role, content: row.content, created_at: row.created_at }
  })
}

function errorForStatus(status: number): string {
  if (status === 404 || status === 401) return "This share is unavailable. Check the ID and token, or ask for a new share."
  if (status === 413) return "This transcript is too large to display (over 16 MiB or 1,000 messages)."
  if (status >= 500) return "The transcript is temporarily unavailable. Try again."
  return "The transcript could not be opened."
}

export function SharedChatViewer() {
  const [shareID, setShareID] = useState("")
  const [token, setToken] = useState("")
  const [messages, setMessages] = useState<SharedMessage[]>([])
  const [state, setState] = useState<ViewState>("idle")
  const [error, setError] = useState("")
  const pending = useRef<AbortController | null>(null)

  useEffect(() => () => pending.current?.abort(), [])

  function clear() {
    pending.current?.abort()
    pending.current = null
    setShareID("")
    setToken("")
    setMessages([])
    setError("")
    setState("idle")
  }

  async function load(event?: FormEvent<HTMLFormElement>) {
    event?.preventDefault()
    pending.current?.abort()
    pending.current = null
    const id = shareID.trim()
    const secret = token.trim()
    if (!/^cshr_[0-9a-f]{32}$/.test(id) || !/^cshr_[A-Za-z0-9_-]{43}$/.test(secret)) {
      setMessages([])
      setError("Enter the complete share ID and token.")
      setState("error")
      return
    }
    const controller = new AbortController()
    pending.current = controller
    setMessages([])
    setError("")
    setState("loading")
    try {
      // apiFetch/serverFetch attach the user's normal session and may refresh
      // it on 401. This public capability uses neither behavior.
      const response = await fetch(withServerBase(`/api/v1/shared-chats/${encodeURIComponent(id)}/messages`), {
        method: "GET",
        headers: { Authorization: `Bearer ${secret}`, Accept: "application/json" },
        credentials: "omit",
        cache: "no-store",
        redirect: "error",
        referrerPolicy: "no-referrer",
        signal: controller.signal,
      })
      if (!response.ok) throw new SharedChatViewError(errorForStatus(response.status))
      const rows = parseMessages(await response.json())
      if (pending.current !== controller) return
      setMessages(rows)
      setState("ready")
    } catch (cause) {
      if (pending.current !== controller || controller.signal.aborted) return
      setMessages([])
      setError(cause instanceof SharedChatViewError ? cause.message : "The transcript could not be opened.")
      setState("error")
    } finally {
      if (pending.current === controller) pending.current = null
    }
  }

  return (
    <main className="min-h-screen bg-background px-4 py-10 text-foreground sm:px-6">
      <div className="mx-auto max-w-2xl space-y-6">
        <header className="space-y-2">
          <p className="text-sm font-semibold text-primary">Crewship</p>
          <h1 className="text-2xl font-semibold">Read a shared chat</h1>
          <p className="text-sm text-muted-foreground">Enter the share ID and token you received. This view contains conversation text only.</p>
        </header>
        <form onSubmit={(event) => void load(event)} autoComplete="off" className="space-y-4 rounded-xl border border-border bg-card p-5">
          <div className="space-y-2">
            <label htmlFor="share-id" className="block text-sm font-medium">Share ID</label>
            <input id="share-id" value={shareID} onChange={(event) => setShareID(event.target.value)} autoCapitalize="none" autoCorrect="off" spellCheck={false} className="w-full rounded-md border border-input bg-background px-3 py-2 text-base" />
          </div>
          <div className="space-y-2">
            <label htmlFor="share-token" className="block text-sm font-medium">Share token</label>
            <input id="share-token" type="password" value={token} onChange={(event) => setToken(event.target.value)} autoComplete="off" autoCapitalize="none" autoCorrect="off" spellCheck={false} className="w-full rounded-md border border-input bg-background px-3 py-2 text-base" />
          </div>
          <div className="flex flex-wrap gap-3">
            <button type="submit" disabled={state === "loading"} className="coarse:min-h-12 rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground disabled:opacity-60">{state === "loading" ? "Opening…" : "Open transcript"}</button>
            <button type="button" onClick={clear} className="coarse:min-h-12 rounded-md border border-border px-4 py-2 text-sm font-medium">Clear</button>
            {state === "ready" && <button type="button" onClick={() => void load()} className="coarse:min-h-12 rounded-md border border-border px-4 py-2 text-sm font-medium">Refresh</button>}
          </div>
        </form>
        {state === "error" && <p role="alert" className="rounded-md border border-destructive/40 p-4 text-sm text-destructive">{error}</p>}
        {state === "ready" && (
          <section aria-label="Shared transcript" className="space-y-4">
            <p className="text-sm text-muted-foreground">This share may show future messages until it expires or is revoked. It cannot run the agent or open files.</p>
            {messages.length === 0 ? <p className="text-sm text-muted-foreground">No text messages yet.</p> : messages.map((message) => (
              <article key={message.id} className="rounded-xl border border-border bg-card p-4">
                <p className="mb-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">{message.role === "user" ? "Person" : "Agent"}</p>
                <p className="whitespace-pre-wrap break-words text-sm">{message.content}</p>
              </article>
            ))}
          </section>
        )}
      </div>
    </main>
  )
}
