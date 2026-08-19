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
  History,
  Info,
  KeyRound,
  Loader2,
  Plus,
  Trash2,
  Undo2,
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

/** What the revoke dialog is asking about. Held as a whole grant so the
 *  sentence can name the level as well as the subject. */
type RevokeTarget = PageGrant

function GrantRow({
  grant,
  onRevoke,
  disabled,
}: {
  grant: PageGrant
  onRevoke: () => void
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
            {grants.map((g) => (
              <GrantRow
                key={`${g.subjectType}:${g.subjectId}:${g.level}`}
                grant={g}
                disabled={busy}
                onRevoke={() => setRevokeTarget(g)}
              />
            ))}
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
              Revoke this grant
            </AlertDialogTitle>
            <AlertDialogDescription className="text-xs">
              Remove <strong>{revokeTarget?.level}</strong> on <strong>{slug}</strong> from{" "}
              <strong>
                {revokeTarget?.subjectType}/{revokeTarget?.subject}
              </strong>
              ? The change is journalled with you as the actor, and it takes effect on the next read.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel className="h-7 text-xs">Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="h-7 bg-destructive text-xs text-destructive-foreground hover:bg-destructive/90"
              disabled={revoke.isPending}
              onClick={() => {
                if (!revokeTarget) return
                revoke.mutate({
                  subjectType: revokeTarget.subjectType,
                  subject: revokeTarget.subject,
                  level: revokeTarget.level,
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
            <GeneralCard workspaceId={workspaceId} slug={slug} page={page} />
          </div>
        </div>
      </div>
    </div>
  )
}
