"use client"

// The comment composer on the issue card, and the `@` picker inside it.
//
// The reason this exists at all is the next step, not this one: an agent
// becomes a participant in an issue when somebody @mentions it in a comment.
// The trigger is the backend's job; the format the trigger has to read is
// this file's, and it is documented in lib/mentions.ts.
//
// Two things are load-bearing and easy to lose in a rewrite:
//
//   1. The picker inserts a **token**, not a name. `@Robin` is a string; a
//      mention is an id. Everything downstream — the chip, the trigger, the
//      notification — reads the id and looks the rest up.
//   2. The textarea keeps focus the whole time. The picker is a listbox the
//      textarea *controls* (`role="combobox"` + `aria-activedescendant`), the
//      way GitHub's comment box does it, not a thing you tab into. Arrow keys
//      move the active option without the caret ever leaving the draft.

import * as React from "react"
import { AtSign, Loader2, Send } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import { Popover, PopoverAnchor, PopoverContent } from "@/components/ui/popover"
import { Command, CommandGroup, CommandItem, CommandList } from "@/components/ui/command"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { applyMention, findMentionQuery, type MentionAgent } from "@/lib/mentions"
import { mentionAvatarSeed } from "@/components/features/issues/mention-chip"

/** How many candidates the picker will show at once. */
const MAX_MATCHES = 8

interface Props {
  /** Agents that may be mentioned here — the workspace roster. */
  agents: readonly MentionAgent[]
  /**
   * Posts the comment. Resolve `true` to clear the draft; anything else keeps
   * it, because a body that failed to post is the only copy of it there is.
   */
  onSubmit: (body: string) => boolean | Promise<boolean>
  /** Initial for the author bubble; the avatar column is decorative. */
  authorInitial?: string
  placeholder?: string
  disabled?: boolean
  className?: string
}

export function CommentComposer({
  agents,
  onSubmit,
  authorInitial = "U",
  placeholder = "Write a comment.  @ to bring in an agent.",
  disabled,
  className,
}: Props) {
  const baseId = React.useId()
  const listId = `${baseId}-mentions`
  const boxRef = React.useRef<HTMLTextAreaElement>(null)

  const [value, setValue] = React.useState("")
  const [caret, setCaret] = React.useState(0)
  const [dismissed, setDismissed] = React.useState(false)
  const [active, setActive] = React.useState(0)
  const [submitting, setSubmitting] = React.useState(false)
  // Set when a pick rewrites the draft, so the caret can be restored after
  // React has committed the new value.
  const [pendingCaret, setPendingCaret] = React.useState<number | null>(null)

  const query = React.useMemo(() => findMentionQuery(value, caret), [value, caret])

  const matches = React.useMemo(() => {
    if (!query) return []
    const q = query.query
    const pool = q
      ? agents.filter(
          (a) => a.slug.toLowerCase().includes(q) || a.name.toLowerCase().includes(q),
        )
      : agents
    return pool.slice(0, MAX_MATCHES)
  }, [agents, query])

  const open = !dismissed && query !== null && matches.length > 0

  // A filtered-down list must not leave the active index pointing past its end.
  React.useEffect(() => {
    setActive(0)
  }, [query?.start, query?.query])

  React.useLayoutEffect(() => {
    if (pendingCaret === null) return
    const el = boxRef.current
    if (el) {
      el.focus()
      el.setSelectionRange(pendingCaret, pendingCaret)
    }
    setPendingCaret(null)
  }, [pendingCaret])

  const activeAgent = matches[active] ?? matches[0]

  // `aria-activedescendant` has to name a real element id, and cmdk mints its
  // own for every item — it spreads incoming props first and then overwrites
  // `id`, so passing one is silently ignored. `asChild` is the way back in:
  // the item renders through a Radix Slot, and a Slot lets the child's own
  // props win. Without this the textarea would point at an id that is not in
  // the document, which reads to a screen reader as no active option at all.
  const optionId = (agent: MentionAgent) => `${baseId}-opt-${agent.id}`

  function syncCaret(el: HTMLTextAreaElement) {
    setCaret(el.selectionStart ?? el.value.length)
  }

  function pick(agent: MentionAgent) {
    if (!query) return
    const next = applyMention(value, query.start, caret, agent)
    setValue(next.text)
    setCaret(next.caret)
    setPendingCaret(next.caret)
    setDismissed(false)
  }

  async function submit() {
    const body = value.trim()
    if (!body || submitting || disabled) return
    setSubmitting(true)
    try {
      const ok = await onSubmit(body)
      if (ok) {
        setValue("")
        setCaret(0)
      }
    } finally {
      setSubmitting(false)
    }
  }

  function onKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    // Submit wins over the picker: ⌘/Ctrl+Enter means "post" everywhere else
    // in the app, and a half-typed handle is not a reason to change that.
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault()
      void submit()
      return
    }
    if (!open || !activeAgent) return
    if (e.key === "ArrowDown") {
      e.preventDefault()
      setActive((a) => (a + 1) % matches.length)
    } else if (e.key === "ArrowUp") {
      e.preventDefault()
      setActive((a) => (a - 1 + matches.length) % matches.length)
    } else if (e.key === "Enter" || e.key === "Tab") {
      e.preventDefault()
      pick(activeAgent)
    } else if (e.key === "Escape") {
      e.preventDefault()
      setDismissed(true)
    }
  }

  return (
    <div className={cn("flex gap-3", className)}>
      <span
        aria-hidden
        className="mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-primary/20 text-[11px] font-semibold text-primary"
      >
        {authorInitial.charAt(0).toUpperCase()}
      </span>

      <div className="min-w-0 flex-1 space-y-2">
        <Popover open={open} onOpenChange={(o) => !o && setDismissed(true)}>
          <PopoverAnchor asChild>
            <Textarea
              ref={boxRef}
              value={value}
              disabled={disabled}
              placeholder={placeholder}
              role="combobox"
              aria-expanded={open}
              aria-controls={open ? listId : undefined}
              aria-autocomplete="list"
              aria-haspopup="listbox"
              aria-activedescendant={open && activeAgent ? optionId(activeAgent) : undefined}
              className="min-h-[72px] resize-none bg-card text-[13px]"
              onChange={(e) => {
                setValue(e.target.value)
                setDismissed(false)
                syncCaret(e.target)
              }}
              onKeyUp={(e) => syncCaret(e.currentTarget)}
              onClick={(e) => syncCaret(e.currentTarget)}
              onSelect={(e) => syncCaret(e.currentTarget)}
              onKeyDown={onKeyDown}
            />
          </PopoverAnchor>

          <PopoverContent
            align="start"
            side="top"
            sideOffset={6}
            className="w-[320px] max-w-[90vw] p-0"
            // The draft never loses focus: the picker is driven from the
            // textarea, so handing it the caret would break typing.
            onOpenAutoFocus={(e) => e.preventDefault()}
            onCloseAutoFocus={(e) => e.preventDefault()}
          >
            <div className="flex items-center gap-1.5 border-b border-border/60 px-2.5 py-1.5 text-[11px] text-muted-foreground">
              <AtSign className="h-3 w-3" />
              <span>Mention an agent — it gets the issue and replies here</span>
            </div>
            <Command shouldFilter={false} value={activeAgent?.id ?? ""}>
              <CommandList id={listId} className="max-h-56">
                <CommandGroup>
                  {matches.map((agent) => (
                    <CommandItem
                      key={agent.id}
                      value={agent.id}
                      onSelect={() => pick(agent)}
                      asChild
                    >
                      <div id={optionId(agent)} role="option" className="gap-2">
                        <AgentAvatar
                          seed={mentionAvatarSeed(agent)}
                          style={agent.avatar_style}
                          avatarUrl={agent.avatar_url}
                          className="h-5 w-5 shrink-0"
                          alt=""
                        />
                        <span className="min-w-0 flex-1 truncate">
                          <span className="font-medium">{agent.name}</span>{" "}
                          <span className="text-muted-foreground">@{agent.slug}</span>
                        </span>
                        {agent.role_title && (
                          <span className="shrink-0 text-[10px] text-muted-foreground-soft">
                            {agent.role_title}
                          </span>
                        )}
                      </div>
                    </CommandItem>
                  ))}
                </CommandGroup>
              </CommandList>
            </Command>
          </PopoverContent>
        </Popover>

        <p aria-live="polite" className="sr-only">
          {open ? `${matches.length} agents match. Use the arrow keys.` : ""}
        </p>

        <div className="flex items-center justify-between">
          <span className="text-[11px] text-muted-foreground-soft">
            <kbd className="font-sans">⌘/Ctrl</kbd> + <kbd className="font-sans">Enter</kbd> to
            post · <kbd className="font-sans">@</kbd> to mention an agent
          </span>
          <Button
            type="button"
            size="sm"
            className="h-7 gap-1.5 text-[11px]"
            onClick={() => void submit()}
            disabled={disabled || submitting || !value.trim()}
          >
            {submitting ? (
              <Loader2 className="h-3 w-3 animate-spin" />
            ) : (
              <Send className="h-3 w-3" />
            )}
            Comment
          </Button>
        </div>
      </div>
    </div>
  )
}
