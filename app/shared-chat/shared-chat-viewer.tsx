"use client"

import { useEffect, useRef, useState, type FormEvent } from "react"
import { Info, Link2Off, MessageSquare, MessageSquareLock, RefreshCw } from "lucide-react"

import { AUTH_BUTTON, AUTH_INPUT, AuthCard, AuthScreen } from "@/components/branding/auth-card"
import { PublicBar } from "@/components/branding/public-bar"
import { CreateSurfaceField, CreateSurfaceNotice } from "@/components/layout/create-surface"
import { PageEmpty, PageError, PageLoading } from "@/components/layout/page-state"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { withServerBase } from "@/lib/server-base"
import { cn } from "@/lib/utils"

type SharedMessage = { id: string; role: "user" | "assistant"; content: string; created_at: string }
type ViewState = "idle" | "loading" | "ready" | "error"
/**
 * What kind of failure, so each gets the right page state (Harbor 2): a share
 * that is not there is "unavailable", a temporary or malformed answer can be
 * retried, a transcript over the limit cannot.
 */
type ErrorKind = "unavailable" | "too-large" | "retry" | "failed"
class SharedChatViewError extends Error {
  constructor(message: string, readonly kind: ErrorKind = "retry") {
    super(message)
  }
}

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

function errorForStatus(status: number): SharedChatViewError {
  if (status === 404 || status === 401) return new SharedChatViewError("This share is unavailable. Check the ID and token, or ask for a new share.", "unavailable")
  if (status === 413) return new SharedChatViewError("This transcript is too large to display (over 16 MiB or 1,000 messages).", "too-large")
  if (status >= 500) return new SharedChatViewError("The transcript is temporarily unavailable. Try again.", "retry")
  return new SharedChatViewError("The transcript could not be opened.", "failed")
}

const SHARE_ID_RE = /^cshr_[0-9a-f]{32}$/
const SHARE_TOKEN_RE = /^cshr_[A-Za-z0-9_-]{43}$/

export function SharedChatViewer() {
  const [shareID, setShareID] = useState("")
  const [token, setToken] = useState("")
  const [messages, setMessages] = useState<SharedMessage[]>([])
  const [state, setState] = useState<ViewState>("idle")
  const [error, setError] = useState("")
  const [errorKind, setErrorKind] = useState<ErrorKind>("failed")
  // A malformed value belongs under its field, and is checked before any request.
  const [fieldErrors, setFieldErrors] = useState<{ id?: string; token?: string }>({})
  const pending = useRef<AbortController | null>(null)

  useEffect(() => () => pending.current?.abort(), [])

  function clear() {
    pending.current?.abort()
    pending.current = null
    setShareID("")
    setToken("")
    setMessages([])
    setError("")
    setFieldErrors({})
    setState("idle")
  }

  async function load(event?: FormEvent<HTMLFormElement>) {
    event?.preventDefault()
    pending.current?.abort()
    pending.current = null
    const id = shareID.trim()
    const secret = token.trim()
    const invalid = {
      id: SHARE_ID_RE.test(id) ? undefined : "Enter the complete share ID. It starts with cshr_ and has 32 more characters.",
      token: SHARE_TOKEN_RE.test(secret) ? undefined : "Enter the complete share token. It starts with cshr_.",
    }
    if (invalid.id || invalid.token) {
      setMessages([])
      setError("")
      setFieldErrors(invalid)
      setState("idle")
      return
    }
    setFieldErrors({})
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
      if (!response.ok) throw errorForStatus(response.status)
      const rows = parseMessages(await response.json())
      if (pending.current !== controller) return
      setMessages(rows)
      setState("ready")
    } catch (cause) {
      if (pending.current !== controller || controller.signal.aborted) return
      setMessages([])
      setError(cause instanceof SharedChatViewError ? cause.message : "The transcript could not be opened.")
      setErrorKind(cause instanceof SharedChatViewError ? cause.kind : "retry")
      setState("error")
    } finally {
      if (pending.current === controller) pending.current = null
    }
  }

  return (
    <div className="flex min-h-dvh flex-col bg-background text-foreground">
      <PublicBar
        crumb="Shared chat"
        actions={
          state === "ready" ? (
            <Button
              type="button"
              variant="ghost"
              size="icon"
              onClick={() => void load()}
              aria-label="Refresh"
              title="Refresh"
              className="coarse:size-[44px]"
            >
              <RefreshCw className="size-4" aria-hidden />
            </Button>
          ) : undefined
        }
      />
      <AuthScreen lockup={false} width="md" className="min-h-0 flex-1 bg-background md:pt-8 xl:justify-start xl:pt-8">
        <div className="flex flex-col gap-6">
          <AuthCard
            size="md"
            icon={MessageSquareLock}
            title="Read a shared chat"
            description="Enter the share ID and token you received. This view contains conversation text only."
            onSubmit={(event) => void load(event)}
            formProps={{ autoComplete: "off" }}
            actions={
              <>
                <Button type="button" variant="ghost" onClick={clear} className={AUTH_BUTTON}>
                  Clear
                </Button>
                <Button type="submit" disabled={state === "loading"} className={AUTH_BUTTON}>
                  {state === "loading" && <Spinner className="size-3.5" />}
                  {state === "loading" ? "Opening…" : "Open transcript"}
                </Button>
              </>
            }
          >
            <CreateSurfaceField label="Share ID" htmlFor="share-id" error={fieldErrors.id}>
              <Input
                id="share-id"
                value={shareID}
                onChange={(event) => setShareID(event.target.value)}
                autoCapitalize="none"
                autoCorrect="off"
                spellCheck={false}
                placeholder="cshr_…"
                aria-invalid={fieldErrors.id ? true : undefined}
                aria-describedby={fieldErrors.id ? "share-id-error" : undefined}
                className={cn("font-mono", AUTH_INPUT)}
              />
            </CreateSurfaceField>
            <CreateSurfaceField label="Share token" htmlFor="share-token" error={fieldErrors.token}>
              <Input
                id="share-token"
                type="password"
                value={token}
                onChange={(event) => setToken(event.target.value)}
                autoComplete="off"
                autoCapitalize="none"
                autoCorrect="off"
                spellCheck={false}
                aria-invalid={fieldErrors.token ? true : undefined}
                aria-describedby={fieldErrors.token ? "share-token-error" : undefined}
                className={cn("font-mono", AUTH_INPUT)}
              />
            </CreateSurfaceField>
          </AuthCard>

          {state === "loading" && <PageLoading rows={4} className="p-0" />}

          {state === "error" &&
            (errorKind === "unavailable" ? (
              // One answer for a wrong ID, a wrong token, an expired and a
              // revoked share: the server does not tell them apart, so neither
              // does this.
              <div role="alert">
                <PageEmpty
                  icon={Link2Off}
                  title="This share is not available"
                  description={error}
                  className="p-0"
                />
              </div>
            ) : (
              <PageError
                title={errorKind === "too-large" ? "This transcript is too large to show" : "Couldn’t open the transcript"}
                description={error}
                onRetry={errorKind === "too-large" ? undefined : () => void load()}
                className="p-0"
              />
            ))}

          {state === "ready" && (
            <section aria-label="Shared transcript" className="flex flex-col gap-4">
              <CreateSurfaceNotice tone="info" icon={Info}>
                This share may show future messages until it expires or is revoked. It cannot run the agent or open files.
              </CreateSurfaceNotice>
              {messages.length === 0 ? (
                <PageEmpty icon={MessageSquare} title="No text messages yet" description="New messages appear here when you refresh." className="p-0" />
              ) : (
                messages.map((message) => (
                  <article key={message.id} className="rounded-xl border border-border bg-card p-4">
                    <p className="mb-2 text-label font-semibold uppercase tracking-wide text-muted-foreground">{message.role === "user" ? "Person" : "Agent"}</p>
                    <p className="whitespace-pre-wrap break-words text-body">{message.content}</p>
                  </article>
                ))
              )}
            </section>
          )}
        </div>
      </AuthScreen>
    </div>
  )
}
