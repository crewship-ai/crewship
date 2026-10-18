"use client"

/**
 * A page for somebody with no account (PRD `docs/specs/pages.md` §7.3).
 *
 * §7.3.1 calls this "a different product, not a permission level", and the
 * component reflects that: no sidebar, no rail, no breadcrumb, no workspace
 * switcher, no back-to-pages link — none of the app's chrome, because none of
 * it would work for a reader who has no session and nowhere to go.
 *
 * What it does share is the PANELS. The registry is the same one the internal
 * page uses, dispatched on the same closed enum, with `publicView` set — so a
 * failed panel says "Data are not current" and the internal reason never
 * reaches this tree, because the server never sent it (§7.3.2b).
 *
 * THE ONE RULE THIS FILE IS THE LAST LINE OF DEFENCE FOR: there are no buttons
 * in the panel grid, ever (§7.3.2 rule 1). It is not the FIRST line — the
 * server strips actions before serialisation and the public wire has no field
 * to carry one — and that ordering is deliberate: "hidden in CSS" is exactly
 * what the rule forbids. The absence here is the second lock, and
 * `__tests__/public-page-view.test.tsx` asserts it by rendering the grid and
 * counting the buttons in it.
 */

import * as React from "react"

import { cn } from "@/lib/utils"
import { LockKeyhole } from "lucide-react"

import { AUTH_BUTTON, AUTH_INPUT, AuthCard } from "@/components/branding/auth-card"
import { CreateSurfaceField } from "@/components/layout/create-surface"
import { EmptyState } from "@/components/layout/empty-state"
import { PageLoading, PageNotFound } from "@/components/layout/page-state"
import { Button } from "@/components/ui/button"
import { ConceptIcon } from "@/components/ui/concept-icon"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { PanelRenderer } from "@/components/features/pages/panels"
import { formatInstant, toDate } from "@/components/features/pages/panels/freshness"
import { spanClass } from "@/components/features/pages/page-view"
import type { PublicPage, PublicPanel, PublicPageStatus } from "./types"

export interface PublicPageViewProps {
  status: PublicPageStatus
  page: PublicPage | null
  message: string | null
  submitting: boolean
  onSubmitPassword: (password: string) => void
  /** Injected clock — absolute ages are computed, so a test can pin `now`. */
  now?: Date
  /** The loading state offers this after 10 s ("Still loading…" + Retry). */
  onRetry?: () => void
}

export function PublicPageView({
  status,
  page,
  message,
  submitting,
  onSubmitPassword,
  now,
  onRetry,
}: PublicPageViewProps) {
  // No page-level height of its own: the caller's frame (the public bar plus
  // this) decides how tall the page is, so the bar and the content never
  // fight over the viewport. 16 px gutter on a phone, 24 px from tablet.
  return (
    <main
      data-slot="public-page-frame"
      className="mx-auto flex w-full max-w-[1200px] flex-1 flex-col gap-5 px-4 py-6 md:px-6 md:py-8"
    >
      <PublicPageBody
        status={status}
        page={page}
        message={message}
        submitting={submitting}
        onSubmitPassword={onSubmitPassword}
        now={now}
        onRetry={onRetry}
      />
    </main>
  )
}

function PublicPageBody({
  status,
  page,
  message,
  submitting,
  onSubmitPassword,
  now,
  onRetry,
}: PublicPageViewProps) {
  if (status === "loading") {
    // The kit's card skeleton, the same one a dashboard shows while loading.
    return <PageLoading variant="cards" onRetry={onRetry} className="p-0" />
  }

  if (status === "password") {
    return <PublicPasswordForm message={message} submitting={submitting} onSubmit={onSubmitPassword} />
  }

  if (status === "unavailable" || status === "error" || !page) {
    // One sentence for every unavailable case, because the server gives one
    // answer for all of them (§7.3.1): expired, revoked, mistyped and never
    // existed are facts about a workspace this reader is outside of.
    // The server's own sentence is deliberately NOT shown: one fixed text,
    // no actions (the reader has no account and nowhere to go), so nothing
    // on screen can tell the four cases apart.
    return (
      <PageNotFound
        title="This link is not available"
        description="It may have expired or been withdrawn. Ask whoever shared it for a new one."
        className="p-0"
      />
    )
  }

  const generated = toDate(page.generated_at)
  return (
    <>
      <header className="flex items-center gap-3" data-slot="public-page-header">
        <span data-slot="public-page-tile" className="shrink-0">
          <ConceptIcon concept="pages" variant="chip" size="lg" />
        </span>
        <div className="min-w-0">
          <h1 className="text-title font-semibold text-foreground">{page.name}</h1>
          <p data-slot="public-page-meta" className="type-page-meta text-muted-foreground">
            {generated ? `Shared read-only · rendered ${formatInstant(generated, now)}` : "Shared read-only"}
          </p>
          {page.description ? (
            <p className="mt-1 max-w-[70ch] text-body text-muted-foreground">{page.description}</p>
          ) : null}
        </div>
      </header>

      <PublicPanelGrid page={page} now={now} />

      <PublicPageFooter page={page} now={now} />
    </>
  )
}

/**
 * The grid. Identical geometry to the internal page — `spanClass` is imported
 * rather than re-derived, because `col-span-${n}` is invisible to Tailwind's
 * scanner and a second copy of that map is a second chance to get it wrong.
 */
export function PublicPanelGrid({ page, now }: { page: PublicPage; now?: Date }) {
  if (page.panels.length === 0) {
    return (
      <EmptyState
        icon={CONCEPT_ICON.pages}
        title="Nothing is shared here yet"
        description="This link is live, but no panel on it has been shared. Ask whoever sent it to mark a panel as public."
      />
    )
  }
  return (
    <div
      data-slot="public-panel-grid"
      className="grid grid-cols-1 gap-4 md:grid-cols-12"
    >
      {page.panels.map((panel) => (
        <div
          key={panel.id}
          data-slot="public-panel-cell"
          className={cn("@container/panel min-w-0", spanClass(panel.span))}
        >
          <PanelRenderer {...publicPanelProps(panel, page, now)} />
        </div>
      ))}
    </div>
  )
}

/**
 * Maps the public wire onto the panel registry's props.
 *
 * `failure` is hard-wired to `null`, and that is not belt-and-braces: the
 * server never sends a reason on this path (§7.3.2b), so there is nothing to
 * pass — and writing `null` here means a future field called `reason` cannot be
 * wired up by autocomplete. `publicView` is always true, so even a panel
 * component that grew a way to render internal vocabulary would not.
 *
 * `provenance.produced_at` is always populated: the panels read the age from
 * it, and §7.3.2b requires a public panel to carry when its data were produced
 * even when it failed.
 */
export function publicPanelProps(panel: PublicPanel, page: PublicPage, now?: Date) {
  return {
    panel: {
      id: panel.id,
      schema: panel.schema,
      title: panel.title,
      span: panel.span,
    },
    data: {
      state: panel.state,
      payload: panel.data ?? null,
      provenance: {
        produced_at: panel.produced_at ?? null,
        // Only ever present when the publisher opted provenance back in per
        // token (§7.3.2 rule 5); the server sends nothing here otherwise.
        producer: page.show_provenance ? (panel.provenance?.producer ?? null) : null,
        run_id: page.show_provenance ? (panel.provenance?.run_id ?? null) : null,
      },
      failure: null,
    },
    now,
    publicView: true as const,
  }
}

/**
 * The footer says two true things and no more: when this render happened, and
 * when the link stops working. The second is not a courtesy — a reader who
 * bookmarks a quarterly report deserves to know it expires, and they are
 * holding the token already, so telling them costs nothing.
 */
function PublicPageFooter({ page, now }: { page: PublicPage; now?: Date }) {
  const expires = toDate(page.expires_at)
  return (
    <footer
      data-slot="public-page-footer"
      className="type-page-meta flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-border/60 pt-3 text-muted-foreground-soft"
    >
      {expires ? <span data-slot="public-page-expiry">This link stops working {formatInstant(expires, now)}</span> : null}
    </footer>
  )
}

/**
 * The password prompt (§7.3.3).
 *
 * A form, not a login: there is no account behind it, no "forgot password", no
 * sign-up link and no email field — offering any of those would send a reader
 * looking for an account that does not exist. The password is submitted in the
 * request BODY by the hook; nothing here ever puts it in a URL.
 *
 * The refusal text is whatever the server said, and the server says the same
 * thing for a wrong password and an unknown link. This component must not
 * improve on that: "no such link" and "wrong password" being distinguishable is
 * exactly what §7.3.3 forbids, and it would be the UI, not the API, that broke
 * it.
 */
export function PublicPasswordForm({
  message,
  submitting,
  onSubmit,
}: {
  message: string | null
  submitting: boolean
  onSubmit: (password: string) => void
}) {
  const [password, setPassword] = React.useState("")
  return (
    <AuthCard
      icon={LockKeyhole}
      title="This artifact is protected"
      description="Enter the password you were given alongside the link."
      onSubmit={(e) => {
        e.preventDefault()
        onSubmit(password)
      }}
      formProps={{ autoComplete: "off" }}
      // The server's words, unchanged, in the refusal band.
      refusal={message || null}
      actions={
        <Button type="submit" disabled={submitting || password.length === 0} className={AUTH_BUTTON}>
          {submitting && <Spinner className="size-3.5" />}
          {submitting ? "Checking…" : "Open"}
        </Button>
      }
      className="mx-auto w-full md:max-w-[480px]"
    >
      <CreateSurfaceField label="Password" htmlFor="public-page-password">
        <Input
          id="public-page-password"
          name="password"
          type="password"
          autoComplete="off"
          autoFocus
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          className={AUTH_INPUT}
        />
      </CreateSurfaceField>
    </AuthCard>
  )
}
