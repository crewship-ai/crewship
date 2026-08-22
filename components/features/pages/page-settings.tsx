"use client"

/**
 * Page settings — who reaches this page, and what this page is.
 *
 * PRD `docs/prd/pages.md` §7.1b (three verbs, three subject kinds), §7.1
 * rule 3 (who may issue), §10b.1 (versions and rollback), §9b (the visual
 * language, copied and not re-invented).
 *
 * The whole grants model existed only behind `crewship page grants|grant|
 * revoke` — the multi-agent permission model of §7.1b was reachable from the
 * CLI and from nowhere a page owner looks. So was the answer to "who owns
 * this, and who changed it last". This is that surface, opened from the same
 * SubBar as Edit.
 *
 * Four things here are load-bearing and none of them are decoration:
 *
 *  1. **Every grant names its issuer.** `granted_by_user_id` is NOT NULL by
 *     migration because §7.1b rule 1 says only a human issues a grant, and an
 *     ACL nobody can audit is not a security control. A row that did not say
 *     who vouched for it would throw away the reason the column exists.
 *
 *  2. **An inert grant is drawn inert, with the server's reason verbatim.** A
 *     grant is live only while the human who issued it still has the standing
 *     they granted from (`inertReason()`, `internal/api/pages_grants_authz.go`):
 *     leaving the workspace or losing ADMIN makes it worth nothing without
 *     deleting the row. Showing such a row as though it worked is exactly the
 *     failure that function was written to prevent — somebody believes access
 *     was granted. The reason is the server's sentence, not a paraphrase,
 *     because the paraphrase is where the two copies drift.
 *
 *  3. **Every level says what it does, `read` included.** All three verbs open
 *     the page and none of them unseal a panel, so the meaning shown under the
 *     level select says both halves — `PAGE_GRANT_LEVEL_MEANING`, beside the
 *     vocabulary it describes. This card used to carry a warning that `read`
 *     decided nothing; the server decides page reach on it now, and the
 *     warning left with the behaviour it described.
 *
 *  4. **Both destructive verbs confirm, and every write goes through
 *     `useApiMutation`.** Revoke and rollback ask first through the shared
 *     `AlertDialog` (§9b.6 — the confirm dialog is on the list of things Pages
 *     must not re-invent). A refusal shows the server's own words and changes
 *     nothing on screen: no row disappears, no field is cleared, because
 *     #1563 rule 3 is that a retry must still have what it needs.
 *
 * It is deliberately NOT part of `page-editor.tsx`. The editor owns one
 * document and its buffer; this owns rows in two other tables that no document
 * can express, and merging them would put a destructive ACL change one stray
 * Save away from a YAML edit.
 */

import * as React from "react"
import {
  AlertTriangle,
  Copy,
  Download,
  Globe,
  History,
  Info,
  KeyRound,
  Loader2,
  Plus,
  Trash2,
  Undo2,
  Webhook,
  X,
} from "lucide-react"
import { toast } from "sonner"

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  fetchPageBundle,
  usePageDelete,
  usePagePublicLinks,
  usePagePublish,
  usePageUnpublish,
  usePageWebhookCreate,
  usePageWebhookRevoke,
  usePageWebhooks,
  type WirePublicLink,
  type WireWebhook,
} from "@/hooks/use-page-sharing"
import { SectionCard } from "@/components/ui/section-card"
import { Spinner } from "@/components/ui/spinner"
import { EmptyState } from "@/components/layout/empty-state"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { formatDateTime } from "@/lib/time"
import { cn } from "@/lib/utils"
import {
  PAGE_GRANT_LEVELS,
  PAGE_GRANT_LEVEL_MEANING,
  PAGE_SUBJECT_TYPES,
  pagePanelCount,
  toPageOwner,
  usePageGrantRevoke,
  usePageGrantWrite,
  usePageGrants,
  usePageRollback,
  usePageVersions,
  type PageGrant,
  type PageGrantLevel,
  type PageSubjectType,
  type PageVersion,
  type WirePageDetail,
} from "@/hooks/use-page-grants"

// ── The shared idiom (§9b.2) ───────────────────────────────────────────────
//
// "Small icon + uppercase tracked label on the left, right-aligned muted status
// word. The right-hand word is always the ANSWER, never a repeat of the label."
//
// The two sizes that used to be written out here are `.type-page-label` and
// `.type-page-meta` from the Pages register (`app/globals.css`). §9b.2 says
// what the idiom IS; the register says how big it is, once.

function CardLabel({ icon: Icon, children }: { icon: React.ElementType; children: React.ReactNode }) {
  return (
    <span className="type-page-label inline-flex items-center gap-1.5 text-foreground/70">
      <Icon className="h-3.5 w-3.5 text-muted-foreground-soft" />
      {children}
    </span>
  )
}

function CardAnswer({ children }: { children: React.ReactNode }) {
  return <span className="type-page-meta text-muted-foreground">{children}</span>
}

/** One label/answer line. Same idiom, one row deep. */
function Fact({
  label,
  mono,
  children,
}: {
  label: string
  mono?: boolean
  children: React.ReactNode
}) {
  return (
    <div
      data-slot="page-fact"
      data-fact={label.toLowerCase()}
      className="flex items-baseline justify-between gap-4 border-b border-border/40 py-2 last:border-b-0"
    >
      <span className="type-page-label shrink-0 text-muted-foreground-soft">
        {label}
      </span>
      <span
        data-slot="fact-value"
        className={cn("min-w-0 text-right text-xs text-muted-foreground", mono && "font-mono")}
      >
        {children}
      </span>
    </div>
  )
}

/**
 * The server's refusal, in its own words.
 *
 * One component for every refusal on this surface so none of them can be
 * quietly rendered as a toast that scrolls away — a 403 on the ACL is an
 * answer the reader has to keep looking at, not a notification.
 */
function Refusal({ children }: { children: React.ReactNode }) {
  return (
    <div
      role="alert"
      data-slot="page-settings-refusal"
      className="flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/[0.06] px-3 py-2 type-page-value text-destructive"
    >
      <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
      <span className="min-w-0">{children}</span>
    </div>
  )
}

/** The em-dash rule (§9b.4): `—` is "no basis", never a guess. */
const DASH = "—"

function dashed(value: string | null | undefined): string {
  return value && value.trim() !== "" ? value : DASH
}

function when(iso: string | null | undefined): string {
  return iso ? formatDateTime(iso) : DASH
}

// ── Access ─────────────────────────────────────────────────────────────────

/**
 * What the revoke dialog is asking about: one row, or every level a subject
 * holds on this page.
 *
 * The second is the shape an incident wants. An agent that has started writing
 * panels it should not typically holds `read` and `produce`, sometimes `write`
 * too, and revoking them one at a time leaves a window between clicks where it
 * still has some of what you are taking away. The server has always accepted a
 * DELETE with no `level` for exactly this — the CLI calls it out as the
 * incident-response shape — and the panel simply never offered it.
 */
type RevokeTarget = { kind: "one"; grant: PageGrant } | { kind: "all"; subject: PageGrant; levels: number }

function GrantRow({
  grant,
  onRevoke,
  onRevokeAll,
  siblingLevels,
  disabled,
}: {
  grant: PageGrant
  onRevoke: () => void
  /** Present only when this subject holds more than one level here. */
  onRevokeAll?: () => void
  siblingLevels: number
  disabled: boolean
}) {
  return (
    <div
      data-slot="page-grant"
      data-live={grant.live ? "true" : "false"}
      data-level={grant.level}
      className={cn(
        "flex flex-col gap-1 border-b border-border/40 py-2.5 last:border-b-0",
        !grant.live && "opacity-75",
      )}
    >
      <div className="flex items-center gap-2">
        <Badge variant="outline" className="h-4 shrink-0 px-1.5 font-mono leading-none">
          {grant.subjectType}
        </Badge>
        <span className="min-w-0 truncate text-xs font-medium text-foreground" title={grant.subject}>
          {grant.subject}
        </span>
        <Badge
          variant="secondary"
          className="h-4 shrink-0 px-1.5 leading-none"
          title={PAGE_GRANT_LEVEL_MEANING[grant.level as PageGrantLevel] ?? grant.level}
        >
          {grant.level}
        </Badge>
        <div className="flex-1" />
        <Button
          size="sm"
          variant="ghost"
          onClick={onRevoke}
          disabled={disabled}
          className="type-page-meta h-6 shrink-0 gap-1 px-2 text-muted-foreground hover:text-destructive"
          aria-label={`Revoke ${grant.level} from ${grant.subjectType}/${grant.subject}`}
        >
          <Trash2 className="h-3 w-3" />
          Revoke
        </Button>
        {/* Offered only where it is a different act from the button beside it.
            On a subject holding one level, "revoke all" IS "revoke", and two
            controls that do the same thing make a reader stop and work out
            which is which. */}
        {onRevokeAll && (
          <Button
            size="sm"
            variant="ghost"
            onClick={onRevokeAll}
            disabled={disabled}
            className="type-page-meta h-6 shrink-0 px-2 text-muted-foreground hover:text-destructive"
            aria-label={`Revoke all ${siblingLevels} levels from ${grant.subjectType}/${grant.subject}`}
          >
            all {siblingLevels}
          </Button>
        )}
      </div>

      {/* The produce scope. An EMPTY list is not "no panels" — a null
          panel_ids column means the grant covers the whole page, and saying
          "none" here would describe an agent as authorised for nothing. */}
      {grant.level === "produce" && (
        <span className="type-page-meta text-muted-foreground">
          {grant.panels.length > 0
            ? `panels: ${grant.panels.join(", ")}`
            : "every panel on this page"}
        </span>
      )}

      {/* §7.1b rule 1: only a human issues a grant, and the audit trail is
          the point. Who vouched for this, and when. */}
      <span className="type-page-meta text-muted-foreground-soft">
        Granted by <span className="text-muted-foreground">{dashed(grant.grantedBy)}</span>
        {grant.grantedAt ? ` · ${formatDateTime(grant.grantedAt)}` : ""}
      </span>

      {grant.live ? (
        <span data-slot="grant-status" className="type-page-meta text-muted-foreground-soft">
          live
        </span>
      ) : (
        <span data-slot="grant-status" className="type-page-meta font-medium text-destructive">
          {/* Verbatim. The reason is the only thing that tells the owner what
              to fix, and "inert" alone tells them something is wrong without
              telling them what. */}
          inert{grant.inertReason ? ` — ${grant.inertReason}` : ""}
        </span>
      )}
    </div>
  )
}

const SELECT_CLASS =
  "h-8 rounded-md border border-input bg-transparent px-2 text-xs text-foreground outline-none " +
  "focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:opacity-50"

function AccessCard({
  workspaceId,
  slug,
  panelIDs,
}: {
  workspaceId: string
  slug: string
  panelIDs: string[]
}) {
  const { grants, inertCount, loading, refusal, error } = usePageGrants(workspaceId, slug)

  const [subjectType, setSubjectType] = React.useState<PageSubjectType>("user")
  const [subject, setSubject] = React.useState("")
  const [level, setLevel] = React.useState<PageGrantLevel>("produce")
  const [panels, setPanels] = React.useState("")
  /** The last write's refusal. Cleared when the form changes, because a
   *  message about a request nobody is making any more is worse than none. */
  const [writeRefusal, setWriteRefusal] = React.useState<string | null>(null)
  const [revokeTarget, setRevokeTarget] = React.useState<RevokeTarget | null>(null)

  const issue = usePageGrantWrite(workspaceId, slug, {
    onOk: (v) => {
      toast.success(`Granted ${v.level} on ${slug} to ${v.subjectType}/${v.subject}`)
      setWriteRefusal(null)
      // Only the reference is cleared, and only on a success. On a refusal
      // every field stays exactly as typed (#1563 rule 3).
      setSubject("")
      setPanels("")
    },
    onRefused: (message) => setWriteRefusal(message),
  })

  const revoke = usePageGrantRevoke(workspaceId, slug, {
    onOk: (v) => {
      toast.success(`Revoked ${v.level ?? "every level"} on ${slug} from ${v.subjectType}/${v.subject}`)
      setWriteRefusal(null)
      setRevokeTarget(null)
    },
    onRefused: (message) => {
      // The dialog closes but the row does NOT: nothing was removed, and the
      // reason has to be readable next to the grant it is about.
      setWriteRefusal(message)
      setRevokeTarget(null)
    },
  })

  const busy = issue.isPending || revoke.isPending

  const answer = refusal
    ? "not yours to read"
    : loading
      ? "loading"
      : grants.length === 0
        ? "no grants"
        : `${grants.length} ${grants.length === 1 ? "grant" : "grants"}${
            inertCount > 0 ? ` · ${inertCount} inert` : ""
          }`

  const onIssue = (e: React.FormEvent) => {
    e.preventDefault()
    const ref = subject.trim()
    if (!ref) {
      // The server says this too; saying it here costs a round trip nobody
      // learns anything from.
      setWriteRefusal("Name the subject: an email for a user, a slug for a crew or an agent.")
      return
    }
    setWriteRefusal(null)
    issue.mutate({
      subjectType,
      subject: ref,
      level,
      panels: panels
        .split(",")
        .map((p) => p.trim())
        .filter(Boolean),
    })
  }

  const touch = () => {
    if (writeRefusal) setWriteRefusal(null)
  }

  return (
    <SectionCard
      title={<CardLabel icon={KeyRound}>Access</CardLabel>}
      actions={<CardAnswer>{answer}</CardAnswer>}
      className="gap-4 py-4"
    >
      <div className="flex flex-col gap-3">
        {refusal && <Refusal>{refusal}</Refusal>}
        {error && <Refusal>{error}</Refusal>}

        {loading && !refusal && (
          <div className="flex items-center gap-2 py-4 text-xs text-muted-foreground">
            <Spinner className="h-3.5 w-3.5" />
            Reading this page&rsquo;s grants…
          </div>
        )}

        {!loading && !refusal && !error && grants.length === 0 && (
          <EmptyState
            size="inline"
            icon={KeyRound}
            title="No grants on this page"
            description={`It is reachable by its owner, by workspace admins, and by the crews that own its panels. Widen it with the form below, or run crewship page grant ${slug} --crew <slug> --level read.`}
          />
        )}

        {!refusal && grants.length > 0 && (
          <div data-slot="page-grants">
            {grants.map((g) => {
              // How many levels this same subject holds. Counted per row rather
              // than grouped into sections: grouping would reorder a list whose
              // order is the server's, and the only thing the count decides is
              // whether one extra control appears.
              const levels = grants.filter(
                (o) => o.subjectType === g.subjectType && o.subjectId === g.subjectId,
              ).length
              return (
                <GrantRow
                  key={`${g.subjectType}:${g.subjectId}:${g.level}`}
                  grant={g}
                  disabled={busy}
                  onRevoke={() => setRevokeTarget({ kind: "one", grant: g })}
                  siblingLevels={levels}
                  onRevokeAll={
                    levels > 1
                      ? () => setRevokeTarget({ kind: "all", subject: g, levels })
                      : undefined
                  }
                />
              )
            })}
          </div>
        )}

        {writeRefusal && <Refusal>{writeRefusal}</Refusal>}

        {/* Issuing. Refused for a caller who is not the owner or an admin, and
            refused for any agent whatever its grants (§7.1b rule 1) — both by
            the server, whose sentence lands in the banner above. */}
        {!refusal && (
          <form onSubmit={onIssue} className="flex flex-col gap-2 rounded-md border border-border/50 p-2.5">
            <div className="flex flex-wrap items-center gap-2">
              <label className="sr-only" htmlFor="grant-subject-type">
                Subject kind
              </label>
              <select
                id="grant-subject-type"
                className={SELECT_CLASS}
                value={subjectType}
                onChange={(e) => {
                  setSubjectType(e.target.value as PageSubjectType)
                  touch()
                }}
              >
                {PAGE_SUBJECT_TYPES.map((k) => (
                  <option key={k} value={k}>
                    {k}
                  </option>
                ))}
              </select>

              <label className="sr-only" htmlFor="grant-subject">
                Subject
              </label>
              <Input
                id="grant-subject"
                value={subject}
                onChange={(e) => {
                  setSubject(e.target.value)
                  touch()
                }}
                placeholder={
                  subjectType === "user" ? "ada@example.com" : subjectType === "crew" ? "lookout" : "watcher"
                }
                className="h-8 min-w-[10rem] flex-1 text-xs"
              />

              <label className="sr-only" htmlFor="grant-level">
                Level
              </label>
              <select
                id="grant-level"
                className={SELECT_CLASS}
                value={level}
                onChange={(e) => {
                  setLevel(e.target.value as PageGrantLevel)
                  touch()
                }}
              >
                {PAGE_GRANT_LEVELS.map((l) => (
                  <option key={l} value={l}>
                    {l}
                  </option>
                ))}
              </select>

              <Button type="submit" size="sm" variant="soft" disabled={busy} className="h-8 gap-1.5 text-xs">
                {issue.isPending ? <Loader2 className="h-3 w-3 animate-spin" /> : <Plus className="h-3 w-3" />}
                Grant
              </Button>
            </div>

            {/* The scope is meaningful for produce and nothing else — the
                database says so with a CHECK and the handler says so with a
                400, so the field is not offered where it would be a lie. */}
            {level === "produce" && (
              <div className="flex flex-col gap-1">
                <label className="sr-only" htmlFor="grant-panels">
                  Panels
                </label>
                <Input
                  id="grant-panels"
                  value={panels}
                  onChange={(e) => {
                    setPanels(e.target.value)
                    touch()
                  }}
                  placeholder={panelIDs.length > 0 ? panelIDs.slice(0, 3).join(", ") : "panel ids, comma-separated"}
                  className="h-8 text-xs"
                />
                <span className="type-page-meta text-muted-foreground-soft">
                  Leave empty to cover every panel. A scope naming a panel this page does not have is refused —
                  it would authorise nothing.
                </span>
              </div>
            )}

            <span className="type-page-meta text-muted-foreground-soft">
              {PAGE_GRANT_LEVEL_MEANING[level]}
            </span>
          </form>
        )}
      </div>

      {/* §9b.6: the confirm dialog is shared, not re-invented. */}
      <AlertDialog open={revokeTarget != null} onOpenChange={(open) => !open && setRevokeTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="flex items-center gap-2 text-sm">
              <AlertTriangle className="h-4 w-4 text-destructive" />
              {revokeTarget?.kind === "all" ? "Revoke every level" : "Revoke this grant"}
            </AlertDialogTitle>
            <AlertDialogDescription className="text-xs">
              {revokeTarget?.kind === "all" ? (
                <>
                  Remove all <strong>{revokeTarget.levels}</strong> levels on{" "}
                  <strong>{slug}</strong> from{" "}
                  <strong>
                    {revokeTarget.subject.subjectType}/{revokeTarget.subject.subject}
                  </strong>
                  ? One request, so there is no moment where some of them are still in force.
                </>
              ) : (
                <>
                  Remove <strong>{revokeTarget?.grant.level}</strong> on <strong>{slug}</strong>{" "}
                  from{" "}
                  <strong>
                    {revokeTarget?.grant.subjectType}/{revokeTarget?.grant.subject}
                  </strong>
                  ?
                </>
              )}{" "}
              The change is journalled with you as the actor, and it takes effect on the next read.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel className="h-7 text-xs">Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="h-7 bg-destructive text-xs text-destructive-foreground hover:bg-destructive/90"
              disabled={revoke.isPending}
              onClick={() => {
                if (!revokeTarget) return
                // Omitting `level` is what makes the server take every one of
                // them in a single DELETE, which is the whole point of the
                // second control — three sequential revokes leave a window
                // where the subject still holds what has not been reached yet.
                const g = revokeTarget.kind === "all" ? revokeTarget.subject : revokeTarget.grant
                revoke.mutate({
                  subjectType: g.subjectType,
                  subject: g.subject,
                  level: revokeTarget.kind === "all" ? undefined : g.level,
                })
              }}
            >
              {revoke.isPending && <Spinner className="mr-1.5 h-3 w-3" />}
              Revoke
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SectionCard>
  )
}

// ── General information ────────────────────────────────────────────────────

function VersionRow({
  version,
  onRollback,
  disabled,
}: {
  version: PageVersion
  onRollback: () => void
  disabled: boolean
}) {
  return (
    <div
      data-slot="page-version"
      data-seq={version.seq}
      className="flex items-center gap-2 border-b border-border/40 py-2 last:border-b-0"
    >
      <span className="type-page-stamp w-10 shrink-0 text-muted-foreground-soft">v{version.seq}</span>
      <div className="flex min-w-0 flex-1 flex-col">
        <span className="truncate text-xs text-foreground/85">
          {dashed(version.name)}
          <span className="text-muted-foreground-soft">
            {" · "}
            {version.panelCount} {version.panelCount === 1 ? "panel" : "panels"}
          </span>
        </span>
        <span className="type-page-meta truncate text-muted-foreground-soft">
          {/* A version whose author was erased is still a version worth
              keeping (the migration says so), and `—` is the honest answer
              for it — not a blank that reads as a bug. */}
          {dashed(version.authorLabel)} · {when(version.createdAt)}
        </span>
      </div>
      {version.current ? (
        <Badge variant="outline" className="h-4 shrink-0 px-1.5 leading-none">
          current
        </Badge>
      ) : (
        <Button
          size="sm"
          variant="ghost"
          onClick={onRollback}
          disabled={disabled}
          className="type-page-meta h-6 shrink-0 gap-1 px-2 text-muted-foreground hover:text-foreground"
          aria-label={`Roll back to version ${version.seq}`}
        >
          <Undo2 className="h-3 w-3" />
          Roll back
        </Button>
      )}
    </div>
  )
}

function GeneralCard({
  workspaceId,
  slug,
  page,
}: {
  workspaceId: string
  slug: string
  page: WirePageDetail | null
}) {
  const owner = toPageOwner(page)
  const panelCount = pagePanelCount(page)
  const { versions, loading, refusal, error } = usePageVersions(workspaceId, slug)

  const [target, setTarget] = React.useState<PageVersion | null>(null)
  const [rollbackRefusal, setRollbackRefusal] = React.useState<string | null>(null)

  const rollback = usePageRollback(workspaceId, slug, {
    onOk: (v) => {
      toast.success(`Rolled ${slug} back to version ${v.to}`, {
        description: "Restored panels arrive with no data — old payloads are never resurrected as current.",
      })
      setRollbackRefusal(null)
      setTarget(null)
    },
    onRefused: (message) => {
      setRollbackRefusal(message)
      setTarget(null)
    },
  })

  return (
    <SectionCard
      title={<CardLabel icon={Info}>General</CardLabel>}
      actions={
        <CardAnswer>
          {panelCount} {panelCount === 1 ? "panel" : "panels"}
        </CardAnswer>
      }
      className="gap-4 py-4"
    >
      <div className="flex flex-col gap-4">
        <div data-slot="page-general" className="flex flex-col">
          {/* §7.1 rule 1: owner_user_id XOR owner_crew_id. Which arc it is
              changes what "the owner" means — a crew-owned page survives the
              person leaving — so the kind is printed, never trimmed off. */}
          <Fact label="Owner">
            {owner ? (
              <>
                <span className="text-muted-foreground-soft">{owner.kind}</span>{" "}
                <span className="text-foreground/85">{owner.label}</span>
              </>
            ) : (
              DASH
            )}
          </Fact>
          <Fact label="Slug" mono>
            {slug}
          </Fact>
          <Fact label="Description">{dashed(page?.description)}</Fact>
          <Fact label="Panels">{panelCount}</Fact>
          <Fact label="Created">{when(page?.created_at)}</Fact>
          {/* §10 defines updated_at as the SPEC's mtime — when the
              arrangement last changed, not when data last arrived. */}
          <Fact label="Spec changed">{when(page?.updated_at)}</Fact>
        </div>

        <div className="flex flex-col gap-2">
          <div className="flex items-center justify-between">
            <CardLabel icon={History}>History</CardLabel>
            <CardAnswer>
              {refusal
                ? "not yours to read"
                : loading
                  ? "loading"
                  : versions.length === 0
                    ? "no versions"
                    : `${versions.length} retained`}
            </CardAnswer>
          </div>

          {refusal && <Refusal>{refusal}</Refusal>}
          {error && <Refusal>{error}</Refusal>}
          {rollbackRefusal && <Refusal>{rollbackRefusal}</Refusal>}

          {loading && !refusal && (
            <div className="flex items-center gap-2 py-3 text-xs text-muted-foreground">
              <Spinner className="h-3.5 w-3.5" />
              Reading the version log…
            </div>
          )}

          {!loading && !refusal && !error && versions.length === 0 && (
            <EmptyState
              size="inline"
              icon={History}
              title="No versions yet"
              description={`Every save is a version. Edit this page, or run crewship page update ${slug} --file page.yaml, and the history starts here.`}
            />
          )}

          {!refusal && versions.length > 0 && (
            <div data-slot="page-versions">
              {versions.map((v) => (
                <VersionRow
                  key={v.seq}
                  version={v}
                  disabled={rollback.isPending}
                  onRollback={() => setTarget(v)}
                />
              ))}
            </div>
          )}
        </div>
      </div>

      <AlertDialog open={target != null} onOpenChange={(open) => !open && setTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="flex items-center gap-2 text-sm">
              <AlertTriangle className="h-4 w-4 text-destructive" />
              Roll back to version {target?.seq}
            </AlertDialogTitle>
            <AlertDialogDescription className="text-xs">
              This replaces the current spec of <strong>{slug}</strong>. A panel this brings back or redefines
              arrives with no data — old payloads are never resurrected and shown as current. The rollback is
              itself a save, so it appends a new version rather than erasing the ones after {target?.seq}.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel className="h-7 text-xs">Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="h-7 bg-destructive text-xs text-destructive-foreground hover:bg-destructive/90"
              disabled={rollback.isPending}
              onClick={() => {
                if (!target) return
                rollback.mutate({ to: target.seq })
              }}
            >
              {rollback.isPending && <Spinner className="mr-1.5 h-3 w-3" />}
              Roll back
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SectionCard>
  )
}

// ── The surface ────────────────────────────────────────────────────────────

export interface PageSettingsProps {
  workspaceId: string
  slug: string
  /** The page exactly as it came off the wire — `usePage(...).raw`. */
  page: WirePageDetail | null
  onClose: () => void
}

// ── Sharing outward ────────────────────────────────────────────────────────

/**
 * A secret the server will never send again.
 *
 * Both `publish` and `webhook create` answer with a token exactly once — every
 * later read omits it, because what is stored is a hash. So this is not a
 * convenience banner: it is the only moment the value exists on this screen,
 * and dismissing it is the user saying they have it. It stays until they do.
 */
function OneTimeSecret({ url, onDone }: { url: string; onDone: () => void }) {
  const [copied, setCopied] = React.useState(false)
  return (
    <div className="flex flex-col gap-2 rounded-md border border-primary/40 bg-primary/[0.06] p-3">
      <div className="type-page-meta flex items-center gap-1.5 font-medium text-foreground">
        <AlertTriangle className="h-3.5 w-3.5 text-primary" />
        Copy this now — it is shown once
      </div>
      <code className="type-page-stamp block overflow-x-auto rounded bg-background/60 px-2 py-1.5 leading-relaxed">
        {url}
      </code>
      <div className="flex items-center gap-2">
        <Button
          size="sm"
          variant="secondary"
          className="h-7 gap-1.5 px-2.5 text-xs"
          onClick={() => {
            void navigator.clipboard?.writeText(url)
            setCopied(true)
          }}
        >
          <Copy className="h-3 w-3" />
          {copied ? "Copied" : "Copy"}
        </Button>
        <Button size="sm" variant="ghost" className="h-7 px-2.5 text-xs" onClick={onDone}>
          I have it
        </Button>
      </div>
    </div>
  )
}

/** One public link, live or withdrawn. */
function LinkRow({
  link,
  onRevoke,
}: {
  link: WirePublicLink
  onRevoke: () => void
}) {
  return (
    <div
      data-slot="page-public-link"
      data-live={link.live ? "true" : "false"}
      className="flex items-start gap-3 border-b border-border/40 py-2.5 last:border-b-0"
    >
      <div className="min-w-0 flex-1">
        <div className="type-page-value flex flex-wrap items-center gap-1.5">
          <span className="type-page-stamp text-foreground/90">{link.id}</span>
          {link.has_password && (
            <Badge variant="outline" className="type-page-label h-4 px-1">
              password
            </Badge>
          )}
          {link.show_provenance ? (
            <Badge variant="outline" className="type-page-label h-4 px-1">
              provenance shown
            </Badge>
          ) : null}
        </div>
        <div className="type-page-meta text-muted-foreground">
          {link.live ? `Expires ${when(link.expires_at)}` : `Withdrawn ${when(link.revoked_at)}`}
          {" · "}
          {/* A link nobody has opened is a different fact from one that has
              been read, and it is the fact an audit asks for first. */}
          {link.last_seen_at ? `last opened ${when(link.last_seen_at)}` : "never opened"}
          {" · by "}
          {dashed(link.created_by)}
        </div>
        {link.panels.length > 0 && (
          <div className="type-page-meta text-muted-foreground-soft">
            Serves {link.panels.join(", ")}
          </div>
        )}
      </div>
      <div className="shrink-0">
        {link.live ? (
          <Button size="sm" variant="ghost" className="h-7 px-2 text-xs" onClick={onRevoke}>
            Withdraw
          </Button>
        ) : (
          <span className="type-page-meta text-muted-foreground-soft">revoked</span>
        )}
      </div>
    </div>
  )
}

/**
 * Public links — the page as seen by somebody with no account.
 *
 * This sat only behind `crewship page publish` until now, which put the one
 * question a reader arrives at this panel with — "who outside can see this?" —
 * in a place they had no reason to look. It belongs next to Access, because
 * the two together are the whole answer to who reaches this page.
 */
function SharingCard({ workspaceId, slug }: { workspaceId: string; slug: string }) {
  const { links, loading, refusal, error } = usePagePublicLinks(workspaceId, slug)
  const [minted, setMinted] = React.useState<string | null>(null)
  const [writeRefusal, setWriteRefusal] = React.useState<string | null>(null)
  const [revokeTarget, setRevokeTarget] = React.useState<WirePublicLink | null>(null)
  const [days, setDays] = React.useState("")
  const [password, setPassword] = React.useState("")
  const [showProvenance, setShowProvenance] = React.useState(false)

  const publish = usePagePublish(workspaceId, slug, {
    onOk: (link) => {
      setWriteRefusal(null)
      setDays("")
      setPassword("")
      // The URL is in this response and in no other. Holding it in state is
      // what gives the reader a chance to copy it.
      setMinted(link.url ?? null)
      toast.success("Published", { description: "The link is live until it expires or you withdraw it." })
    },
    onRefused: setWriteRefusal,
  })
  const unpublish = usePageUnpublish(workspaceId, slug, {
    onOk: () => {
      setRevokeTarget(null)
      toast.success("Withdrawn", { description: "The link stops resolving immediately." })
    },
    onRefused: (m) => {
      setRevokeTarget(null)
      setWriteRefusal(m)
    },
  })

  const liveCount = links.reduce((n, l) => (l.live ? n + 1 : n), 0)
  const answer = loading
    ? "…"
    : liveCount === 0
      ? "not published"
      : `${liveCount} live link${liveCount === 1 ? "" : "s"}`

  return (
    <SectionCard
      title={<CardLabel icon={Globe}>Public links</CardLabel>}
      actions={<CardAnswer>{answer}</CardAnswer>}
      className="gap-4 py-4"
    >
      <div className="flex flex-col gap-3">
        {refusal && <Refusal>{refusal}</Refusal>}
        {error && <Refusal>{error}</Refusal>}
        {writeRefusal && <Refusal>{writeRefusal}</Refusal>}
        {minted && <OneTimeSecret url={minted} onDone={() => setMinted(null)} />}

        {loading && !refusal && (
          <div className="flex items-center gap-2 py-4 text-xs text-muted-foreground">
            <Spinner className="h-3.5 w-3.5" />
            Reading this page&rsquo;s links…
          </div>
        )}

        {!loading && !refusal && !error && links.length === 0 && (
          <EmptyState
            size="inline"
            icon={Globe}
            title="Not published"
            description="Only panels that declare `public: true` are ever served on a link, and provenance is stripped unless you ask for it. Nothing here is visible to anyone until you publish."
          />
        )}

        {links.length > 0 && (
          <div className="flex flex-col">
            {links.map((l) => (
              <LinkRow key={l.id} link={l} onRevoke={() => setRevokeTarget(l)} />
            ))}
          </div>
        )}

        {!refusal && (
          <div className="flex flex-col gap-2 rounded-md border border-border/50 bg-background/40 p-2.5">
            <div className="flex flex-wrap items-center gap-2">
              <Input
                value={days}
                onChange={(e) => setDays(e.target.value)}
                placeholder="30"
                inputMode="numeric"
                aria-label="Days until the link expires"
                className="h-8 w-20 text-xs"
              />
              <span className="type-page-meta text-muted-foreground">days</span>
              <Input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="password (optional)"
                aria-label="Password for the link"
                className="h-8 min-w-[10rem] flex-1 text-xs"
              />
              <Button
                size="sm"
                className="h-8 gap-1.5 px-3 text-xs"
                disabled={publish.isPending}
                onClick={() => {
                  setWriteRefusal(null)
                  const n = Number(days.trim())
                  publish.mutate({
                    expiresInDays: days.trim() !== "" && Number.isFinite(n) ? n : undefined,
                    password: password.trim() || undefined,
                    showProvenance,
                  })
                }}
              >
                {publish.isPending ? <Spinner className="h-3 w-3" /> : <Plus className="h-3 w-3" />}
                Publish
              </Button>
            </div>
            <label className="type-page-meta flex items-center gap-1.5 text-muted-foreground">
              <input
                type="checkbox"
                checked={showProvenance}
                onChange={(e) => setShowProvenance(e.target.checked)}
                className="h-3 w-3"
              />
              Show provenance — run ids, crew and agent names. Off by default.
            </label>
            <p className="type-page-meta text-muted-foreground-soft">
              Leave the days empty for the default of 30. Every link expires; the maximum is 365.
            </p>
          </div>
        )}
      </div>

      <AlertDialog open={revokeTarget != null} onOpenChange={(o) => !o && setRevokeTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="flex items-center gap-2 text-sm">
              <AlertTriangle className="h-4 w-4 text-destructive" />
              Withdraw this link
            </AlertDialogTitle>
            <AlertDialogDescription className="text-xs">
              It stops resolving immediately for anyone holding it. The row stays in this list with
              the time it was withdrawn, because &ldquo;was it used after we pulled it&rdquo; is the
              question an incident asks.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel className="h-7 text-xs">Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="h-7 bg-destructive text-xs text-destructive-foreground hover:bg-destructive/90"
              disabled={unpublish.isPending}
              onClick={() => revokeTarget && unpublish.mutate({ id: revokeTarget.id })}
            >
              {unpublish.isPending && <Spinner className="mr-1.5 h-3 w-3" />}
              Withdraw
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SectionCard>
  )
}

/**
 * Panel webhooks — the door for a producer that cannot run the binary.
 *
 * A token is bound to exactly ONE panel, and that is the property worth
 * putting on screen: a leaked token writes that panel and nothing else. The
 * form therefore makes the panel a required choice rather than an optional
 * scope, which is the same shape the server enforces.
 */
function WebhooksCard({
  workspaceId,
  slug,
  panelIDs,
}: {
  workspaceId: string
  slug: string
  panelIDs: string[]
}) {
  const { webhooks, loading, refusal, error } = usePageWebhooks(workspaceId, slug)
  const [minted, setMinted] = React.useState<string | null>(null)
  const [writeRefusal, setWriteRefusal] = React.useState<string | null>(null)
  const [revokeTarget, setRevokeTarget] = React.useState<WireWebhook | null>(null)
  const [panel, setPanel] = React.useState(panelIDs[0] ?? "")
  const [name, setName] = React.useState("")

  const create = usePageWebhookCreate(workspaceId, slug, {
    onOk: (wh) => {
      setWriteRefusal(null)
      setName("")
      setMinted(wh.url ?? null)
      toast.success("Token minted", { description: `It writes ${wh.panel} and nothing else.` })
    },
    onRefused: setWriteRefusal,
  })
  const revoke = usePageWebhookRevoke(workspaceId, slug, {
    onOk: () => {
      setRevokeTarget(null)
      toast.success("Token revoked", { description: "The next request with it is refused." })
    },
    onRefused: (m) => {
      setRevokeTarget(null)
      setWriteRefusal(m)
    },
  })

  const liveCount = webhooks.reduce((n, w) => (w.live ? n + 1 : n), 0)
  const answer = loading ? "…" : liveCount === 0 ? "none" : `${liveCount} live`

  return (
    <SectionCard
      title={<CardLabel icon={Webhook}>Webhooks</CardLabel>}
      actions={<CardAnswer>{answer}</CardAnswer>}
      className="gap-4 py-4"
    >
      <div className="flex flex-col gap-3">
        {refusal && <Refusal>{refusal}</Refusal>}
        {error && <Refusal>{error}</Refusal>}
        {writeRefusal && <Refusal>{writeRefusal}</Refusal>}
        {minted && <OneTimeSecret url={minted} onDone={() => setMinted(null)} />}

        {loading && !refusal && (
          <div className="flex items-center gap-2 py-4 text-xs text-muted-foreground">
            <Spinner className="h-3.5 w-3.5" />
            Reading this page&rsquo;s tokens…
          </div>
        )}

        {!loading && !refusal && !error && webhooks.length === 0 && (
          <EmptyState
            size="inline"
            icon={Webhook}
            title="No webhook tokens"
            description="Mint one for a system that cannot run the CLI — a cron on somebody else's box, a CI step, a gateway. Each token writes exactly one panel."
          />
        )}

        {webhooks.length > 0 && (
          <div className="flex flex-col">
            {webhooks.map((w) => (
              <div
                key={w.id}
                data-slot="page-webhook"
                data-live={w.live ? "true" : "false"}
                className="flex items-start gap-3 border-b border-border/40 py-2.5 last:border-b-0"
              >
                <div className="min-w-0 flex-1">
                  <div className="type-page-value flex flex-wrap items-center gap-1.5">
                    <span className="font-medium text-foreground/90">{dashed(w.name)}</span>
                    <Badge variant="outline" className="type-page-label h-4 px-1">
                      {w.panel}
                    </Badge>
                  </div>
                  <div className="type-page-meta text-muted-foreground">
                    {w.live ? `Minted ${when(w.created_at)}` : `Revoked ${when(w.revoked_at)}`}
                    {" · "}
                    {w.fire_count > 0
                      ? `fired ${w.fire_count}×, last ${when(w.last_fired_at)}`
                      : "never fired"}
                    {" · by "}
                    {dashed(w.created_by)}
                  </div>
                </div>
                <div className="shrink-0">
                  {w.live ? (
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-7 px-2 text-xs"
                      onClick={() => setRevokeTarget(w)}
                    >
                      Revoke
                    </Button>
                  ) : (
                    <span className="type-page-meta text-muted-foreground-soft">revoked</span>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}

        {!refusal && panelIDs.length > 0 && (
          <div className="flex flex-wrap items-center gap-2 rounded-md border border-border/50 bg-background/40 p-2.5">
            <select
              value={panel}
              onChange={(e) => setPanel(e.target.value)}
              aria-label="Panel this token may write"
              className="h-8 rounded-md border border-border bg-background px-2 text-xs"
            >
              {panelIDs.map((id) => (
                <option key={id} value={id}>
                  {id}
                </option>
              ))}
            </select>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="what holds it (optional)"
              aria-label="A label for this token"
              className="h-8 min-w-[10rem] flex-1 text-xs"
            />
            <Button
              size="sm"
              className="h-8 gap-1.5 px-3 text-xs"
              disabled={create.isPending || !panel}
              onClick={() => {
                setWriteRefusal(null)
                create.mutate({ panel, name: name.trim() || undefined })
              }}
            >
              {create.isPending ? <Spinner className="h-3 w-3" /> : <Plus className="h-3 w-3" />}
              Mint
            </Button>
          </div>
        )}
      </div>

      <AlertDialog open={revokeTarget != null} onOpenChange={(o) => !o && setRevokeTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="flex items-center gap-2 text-sm">
              <AlertTriangle className="h-4 w-4 text-destructive" />
              Revoke this token
            </AlertDialogTitle>
            <AlertDialogDescription className="text-xs">
              Whatever holds it stops being able to write{" "}
              <strong>{revokeTarget?.panel}</strong> immediately. The row stays in this list with
              its fire count, so you can still answer whether it was used after you pulled it.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel className="h-7 text-xs">Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="h-7 bg-destructive text-xs text-destructive-foreground hover:bg-destructive/90"
              disabled={revoke.isPending}
              onClick={() => revokeTarget && revoke.mutate({ id: revokeTarget.id })}
            >
              {revoke.isPending && <Spinner className="mr-1.5 h-3 w-3" />}
              Revoke
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SectionCard>
  )
}

/**
 * Deleting the page.
 *
 * Its own card at the bottom rather than a button in General, because the two
 * are not the same kind of thing: everything in General describes the page,
 * and this ends it. The panel data goes with it — the payload ring is the
 * page's, not the producer's — and the confirm says so, because a producer
 * pushing every thirty seconds does not make the history it wrote recoverable.
 */
function DangerCard({
  workspaceId,
  slug,
  onDeleted,
}: {
  workspaceId: string
  slug: string
  onDeleted: () => void
}) {
  const [open, setOpen] = React.useState(false)
  const [typed, setTyped] = React.useState("")
  const [writeRefusal, setWriteRefusal] = React.useState<string | null>(null)

  const del = usePageDelete(workspaceId, slug, {
    onOk: () => {
      setOpen(false)
      toast.success("Page deleted", { description: `${slug} and its panels are gone.` })
      onDeleted()
    },
    onRefused: (m) => {
      setOpen(false)
      setWriteRefusal(m)
    },
  })

  return (
    <SectionCard
      title={<CardLabel icon={Trash2}>Delete</CardLabel>}
      className="gap-4 border-destructive/30 py-4"
    >
      <div className="flex flex-col gap-3">
        {writeRefusal && <Refusal>{writeRefusal}</Refusal>}
        <p className="type-page-meta text-muted-foreground">
          Deletes the page, its panels and every payload they hold. Grants, public links and webhook
          tokens go with it. There is no undo — a rollback restores a spec, not a deleted page.
        </p>
        <div>
          <Button
            size="sm"
            variant="outline"
            className="h-8 gap-1.5 border-destructive/50 px-3 text-xs text-destructive hover:bg-destructive/10"
            onClick={() => {
              setTyped("")
              setOpen(true)
            }}
          >
            <Trash2 className="h-3 w-3" />
            Delete this page
          </Button>
        </div>
      </div>

      <AlertDialog open={open} onOpenChange={setOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="flex items-center gap-2 text-sm">
              <AlertTriangle className="h-4 w-4 text-destructive" />
              Delete {slug}
            </AlertDialogTitle>
            <AlertDialogDescription className="text-xs">
              Every panel on this page and every payload it holds is deleted with it. Type the slug
              to confirm — the friction is deliberate, because this is the one action on this panel
              nothing can undo.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <Input
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
            placeholder={slug}
            aria-label="Type the page slug to confirm"
            className="h-8 text-xs"
          />
          <AlertDialogFooter>
            <AlertDialogCancel className="h-7 text-xs">Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="h-7 bg-destructive text-xs text-destructive-foreground hover:bg-destructive/90"
              disabled={del.isPending || typed.trim() !== slug}
              onClick={() => del.mutate()}
            >
              {del.isPending && <Spinner className="mr-1.5 h-3 w-3" />}
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SectionCard>
  )
}


/**
 * Export — the page as a document you can install somewhere else.
 *
 * Deliberately a download rather than a preview pane: a bundle is something
 * you keep, and putting it on screen invites hand-editing a format the server
 * validates strictly. It is also NOT the page's spec — the bundle drops
 * `wake:`, `on_failure:`, `actions:`, `refresh:` and `public:`, because a
 * gate that arrived with an install would create automations nobody in the
 * receiving workspace approved, and publication is a property of the install
 * rather than of the document. The card says so, because a person who exports
 * a monitored page and imports a silent one should learn that here and not
 * from a panel that never fired.
 */
function ExportCard({ workspaceId, slug }: { workspaceId: string; slug: string }) {
  const [busy, setBusy] = React.useState(false)
  const [refusal, setRefusal] = React.useState<string | null>(null)

  return (
    <SectionCard
      title={<CardLabel icon={Download}>Export</CardLabel>}
      className="gap-4 py-4"
    >
      <div className="flex flex-col gap-3">
        {refusal && <Refusal>{refusal}</Refusal>}
        <p className="type-page-meta text-muted-foreground">
          Downloads this page as a portable bundle — its panels, their owners and producers, and the
          references another workspace has to bind. Wake gates, on-failure routes, actions and
          publication do <strong>not</strong> travel: they would arrive as automations and links
          nobody there approved.
        </p>
        <div>
          <Button
            size="sm"
            variant="outline"
            className="h-8 gap-1.5 px-3 text-xs"
            disabled={busy}
            onClick={async () => {
              setBusy(true)
              setRefusal(null)
              try {
                const bundle = await fetchPageBundle(workspaceId, slug)
                // Held only long enough for the browser to take it. Revoked
                // straight after, because an object URL keeps the whole blob
                // alive for the life of the document otherwise.
                const blob = new Blob([JSON.stringify(bundle, null, 2)], {
                  type: "application/json",
                })
                const href = URL.createObjectURL(blob)
                const a = document.createElement("a")
                a.href = href
                a.download = `${slug}.bundle.json`
                a.click()
                URL.revokeObjectURL(href)
                toast.success("Exported", { description: `${slug}.bundle.json` })
              } catch (err) {
                setRefusal(err instanceof Error ? err.message : "Export failed")
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy ? <Spinner className="h-3 w-3" /> : <Download className="h-3 w-3" />}
            Download bundle
          </Button>
        </div>
      </div>
    </SectionCard>
  )
}

export function PageSettings({ workspaceId, slug, page, onClose }: PageSettingsProps) {
  // Panel ids, for the produce-scope placeholder. A sealed panel still has an
  // id (§11b.14 keeps `panel_id`), and it is still a legitimate scope target.
  const panelIDs = React.useMemo(() => {
    if (!Array.isArray(page?.panels)) return []
    return page.panels
      .map((p) => (typeof p.id === "string" && p.id) || (typeof p.panel_id === "string" && p.panel_id) || "")
      .filter(Boolean)
  }, [page])

  const title = `Settings for ${slug}`

  React.useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // Escape closes — unless a confirm dialog is open above this, which
      // takes the key first and only dismisses itself.
      if (e.key === "Escape" && !document.querySelector("[data-slot='alert-dialog-content']")) {
        onClose()
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [onClose])

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 md:p-8">
      <button
        type="button"
        aria-label="Close page settings"
        onClick={onClose}
        className="absolute inset-0 bg-background/70 backdrop-blur-md"
      />
      <div
        role="dialog"
        aria-label={title}
        className="relative flex h-full max-h-[92vh] w-full max-w-[720px] flex-col overflow-hidden rounded-xl border border-border/60 bg-card shadow-2xl"
      >
        <div className="flex shrink-0 items-center justify-between gap-3 border-b border-border/60 bg-card/30 px-4 py-2.5">
          <div className="type-page-meta flex min-w-0 items-center gap-2.5 text-muted-foreground">
            <CONCEPT_ICON.pages className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" />
            <span className="font-medium text-foreground">Page settings</span>
            <span className="opacity-60">·</span>
            <span className="type-page-stamp truncate">{slug}</span>
          </div>
          <Button size="sm" variant="ghost" onClick={onClose} className="h-8 gap-1.5 px-2.5 text-xs">
            <X className="h-3.5 w-3.5" />
            Close
          </Button>
        </div>

        <div className="flex-1 overflow-auto">
          <div className="flex flex-col gap-4 p-4">
            <AccessCard workspaceId={workspaceId} slug={slug} panelIDs={panelIDs} />
            {/* Access and Public links sit together on purpose: they are the two
                halves of one question — who reaches this page — and splitting
                them across surfaces is what sent people to the CLI to answer
                the second half. */}
            <SharingCard workspaceId={workspaceId} slug={slug} />
            <WebhooksCard workspaceId={workspaceId} slug={slug} panelIDs={panelIDs} />
            <GeneralCard workspaceId={workspaceId} slug={slug} page={page} />
            <ExportCard workspaceId={workspaceId} slug={slug} />
            <DangerCard workspaceId={workspaceId} slug={slug} onDeleted={onClose} />
          </div>
        </div>
      </div>
    </div>
  )
}
