"use client"

import { useEffect, useLayoutEffect, useRef } from "react"
import { usePathname, useRouter } from "next/navigation"
import { useIsMobile } from "@/hooks/use-mobile"
import { useRouteScrollRestoration } from "@/hooks/use-route-scroll-restoration"
import { MobileTabBar } from "@/components/layout/mobile-tab-bar"
import { useAuth, useSession } from "@/hooks/use-auth"
import { AppFrame } from "@/components/layout/app-frame"
import { AppBannerProvider } from "@/components/layout/app-banners"
import { PageLoading } from "@/components/layout/page-state"
import { loginRedirectUrl } from "@/lib/login-redirect"
import { BareNotFound, NotFoundInShell, useUnknownAppPath } from "@/components/layout/not-found-state"

import { AppSidebar } from "@/components/layout/app-sidebar"
import { AppToolbar } from "@/components/layout/app-toolbar"
import { RuntimeBanner } from "@/components/layout/runtime-banner"
import { UpdateBanner } from "@/components/layout/update-banner"
import { SidebarProvider, SidebarInset } from "@/components/ui/sidebar"
import { RealtimeProvider } from "@/hooks/use-realtime"
import { JournalLookupProvider } from "@/hooks/use-journal-lookup"
import { ActiveRoutineRunsProvider } from "@/hooks/use-active-routine-runs"
import { WorkspaceAvailabilityBoundary } from "@/components/layout/workspace-availability-boundary"
import { bindWorkspaceSession, useWorkspace } from "@/hooks/use-workspace"
import { useAccessMode, useAccessModeWatcher, useRestrictedSurfaces, useTrustedWorkspaceId } from "@/hooks/use-access-mode"
import { restrictedHome, restrictedPathAllowed } from "@/lib/restricted-surfaces"
import { RestrictedUnavailable } from "@/components/layout/restricted-unavailable"
import { RealtimeToasts } from "@/components/layout/realtime-toasts"
import { RealtimeStatusBanner } from "@/components/layout/realtime-status-banner"
import { NotificationSoundEvents } from "@/components/layout/notification-sound-events"
import { AppearanceSync } from "@/components/layout/appearance-sync"
import { WorkspaceAccentStyle } from "@/components/layout/workspace-accent"
import { SidebarPreference } from "@/components/layout/sidebar-preference"

export default function DashboardLayout({
  children,
}: {
  children: React.ReactNode
}) {
  const { status, data: session } = useSession()
  const { signOut } = useAuth()
  useLayoutEffect(() => {
    if (status !== "loading") bindWorkspaceSession(status === "authenticated" ? session?.user.id ?? null : null)
  }, [status, session?.user.id])
  const { error: workspacesError, refresh: refreshWorkspaces, workspaceUnavailable } = useWorkspace()
  // Session-level, not per workspace: one restricted membership puts the
  // whole account behind the restricted allowlist (see use-access-mode.ts).
  const accessMode = useAccessMode()
  const trusted = accessMode === "trusted"
  // Null until the session is known to be trusted, so the workspace-scoped
  // shell providers below send nothing a restricted session would be refused.
  const trustedWorkspaceId = useTrustedWorkspaceId()
  useAccessModeWatcher()
  const isMobile = useIsMobile()
  const pathname = usePathname()
  const independentPage = pathname === "/settings" || (pathname.startsWith("/admin") && session?.user.is_instance_admin === true)
  const scrollRef = useRef<HTMLDivElement>(null)

  /**
   * The page content scrolls inside this div, not the document, so the browser
   * has nothing to manage on a route change — a new screen opened already
   * scrolled halfway down the last one, and back opened a list at the top
   * instead of where the person left it. Nothing about that is visible on a
   * desktop, where a route change usually fits the viewport anyway.
   */
  useRouteScrollRestoration(scrollRef, pathname)

  // A restricted session opens only the screens the server lists (#2861).
  // Anything else renders the unavailable page instead of the screen, so the
  // screen's requests are never sent; the root goes to chat.
  const router = useRouter()
  // A path the app has no page for. The static-export backend answers it with
  // the root index.html, so this layout — not not-found.tsx — is what runs;
  // it shows the not-found state in place of the (dashboard) page.
  const unknownPath = useUnknownAppPath(pathname)
  const surfaces = useRestrictedSurfaces()
  // This exact page mounts only a known resource's server-checked boundary.
  // It does not offer Issues discovery or widen the navigation surfaces.
  const knownProjectManagement = pathname === "/issues/manage"
  const restrictedBlocked = !unknownPath && surfaces !== null && !knownProjectManagement && !restrictedPathAllowed(pathname, surfaces)
  const restrictedLanding = restrictedBlocked && pathname === "/" ? restrictedHome(surfaces) : null
  useEffect(() => {
    if (restrictedLanding) router.replace(restrictedLanding)
  }, [restrictedLanding, router])

  // Whether this tab has had a session: an explicit Log out lands on the plain
  // login page, a visitor who never signed in keeps the screen they asked for.
  const hadSession = useRef(false)
  if (status === "authenticated") hadSession.current = true
  useEffect(() => {
    // An unknown path is not a screen to come back to after login: signed out,
    // it is the bare not-found state, not a detour through the login page.
    if (status === "unauthenticated" && !unknownPath) {
      // Immediate redirect — no spinner, no delay
      window.location.replace(loginRedirectUrl(window.location.pathname, window.location.search, hadSession.current))
    }
  }, [status, unknownPath])

  if (status === "unauthenticated" && unknownPath) return <BareNotFound />
  if (status === "loading" || status === "unauthenticated") {
    return <AppFrame />
  }

  // Until the access mode is known, nothing below may mount: the shell and
  // every page start requests that a restricted session is refused. The
  // workspace list (allowlisted for everyone) is the only request in flight.
  if (accessMode === "loading" && !workspaceUnavailable && !independentPage) {
    return workspacesError ? (
      <AppFrame
        state="error"
        onRetry={() => { void refreshWorkspaces() }}
        onSignOut={() => { void signOut().then(() => { window.location.href = "/login" }) }}
      />
    ) : (
      <AppFrame />
    )
  }

  return (
    <RealtimeProvider>
    <AppBannerProvider>
      {/* Workspace-scoped journal lookup (crew/agent/mission id → name,
          slug, color). One fetch shared by every journal surface — the
          journal page, crew-journal, and the crew/agent activity feeds —
          which resolve ids to display names client-side. Must sit inside
          RealtimeProvider (it invalidates on crew/agent realtime events). */}
      <JournalLookupProvider workspaceId={trustedWorkspaceId}>
      {/* One workspace-scoped "active routine runs" subscription shared
          by the toolbar live chip and the /routines live surfaces —
          must sit inside RealtimeProvider (it consumes WS events). */}
      <ActiveRoutineRunsProvider>
        <SidebarProvider>
          {/* Syncs the rail's mode with Settings › Appearance and the account. */}
          <SidebarPreference />
          <AppSidebar />
          <SidebarInset>
            <AppToolbar />
            {/* One banner at a time (app-banners.tsx): each asks the queue
                for the slot, highest priority first. */}
            <RealtimeStatusBanner />
            {/* /system/runtime and /system/version are not on the
                restricted allowlist. */}
            {trusted && <RuntimeBanner />}
            {trusted && <UpdateBanner />}
            <div ref={scrollRef} className="flex-1 min-h-0 overflow-y-auto overflow-x-hidden bg-background rounded-t-2xl">
              <WorkspaceAvailabilityBoundary enabled={!independentPage}>
              {unknownPath
                ? <NotFoundInShell />
                : restrictedBlocked && !workspaceUnavailable
                ? restrictedLanding
                  ? <PageLoading />
                  : <RestrictedUnavailable home={surfaces ? restrictedHome(surfaces) : null} pathname={pathname} />
                : children}
              </WorkspaceAvailabilityBoundary>
            </div>
            {/* A flex sibling, not a fixed bar: it takes real space, so the
                scroll container above it needs no compensating padding and
                nothing can end up hidden underneath it. */}
            {isMobile && <MobileTabBar />}
          </SidebarInset>
        </SidebarProvider>
        <RealtimeToasts />
        {trusted && <NotificationSoundEvents />}
        {/* GET/PUT /api/v1/me/preferences are not on the restricted allowlist. */}
        {trusted && <AppearanceSync />}
        {/* Paints the workspace brand accent for a user who picked it; reads
            only the already-loaded workspace list, so no session is refused. */}
        <WorkspaceAccentStyle />
      </ActiveRoutineRunsProvider>
      </JournalLookupProvider>
    </AppBannerProvider>
    </RealtimeProvider>
  )
}
