"use client"

import type { ReactNode } from "react"
import Link from "next/link"
import { AlertTriangle, ArrowRight, KeyRound, LayoutGrid, Lock, MessageSquare, Settings, ShieldCheck, UserRound, Workflow, Wrench, type LucideIcon } from "lucide-react"

import { CrewshipLogoMark } from "@/components/branding/crewship-logo"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { Button } from "@/components/ui/button"
import { useSessionSafe } from "@/hooks/use-auth"
import { useRestrictedSurfaces } from "@/hooks/use-access-mode"
import { useWorkspace } from "@/hooks/use-workspace"
import { RESTRICTED_SURFACE_PATHS, type RestrictedSurface } from "@/lib/restricted-surfaces"
import { cn } from "@/lib/utils"

/**
 * What a restricted member sees where their access ends: a screen outside it,
 * a private catalog with nothing granted, a catalog whose runtime is not
 * installed, or one that failed to load.
 *
 * Built from the same pieces as the Routines overview — the attention-strip
 * status card, the eyebrow row and DashboardCard — so it reads as part of the
 * product, not as an error page. It only words the server's answer: what is
 * reachable is decided by the API, and the screens listed are the server's
 * own `restricted_surfaces`. Asking for more access is the administrator's
 * business, so there is no request flow here.
 */
export type AccessNoticeKind = "no-access" | "not-installed" | "unavailable"

/** The member's side of the notice, read once by the page that hosts it. */
export interface AccessContext {
  workspaceName: string | null
  role: string | null
  surfaces: RestrictedSurface[]
}

export function useAccessContext(): AccessContext {
  const { workspace } = useWorkspace()
  return {
    workspaceName: workspace?.name ?? null,
    role: workspace?.currentUserRole ?? null,
    surfaces: useRestrictedSurfaces() ?? [],
  }
}

const SURFACES: Record<RestrictedSurface, { label: string; detail: string; icon: LucideIcon }> = {
  issues: { label:"Issues", detail:"Read issues in projects shared with you",icon:LayoutGrid },
  chat: { label: "Chat", detail: "Discover agents shared with you", icon: MessageSquare },
  routines: { label: "Routines", detail: "Run routines shared with you", icon: Workflow },
  pages: { label: "Pages", detail: "Open Pages and run their actions", icon: LayoutGrid },
  account_security: { label: "Settings", detail: "Your account and sign-in security", icon: Settings },
}

const TONE: Record<AccessNoticeKind, { icon: LucideIcon; border: string; ink: string }> = {
  "no-access": { icon: Lock, border: "border-primary/25", ink: "text-primary-hover" },
  "not-installed": { icon: Wrench, border: "border-warn/25", ink: "text-warn" },
  unavailable: { icon: AlertTriangle, border: "border-destructive/25", ink: "text-destructive" },
}

function roleLabel(role: string | null): string | null {
  if (!role) return null
  return role.charAt(0).toUpperCase() + role.slice(1).toLowerCase()
}

export function AccessNotice({
  kind,
  title,
  description,
  context,
  current,
  onRetry,
  className,
}: {
  kind: AccessNoticeKind
  /** One line; the alert for "unavailable", the heading otherwise. */
  title: string
  /** The exact sentence the state is known by (tests and acceptance read it). */
  description: ReactNode
  context?: AccessContext
  /** The surface this notice stands on, marked in the list. */
  current?: RestrictedSurface
  onRetry?: () => void
  className?: string
}) {
  const { data } = useSessionSafe()
  const email = data?.user.email || null
  const tone = TONE[kind]
  const Icon = tone.icon
  const unavailable = kind === "unavailable"
  const surfaces = context?.surfaces ?? []
  const home = surfaces.includes("chat") ? "chat" : surfaces.find((s) => s !== current)
  const role = roleLabel(context?.role ?? null)

  return (
    <div data-testid="access-notice" data-kind={kind} className={cn("@container/access flex flex-col gap-3", className)}>
      <section className={cn("overflow-hidden rounded-xl border bg-card", tone.border)}>
        <div className="flex items-center justify-between gap-3 border-b border-border/60 px-4 py-3">
          <div className="flex min-w-0 items-center gap-2">
            <Icon className={cn("h-4 w-4 shrink-0", tone.ink)} aria-hidden />
            {unavailable
              ? <p role="alert" className="text-body font-semibold">{title}</p>
              : <h2 className="text-body font-semibold">{title}</h2>}
          </div>
          {unavailable && onRetry ? (
            <Button variant="ghost" size="sm" className="h-7 text-label text-primary-hover" onClick={onRetry}>Try again</Button>
          ) : home && home !== current ? (
            <Link href={RESTRICTED_SURFACE_PATHS[home]} className="inline-flex shrink-0 items-center gap-1 whitespace-nowrap text-label text-primary-hover hover:underline">
              Go to {SURFACES[home].label} <ArrowRight className="h-3.5 w-3.5" aria-hidden />
            </Link>
          ) : null}
        </div>
        <div className="flex items-start gap-4 px-4 py-4">
          <CrewshipLogoMark variant="navy-white" className={cn("h-10 w-10 shrink-0", unavailable && "opacity-60")} aria-hidden />
          <div className="min-w-0 space-y-1 text-body text-muted-foreground">
            <p>{description}</p>
            <p className="text-label text-muted-foreground-soft">
              {unavailable
                ? "Something went wrong on our side — this is not about your access."
                : kind === "not-installed"
                  ? "An administrator of this server can install it."
                  : "Your workspace administrator decides what you can open — ask them if you need it."}
            </p>
          </div>
        </div>
      </section>

      <div className="mt-1 flex flex-wrap items-center gap-2">
        <KeyRound className="h-3.5 w-3.5 text-primary-hover" aria-hidden />
        <h2 className="eyebrow">Your access</h2>
        <span className="font-mono text-[11px] text-muted-foreground-soft">
          {["Guest", role, context?.workspaceName].filter(Boolean).join(" · ")}
        </span>
      </div>

      <div className="grid grid-cols-1 gap-3 @3xl/access:grid-cols-5">
        <DashboardCard title="What you can open" icon={LayoutGrid} hint={`${surfaces.length} ${surfaces.length === 1 ? "screen" : "screens"}`} className="@3xl/access:col-span-3">
          {surfaces.length === 0 ? (
            <p className="text-body text-muted-foreground">Nothing yet.</p>
          ) : (
            <ul>
              {surfaces.map((surface) => {
                const s = SURFACES[surface]
                const here = surface === current
                return (
                  <li key={surface}>
                    <Link
                      href={RESTRICTED_SURFACE_PATHS[surface]}
                      aria-current={here ? "page" : undefined}
                      className="group grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2.5 rounded-md border-b border-border/50 px-2 py-2 last:border-0 hover:bg-foreground/[0.025]"
                    >
                      <span className="icon-tile inline-flex h-7 w-7 items-center justify-center rounded-md" aria-hidden><s.icon className="h-3.5 w-3.5" /></span>
                      <span className="min-w-0">
                        <span className="block text-body text-foreground">{s.label}</span>
                        <span className="block truncate text-label text-muted-foreground">{s.detail}</span>
                      </span>
                      <span className="font-mono text-[11px] text-muted-foreground-soft group-hover:text-primary-hover">{here ? "you are here" : "Open →"}</span>
                    </Link>
                  </li>
                )
              })}
            </ul>
          )}
        </DashboardCard>

        <DashboardCard title="How access works" icon={ShieldCheck} className="@3xl/access:col-span-2">
          <ul className="space-y-2.5 text-label text-muted-foreground">
            <li className="flex gap-2.5"><Lock className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden />Administrators choose which resources and operations are shared with you.</li>
            <li className="flex gap-2.5"><ShieldCheck className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden />Runs you start, and their results, are visible to you.</li>
            {email && <li className="flex gap-2.5"><UserRound className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden />Signed in as <span className="truncate text-foreground">{email}</span></li>}
          </ul>
        </DashboardCard>
      </div>
    </div>
  )
}
