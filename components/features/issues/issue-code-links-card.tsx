"use client"

// The pull requests attached to an issue.
//
// #1758 shipped the whole of link-first Git integration — parse a pasted
// GitHub PR / GitLab MR URL, fetch it through the provider's API with a
// credential already in the vault, store title, state, author and branches —
// and then rendered exactly none of it. There was an HTTP API and a CLI, and
// nothing an owner could look at. This is the missing half.
//
// Three things shape the design, and they are not negotiable:
//
//   The state is drawn BEFORE the title. A merged pull request, a closed one,
//   an open draft and an open ready-to-review one are four different facts,
//   and the reader is scanning for which. Fixed-width badge column so the
//   states line down the left edge and the eye can run them; four distinct
//   `Pill` tones from the detail kit, no invented colours.
//
//   A failure says what to do. The 412 is the common one and its `detail`
//   already names the credential to add AND the account label to put on it —
//   the entire fix. It is rendered in the popover the reader is standing in,
//   not thrown away for "Failed to attach link" and not flashed past in a
//   toast that auto-dismisses before the sentence is read.
//
//   The title, author and branch names are UNTRUSTED. Whoever opened the pull
//   request on the forge chose them; on a public repository, anyone. They are
//   rendered as text — React escapes it — and never through MarkdownContent,
//   which would hand a forge-supplied string to a link factory. The stored URL
//   goes through `safeExternalHref` before it can be an href. The agent-facing
//   read path fences the same strings in `<untrusted>`
//   (internal/api/issues_internal.go); this is the browser's half of that.
//
// A renderer, like the rest of issue-card-detail.tsx: it draws and it calls
// back. The fetches and the writes live in issue-detail-surface.tsx, which is
// what makes this card the same card on /issues/<identifier> and on
// /issues?issue=<identifier>.

import * as React from "react"
import {
  ArrowUpRight,
  GitMerge,
  GitPullRequest,
  GitPullRequestClosed,
  GitPullRequestDraft,
  RefreshCw,
  X,
} from "lucide-react"

import { cn } from "@/lib/utils"
import { relTime } from "@/lib/time"
import { DetailCard, Pill } from "@/components/ui/detail"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Button } from "@/components/ui/button"
import {
  codeLinkBranches,
  codeLinkNoun,
  codeLinkRef,
  codeLinkStaleReason,
  codeLinkStateBadge,
  safeExternalHref,
  type CodeLinkStateIcon,
} from "@/lib/code-links"
import type { IssueCodeLink } from "@/lib/types/mission"

/** What `attach` answers. The failure carries a sentence worth showing. */
export type CodeLinkAttachResult = { ok: true } | { ok: false; message: string }

/**
 * The writes. Shaped after `IssueCardEdit`: absent means read-only, and the
 * card renders the same rows without a single write affordance rather than a
 * different, smaller card.
 *
 * `attach` returns its failure because the message belongs in the popover the
 * reader is standing in. `remove` and `refresh` do not — they are row actions
 * with nowhere to put a paragraph, so the host reports them the way it reports
 * every other row failure on this screen.
 */
export interface CodeLinkEdit {
  attach: (url: string) => Promise<CodeLinkAttachResult>
  remove: (linkId: string) => Promise<void>
  refresh: (linkId: string) => Promise<void>
}

const STATE_ICON: Record<CodeLinkStateIcon, React.ComponentType<{ className?: string }>> = {
  open: GitPullRequest,
  draft: GitPullRequestDraft,
  merged: GitMerge,
  closed: GitPullRequestClosed,
  unknown: GitPullRequest,
}

export function IssueCodeLinksCard({
  links,
  edit,
}: {
  links: IssueCodeLink[]
  edit?: CodeLinkEdit
}) {
  return (
    <DetailCard
      title="Pull requests"
      icon={GitPullRequest}
      subtitle={links.length > 0 ? String(links.length) : undefined}
      action={edit ? <AttachCodeLinkPicker edit={edit} /> : undefined}
      data-testid="issue-code-links"
    >
      {links.length === 0 ? (
        <p className="text-[12px] text-muted-foreground">
          No pull request attached.{" "}
          {edit && "Paste one and Crewship reads its state from the forge."}
        </p>
      ) : (
        <ul className="space-y-2.5">
          {links.map((l) => (
            <CodeLinkRow key={l.id} link={l} edit={edit} />
          ))}
        </ul>
      )}
    </DetailCard>
  )
}

/* ------------------------------------------------------------------ *
 *  One row                                                            *
 * ------------------------------------------------------------------ */

function CodeLinkRow({ link, edit }: { link: IssueCodeLink; edit?: CodeLinkEdit }) {
  const badge = codeLinkStateBadge(link.state)
  const Icon = STATE_ICON[badge.icon]
  const ref = codeLinkRef(link)
  const href = safeExternalHref(link.url)
  const branches = codeLinkBranches(link)
  const stale = codeLinkStaleReason(link)
  const [busy, setBusy] = React.useState(false)

  // The title is the forge's, so the ref is the fallback AND the thing the
  // row is named by for a screen reader — an id we parsed ourselves.
  const label = link.title?.trim() || ref

  async function run(fn: () => Promise<void>) {
    if (busy) return
    setBusy(true)
    try {
      await fn()
    } finally {
      setBusy(false)
    }
  }

  return (
    <li className="group flex items-start gap-2.5" data-testid="code-link-row">
      {/* Fixed width so the badges line down the left edge. The reader is
          scanning this column, not reading it. */}
      <span className="w-[76px] shrink-0 pt-px">
        <Pill tone={badge.tone} data-testid="code-link-state">
          <Icon className="h-3 w-3 shrink-0" />
          {badge.label}
        </Pill>
      </span>

      <div className="min-w-0 flex-1">
        {href ? (
          <a
            href={href}
            target="_blank"
            // The destination is chosen by whoever owns the linked repository.
            // noopener keeps it off window.opener; noreferrer keeps the issue
            // URL out of its referer log.
            rel="noopener noreferrer"
            className="inline-flex min-w-0 max-w-full items-center gap-1 text-[12px] text-foreground/90 hover:underline"
          >
            {/* Text. Never MarkdownContent — see the header of this file. */}
            <span className="truncate">{label}</span>
            <ArrowUpRight className="h-3 w-3 shrink-0 text-muted-foreground-soft" />
          </a>
        ) : (
          // A URL we will not put in an href is still a fact about the issue.
          <span className="block truncate text-[12px] text-foreground/90">{label}</span>
        )}

        <div className="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-[11px] text-muted-foreground">
          <span className="font-mono text-[10px]">{ref}</span>
          {link.author && (
            <>
              <span aria-hidden>·</span>
              <span className="truncate">{link.author}</span>
            </>
          )}
          {branches && (
            <>
              <span aria-hidden>·</span>
              <span className="truncate font-mono text-[10px]">{branches}</span>
            </>
          )}
          {link.last_synced_at && !stale && (
            <>
              <span aria-hidden>·</span>
              <span className="shrink-0 text-muted-foreground-soft">
                checked {relTime(link.last_synced_at)}
              </span>
            </>
          )}
        </div>

        {stale && (
          // The state above is what was true at last_synced_at, not what is
          // true now. Saying so is the difference between "merged" and "was
          // merged the last time we could look".
          <p
            data-testid="code-link-stale"
            className="mt-1 line-clamp-2 text-[11px] leading-relaxed text-warn"
          >
            {link.last_synced_at
              ? `Showing what we saw ${relTime(link.last_synced_at)} — refreshing is failing: `
              : "Never confirmed — refreshing is failing: "}
            {stale}
          </p>
        )}
      </div>

      {edit && (
        // The destructive-row-action pattern this screen already uses for a
        // relation: revealed on hover or keyboard focus, no dialog. Removing a
        // link asserts nothing and leaves nothing behind (the handler hard
        // deletes, like mission_relations), so a confirmation would be
        // ceremony — but it should not sit under the cursor either.
        <span className="flex shrink-0 items-center gap-0.5 pt-0.5">
          <RowAction
            label={`Refresh ${ref}`}
            disabled={busy}
            onClick={() => void run(() => edit.refresh(link.id))}
          >
            <RefreshCw className={cn("h-3 w-3", busy && "animate-spin")} />
          </RowAction>
          <RowAction
            label={`Remove link to ${ref}`}
            disabled={busy}
            destructive
            onClick={() => void run(() => edit.remove(link.id))}
          >
            <X className="h-3 w-3" />
          </RowAction>
        </span>
      )}
    </li>
  )
}

function RowAction({
  label,
  onClick,
  disabled,
  destructive,
  children,
}: {
  label: string
  onClick: () => void
  disabled?: boolean
  destructive?: boolean
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      aria-label={label}
      disabled={disabled}
      onClick={onClick}
      className={cn(
        "rounded p-0.5 text-muted-foreground-soft opacity-0 transition-all",
        "hover:bg-white/[0.08] focus-visible:opacity-100 group-hover:opacity-100",
        "disabled:cursor-not-allowed disabled:opacity-40",
        destructive ? "hover:text-destructive" : "hover:text-foreground",
      )}
    >
      {children}
    </button>
  )
}

/* ------------------------------------------------------------------ *
 *  Attaching                                                          *
 * ------------------------------------------------------------------ */

/**
 * Paste a URL, get a pull request.
 *
 * The same popover shape as `AddRelationPicker` next door, with one addition
 * that is the whole point: the failure is rendered HERE, under the box, with
 * the box still holding what was pasted. Every message this can show names a
 * remedy — add a credential and label it with the host, turn on the
 * private-host opt-in, use a pull-request URL rather than a repository one —
 * and all of them are things the reader does and then retries. A toast would
 * take the sentence away mid-read and empty the box behind it.
 */
function AttachCodeLinkPicker({ edit }: { edit: CodeLinkEdit }) {
  const [open, setOpen] = React.useState(false)
  const [url, setUrl] = React.useState("")
  const [error, setError] = React.useState<string | null>(null)
  const [busy, setBusy] = React.useState(false)

  async function submit() {
    const value = url.trim()
    if (!value || busy) return
    setBusy(true)
    // Clear first: a stale message under a new attempt reads as this
    // attempt's verdict.
    setError(null)
    try {
      const result = await edit.attach(value)
      if (result.ok) {
        setUrl("")
        setOpen(false)
        return
      }
      setError(result.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (!next) setError(null)
      }}
    >
      <PopoverTrigger asChild>
        <button
          type="button"
          aria-label="Attach a pull request"
          className="rounded p-1 text-muted-foreground-soft transition-colors hover:bg-white/[0.06] hover:text-foreground"
        >
          <GitPullRequest className="h-3.5 w-3.5" />
        </button>
      </PopoverTrigger>
      <PopoverContent align="end" sideOffset={4} className="w-[320px] space-y-2 p-3">
        <input
          aria-label="Pull request URL"
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") void submit()
          }}
          placeholder="https://github.com/acme/thing/pull/7"
          className="h-7 w-full rounded border border-border/60 bg-card px-2 text-[12px] outline-none focus:border-primary/50"
        />
        {error ? (
          <p
            role="alert"
            data-testid="code-link-attach-error"
            className="text-[11px] leading-relaxed text-destructive"
          >
            {error}
          </p>
        ) : (
          <p className="text-[11px] leading-relaxed text-muted-foreground-soft">
            A GitHub {codeLinkNoun("GITHUB")} or a GitLab {codeLinkNoun("GITLAB")}, on the public
            service or a self-hosted instance.
          </p>
        )}
        <Button
          type="button"
          size="sm"
          className="h-7 w-full text-[11px]"
          disabled={!url.trim() || busy}
          onClick={() => void submit()}
        >
          {busy ? "Attaching…" : "Attach"}
        </Button>
      </PopoverContent>
    </Popover>
  )
}
