"use client"

/**
 * A page for somebody with no account (PRD `docs/prd/pages.md` §7.3).
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
import { EmptyState } from "@/components/layout/empty-state"
import { Skeleton } from "@/components/ui/skeleton"
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
}

export function PublicPageView({
  status,
  page,
  message,
  submitting,
  onSubmitPassword,
  now,
}: PublicPageViewProps) {
  return (
    <div className="min-h-screen bg-background">
      <div className="mx-auto flex max-w-[1200px] flex-col gap-5 px-4 py-8 md:px-6 md:py-10">
        <PublicPageBody
          status={status}
          page={page}
          message={message}
          submitting={submitting}
          onSubmitPassword={onSubmitPassword}
          now={now}
        />
      </div>
    </div>
  )
}

function PublicPageBody({
  status,
  page,
  message,
  submitting,
  onSubmitPassword,
  now,
}: PublicPageViewProps) {
  if (status === "loading") {
    return (
      <div className="flex flex-col gap-4" data-slot="public-page-loading">
        <Skeleton className="h-8 w-64" />
        <div className="grid grid-cols-1 gap-4 md:grid-cols-12">
          <Skeleton className="h-40 md:col-span-6" />
          <Skeleton className="h-40 md:col-span-6" />
        </div>
      </div>
    )
  }

  if (status === "password") {
    return <PublicPasswordForm message={message} submitting={submitting} onSubmit={onSubmitPassword} />
  }

  if (status === "unavailable" || status === "error" || !page) {
    // One sentence for every unavailable case, because the server gives one
    // answer for all of them (§7.3.1): expired, revoked, mistyped and never
    // existed are facts about a workspace this reader is outside of.
    return (
      <EmptyState
        icon={CONCEPT_ICON.pages}
        title="This link is not available"
        description={
          message?.trim() ||
          "It may have expired, or it may have been withdrawn. Ask whoever shared it for a new one."
        }
      />
    )
  }

  return (
    <>
      <header className="flex flex-col gap-1" data-slot="public-page-header">
        <h1 className="text-xl font-semibold text-foreground">{page.name}</h1>
        {page.description ? (
          <p className="max-w-[70ch] text-sm text-muted-foreground">{page.description}</p>
        ) : null}
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
  const generated = toDate(page.generated_at)
  const expires = toDate(page.expires_at)
  return (
    <footer
      data-slot="public-page-footer"
      className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-border/60 pt-3 text-[11px] text-muted-foreground-soft"
    >
      {generated ? <span>Rendered {formatInstant(generated, now)}</span> : null}
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
    <form
      data-slot="public-password-form"
      className="mx-auto flex w-full max-w-sm flex-col gap-3 rounded-lg border border-border bg-card p-5"
      onSubmit={(e) => {
        e.preventDefault()
        onSubmit(password)
      }}
    >
      <div className="flex flex-col gap-1">
        <h1 className="text-base font-semibold text-foreground">This page is protected</h1>
        <p className="text-sm text-muted-foreground">
          Enter the password you were given alongside the link.
        </p>
      </div>
      <label className="flex flex-col gap-1 text-xs text-muted-foreground" htmlFor="public-page-password">
        Password
        <input
          id="public-page-password"
          name="password"
          type="password"
          autoComplete="off"
          autoFocus
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          className="rounded-md border border-border bg-background px-2.5 py-2 text-sm text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring"
        />
      </label>
      {message ? (
        <p role="alert" className="text-xs text-destructive">
          {message}
        </p>
      ) : null}
      <button
        type="submit"
        disabled={submitting || password.length === 0}
        className="rounded-md bg-primary px-3 py-2 text-sm font-medium text-primary-foreground disabled:opacity-50"
      >
        {submitting ? "Checking…" : "Open the page"}
      </button>
    </form>
  )
}
