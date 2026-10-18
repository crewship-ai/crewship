"use client"

import { useEffect, useRef, useState, type FormEvent } from "react"
import { Bot, Info, MessageSquare, MessageSquareLock, RefreshCw } from "lucide-react"

import { AUTH_BUTTON, AUTH_INPUT, AuthCard } from "@/components/branding/auth-card"
import { PublicBar } from "@/components/branding/public-bar"
import { CreateSurfaceField, CreateSurfaceNotice } from "@/components/layout/create-surface"
import { CopyErrorDetails } from "@/components/layout/copy-error-details"
import { PageEmpty, PageError, PageLoading, PageNotFound } from "@/components/layout/page-state"
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
type ErrorKind = "unavailable" | "too-large" | "retry" | "invalid" | "failed"
class SharedChatViewError extends Error {
  constructor(message: string, readonly kind: ErrorKind = "retry") {
    super(message)
  }
}

function parseMessages(value: unknown): SharedMessage[] {
  if (!value || typeof value !== "object" || !Array.isArray((value as { messages?: unknown }).messages)) {
    throw new SharedChatViewError("The transcript response was invalid.", "invalid")
  }
  return (value as { messages: unknown[] }).messages.map((item) => {
    if (!item || typeof item !== "object") throw new SharedChatViewError("The transcript response was invalid.", "invalid")
    const row = item as Record<string, unknown>
    if (typeof row.id !== "string" || (row.role !== "user" && row.role !== "assistant") ||
      typeof row.content !== "string" || typeof row.created_at !== "string") {
      throw new SharedChatViewError("The transcript response was invalid.", "invalid")
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

/** "27 Sep, 08:05" in the reader's own locale and zone. */
function messageTime(iso: string): string {
  const at = new Date(iso)
  if (Number.isNaN(at.getTime())) return ""
  return at.toLocaleString([], { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })
}

/**
 * One transcript row, the kit's message row: a 30 px avatar, who and when on
 * one line, the text under it. The API sends no names, so a person is the
 * initial P and an agent is a neutral bot tile — nothing here pretends to know
 * more than the share carries.
 */
function SharedMessageRow({ message }: { message: SharedMessage }) {
  const person = message.role === "user"
  const time = messageTime(message.created_at)
  return (
    <div data-slot="shared-message" className="flex gap-3 px-4 md:px-6">
      <span
        data-slot="shared-message-avatar"
        aria-hidden
        className={cn(
          "grid size-[30px] shrink-0 place-items-center rounded-full text-label font-semibold",
          person ? "bg-primary text-primary-foreground" : "bg-muted text-muted-foreground",
        )}
      >
        {person ? "P" : <Bot className="size-4" />}
      </span>
      <div className="min-w-0 flex-1">
        <p className="flex items-baseline gap-2 text-body">
          <span data-slot="shared-message-who" className="font-semibold text-foreground">
            {person ? "Person" : "Agent"}
          </span>
          {time && (
            <time dateTime={message.created_at} className="type-page-meta text-muted-foreground-soft">
              {time}
            </time>
          )}
        </p>
        <p className="mt-0.5 whitespace-pre-wrap break-words text-body leading-relaxed">{message.content}</p>
      </div>
    </div>
  )
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
  // Once a transcript is open the form folds into one line above it.
  const [folded, setFolded] = useState(false)
  const idInput = useRef<HTMLInputElement | null>(null)
  const [focusID, setFocusID] = useState(false)
  const pending = useRef<AbortController | null>(null)

  useEffect(() => {
    if (focusID && !folded) {
      idInput.current?.focus()
      setFocusID(false)
    }
  }, [focusID, folded])

  useEffect(() => () => pending.current?.abort(), [])

  function clear() {
    pending.current?.abort()
    pending.current = null
    setShareID("")
    setToken("")
    setMessages([])
    setError("")
    setFieldErrors({})
    setFolded(false)
    setState("idle")
  }

  function tryAnother() {
    setError("")
    setState("idle")
    setFolded(false)
    setFocusID(true)
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
      setFolded(false)
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
      setFolded(true)
      setState("ready")
    } catch (cause) {
      if (pending.current !== controller || controller.signal.aborted) return
      setMessages([])
      setError(cause instanceof SharedChatViewError ? cause.message : "The transcript could not be opened.")
      setErrorKind(cause instanceof SharedChatViewError ? cause.kind : "retry")
      setFolded(false)
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
      <main
        // touch-form raises fields, buttons and links to 44 px under a coarse pointer.
        className="touch-form flex flex-1 flex-col items-center md:px-4 md:pt-8 md:pb-8"
      >
        <div
          data-slot="shared-transcript-column"
          className={cn("flex w-full max-w-[760px] flex-col gap-6", state === "idle" && "max-md:flex-1")}
        >
          {folded ? (
            <p
              data-slot="shared-chat-summary"
              className="flex items-center gap-2 px-4 text-body text-muted-foreground md:px-6"
            >
              <MessageSquareLock className="size-4 shrink-0" aria-hidden />
              <span className="font-mono">{shareID.trim().slice(0, 13)}…</span>
              <span aria-hidden>·</span>
              <Button type="button" variant="link" size="sm" onClick={() => setFolded(false)} className="h-auto p-0 coarse:min-h-[44px]">
                Change
              </Button>
            </p>
          ) : (
            <AuthCard
              size="md"
              icon={MessageSquareLock}
              title="Read a shared chat"
              description="Enter the share ID and token you received. This view contains conversation text only."
              onSubmit={(event) => void load(event)}
              formProps={{ autoComplete: "off" }}
              className={cn("md:mx-auto md:w-full md:max-w-[640px]", state !== "idle" && "max-md:flex-none")}
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
                  ref={idInput}
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
          )}

          {state === "loading" && <PageLoading rows={4} className="px-4 md:px-6" />}

          {state === "error" &&
            (errorKind === "unavailable" ? (
              // One answer for a wrong ID, a wrong token, an expired and a
              // revoked share: the server does not tell them apart, so neither
              // does this.
              <div role="alert" className="px-4 md:px-6">
                <PageNotFound
                  title="This share is not available"
                  description={error}
                  primary={{ label: "Try another share", onClick: tryAnother }}
                  className="p-0"
                />
              </div>
            ) : (
              <PageError
                title={errorKind === "too-large" ? "This transcript is too large to show" : "Couldn’t open the transcript"}
                description={error}
                onRetry={errorKind === "too-large" ? undefined : () => void load()}
                actions={errorKind === "invalid" ? <CopyErrorDetails /> : undefined}
                // The error id slot is empty here; Copy details still needs the footer.
                detail={errorKind === "invalid" ? "Unexpected answer from the server" : undefined}
                className="px-4 md:px-6"
              />
            ))}

          {state === "ready" && (
            <section aria-label="Shared transcript" className="flex flex-col gap-5">
              <div className="px-4 md:px-6">
                <CreateSurfaceNotice tone="info" icon={Info}>
                  This share may show future messages until it expires or is revoked. It cannot run the agent or open files.
                </CreateSurfaceNotice>
              </div>
              {messages.length === 0 ? (
                <PageEmpty icon={MessageSquare} title="No text messages yet" description="New messages appear here when you refresh." className="px-4 md:px-6" />
              ) : (
                messages.map((message) => <SharedMessageRow key={message.id} message={message} />)
              )}
            </section>
          )}
        </div>
      </main>
    </div>
  )
}
