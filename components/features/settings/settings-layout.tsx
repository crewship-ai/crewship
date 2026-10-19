"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { motion, AnimatePresence } from "motion/react"
import {
  Activity, ArrowLeft, Building, KeyRound, Link2, MonitorSmartphone, Palette, Settings as SettingsIcon, Shield, User, Volume2,
  type LucideIcon,
} from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Button } from "@/components/ui/button"
import { PageError } from "@/components/layout/page-state"
import { useIsMobile } from "@/hooks/use-mobile"
import { SettingsPageHead } from "./shared"
import { useAuth } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAccessMode, useRestrictedSurfaces } from "@/hooks/use-access-mode"
import { OwnAccessCard } from "@/components/features/settings/own-access"
import { apiFetch } from "@/lib/api-fetch"
import { SubBar } from "@/components/layout/sub-bar"
import { useCloseSecondaryPanelSheet } from "@/components/layout/secondary-panel"
import { SettingsNav, isSettingsSectionVisible } from "./settings-nav"
import { NotificationSoundSettings } from "./sections/notification-sounds-section"
import { ProfileSection } from "./sections/profile-section"
import { AppearanceSection } from "./sections/appearance-section"
import { SessionsSection } from "./sections/sessions-section"
import { PrivacySection } from "./sections/privacy-section"
import { GeneralSection } from "./sections/general-section"
import { useFocusSettingsCard } from "./use-focus-settings-card"
import { AccessSecretsSection } from "./sections/access-secrets-section"
import { SectionMoved } from "./sections/section-moved"
import { PageSaveBar, PageSaveProvider } from "@/components/ui/page-save-bar"

interface Org {
  id: string
  name: string
  slug: string
  logo_url?: string | null
  preferred_language: string | null
  _count: { crews: number; agents: number; members: number }
}

interface Member {
  id: string
  role: string
  created_at: string
  access_mode?: "trusted" | "restricted"
  user: { id: string; email: string; full_name: string | null; avatar_url: string | null }
}

// The head of each section (Harbor 2): the nav row's icon in a 40 px tile,
// the title, and one sentence saying what the section is for.
const sectionTitles: Record<string, { title: string; description?: string; icon: LucideIcon }> = {
  profile: { title: "Profile", icon: User, description: "Your name, picture and password. Visible to everyone in your workspaces." },
  appearance: { title: "Appearance", icon: Palette, description: "Applies in this browser at once. With Sync on, the same choices follow you to every device." },
  sounds: { title: "Notification sounds", icon: Volume2, description: "Your Chat and Inbox sounds for this account and workspace, saved in this browser." },
  sessions: { title: "Sessions & tokens", icon: MonitorSmartphone, description: "Everything that can sign in as you right now. Don’t recognise something? End it." },
  privacy: { title: "Privacy", icon: Shield, description: "Agent memory about you (peer cards, opt-out, deletion)" },
  general: { title: "General", icon: Building, description: "Workspace identity and defaults. Admins and owners can change them." },
  connections: { title: "Crew links", icon: Link2, description: "Cross-crew communication links" },
  "access-secrets": { title: "Access & Secrets", icon: KeyRound, description: "Who may see secret values. Only an owner can turn reveal on." },
  audit: { title: "Audit Log", icon: Activity, description: "Track workspace activity" },
}

/**
 * Sections that used to live here and now live on the page that owns the
 * object they configure.
 *
 * They keep no nav row — a row promises a pane, and these have none — but the
 * `?tab=` key stays resolvable, because a bookmark, a doc link or a toolbar
 * entry written before the move must land where the section went rather than
 * on the Profile fallback that an unknown key gets.
 */
export const MOVED_SECTIONS: Record<string, { href: string; label: string }> = {
  // One place to manage a channel. Two surfaces for the same object drift, and
  // an admin auditing "what is this instance wired into" would have to check
  // both.
  notifications: {
    href: "/integrations?tab=notifications&section=connections",
    label: "Integrations",
  },
  "notification-prefs": {
    href: "/integrations?tab=notifications&section=preferences",
    label: "Integrations",
  },
  // Container limits, network policy and allowed domains are crew config, and
  // the crew's own Settings tab already edits them — plus the MCP servers,
  // image, escalations and `allow_private_endpoints` this copy never had.
  crews: { href: "/crews", label: "Crews & Agents" },
  // Nested pages of their own (DrillPage): too big for a card, and each needs
  // its own side panel. The Settings row opens them; old ?tab= links follow.
  connections: { href: "/settings/crew-links", label: "Crew links" },
  audit: { href: "/settings/audit", label: "Audit log" },
  // A ?member= on the old link survives the move (movedSectionHref): the ⌘K
  // palette and other pages linked to one person, not to the roster.
  members: { href: "/settings/members", label: "Members" },
  hooks: { href: "/settings/hooks", label: "Lifecycle hooks" },
}

// Resolve the initial tab from the URL `?tab=` param, falling back to
// "profile" for missing/unknown values. This is what makes deep-links like
// /settings?tab=audit (from the command palette and toolbar nav) land on the
// right section instead of always opening Profile.
export function initialSettingsTab(search: string): string {
  const t = new URLSearchParams(search).get("tab")
  return t && (t in sectionTitles || t in MOVED_SECTIONS) ? t : "profile"
}

/**
 * The user whose row a `?member=` deep link names, or "".
 *
 * The ⌘K palette can find a person by name or email, and landing on the
 * roster without saying which of them you picked makes the caller search a
 * second time by eye. Members is its own page now; this keeps an old
 * /settings?tab=members&member=… link naming the same person there.
 */
export function initialFocusedMember(search: string): string {
  return new URLSearchParams(search).get("member") ?? ""
}

/**
 * Where a moved section's `?tab=` link goes, or null if `tab` did not move.
 * Members carries its `?member=` along; the others have nothing to carry.
 */
export function movedSectionHref(tab: string, search: string): string | null {
  const moved = MOVED_SECTIONS[tab]
  if (!moved) return null
  const member = tab === "members" ? initialFocusedMember(search) : ""
  return member ? `${moved.href}?member=${encodeURIComponent(member)}` : moved.href
}

/** The card a `?card=<slug>` deep link names (see settingsCardSlug), or "". */
export function initialFocusedCard(search: string): string {
  return new URLSearchParams(search).get("card") ?? ""
}

export function SettingsLayout() {
  // A restricted session gets account security and nothing else (#2861): the
  // workspace, members and every other section are off its allowlist, so not
  // even their requests are made.
  const surfaces = useRestrictedSurfaces()
  const mode = useAccessMode()
  const { error, refresh } = useWorkspace()
  if (mode === "loading") return <div className="p-6" role="status">{error ? <><p>Account access unavailable.</p><Button onClick={() => void refresh()}>Retry</Button></> : "Loading account access…"}</div>
  if (surfaces) return <RestrictedAccountSettings />
  // One Save for the whole page: every card's edits join the floating bar,
  // and leaving with edits pending asks first (components/ui/page-save-bar).
  return (
    <PageSaveProvider>
      <SettingsPage />
    </PageSaveProvider>
  )
}

function RestrictedAccountSettings() {
  const { session, signOut } = useAuth()
  return (
    <div key={session?.user?.id} className="mx-auto w-full max-w-[820px] space-y-4 p-4 md:p-6">
      <SettingsPageHead level={1} icon={Shield} title="Account security" description="Your password, the devices signed in as you and your CLI tokens." />
      {/* One page, as before the Sessions split: password, then sessions and
          token revoke. space-y-5 is the gap ProfileSection used between the
          same cards when they lived in one component. The ?tab= param is
          ignored here — /settings?tab=sessions from the user menu lands on
          this same page, which already shows sessions. */}
      <div className="space-y-5">
        <OwnAccessCard />
        <ProfileSection restricted userName={session?.user?.name} userEmail={session?.user?.email} />
        <SessionsSection
          restricted
          sessionExpires={session?.expires}
          onSignOut={() => signOut().then(() => { window.location.href = "/login" })}
        />
      </div>
    </div>
  )
}

function SettingsPage() {
  const { session, signOut } = useAuth()
  const { workspaceId, role, loading: wsLoading } = useWorkspace()

  // Read from useSearchParams, NOT window.location.search.
  //
  // The two disagree during a client-side navigation: the App Router renders
  // the new route before window.location has been updated, so an initializer
  // reading window.location.search on arrival from /crews or the ⌘K palette
  // saw the OLD url and fell through to "profile". Every settings deep link in
  // the product landed on Profile — ?tab=audit, ?tab=members, all of them —
  // while a full page load at the same URL worked, which is what made it look
  // like the links were fine.
  //
  // And read on every CHANGE, not once on mount. A once-only initializer
  // handled arrival but swallowed the next link: standing on
  // /settings?tab=members and following another settings link changed the URL
  // and nothing else, because this layout does not unmount between them.
  //
  // Guarded on the query string itself so it stays compatible with
  // setActiveTab, which writes the URL through history.replaceState precisely
  // so it does NOT navigate: an unchanged string re-applies nothing, and
  // replaceState does not notify useSearchParams anyway. Local state stays
  // authoritative for clicks; this only follows real navigations.
  const searchParams = useSearchParams()
  const search = searchParams.toString()
  const [requestedTab, _setActiveTab] = useState(() => initialSettingsTab(search))
  const [focusedCard, setFocusedCard] = useState(() => initialFocusedCard(search))
  const appliedSearch = useRef(search)

  useEffect(() => {
    if (appliedSearch.current === search) return
    appliedSearch.current = search
    _setActiveTab(initialSettingsTab(search))
    setFocusedCard(initialFocusedCard(search))
  }, [search])

  const closePanelSheet = useCloseSecondaryPanelSheet()

  // Phone (PNL-05): /settings is the list of sections; a pick opens that
  // section with a Back to the list. A link that names a section (?tab=)
  // opens it straight away. Desktop and tablet keep list and section side by
  // side and never look at this.
  const isMobile = useIsMobile()
  const [phoneDetail, setPhoneDetail] = useState(() => new URLSearchParams(search).has("tab"))
  useEffect(() => {
    if (new URLSearchParams(search).has("tab")) setPhoneDetail(true)
  }, [search])
  const phoneList = isMobile && !phoneDetail

  // A deep link can name a section this role cannot see (/settings?tab=audit
  // as a MEMBER). Rendering it gives a blank pane with no nav row to escape
  // from, so fall back to Profile — the one section every role owns. Derived
  // rather than corrected in an effect: an effect would still flash the hidden
  // pane for a frame. While the role is loading nothing is judged, so an
  // OWNER's deep link survives the round trip.
  //
  // A section that MOVED to a page of its own is the exception: it is
  // forwarded (below) whatever the role, and that page judges the role
  // itself — /settings/audit says "You can't open Audit log" — so the old
  // link ends on the same sentence instead of silently opening Profile.
  const activeTab =
    wsLoading || requestedTab in MOVED_SECTIONS || isSettingsSectionVisible(requestedTab, role) ? requestedTab : "profile"
  useFocusSettingsCard(focusedCard, activeTab)

  // A section that moved forwards immediately — before the workspace fetch,
  // before the role is known, before anything renders — so a stale link costs
  // one frame rather than a page of the wrong content.
  const moved = MOVED_SECTIONS[activeTab]
  const movedHref = movedSectionHref(activeTab, search)
  const router = useRouter()
  useEffect(() => {
    if (movedHref) router.replace(movedHref)
  }, [movedHref, router])

  // The active tab used to be mirrored into the zustand store on every change
  // (plus an initial-set / clear-on-unmount effect) for one reader: the global
  // top bar's "Settings / <tab>" breadcrumb. The sub-bar above reads local
  // state directly, so the store round-trip is gone along with the breadcrumb.
  const setActiveTab = useCallback((tab: string) => {
    _setActiveTab(tab)
    // A card belongs to the tab the link opened; another tab has none.
    setFocusedCard("")
    // Keep the URL in sync so the active tab is shareable/bookmarkable and
    // the back button works, without triggering a route navigation.
    if (typeof window !== "undefined") {
      const url = new URL(window.location.href)
      url.searchParams.set("tab", tab)
      url.searchParams.delete("card")
      window.history.replaceState(null, "", url.toString())
    }
  }, [])

  const [org, setOrg] = useState<Org | null>(null)
  const [members, setMembers] = useState<Member[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  // Bumped by the error state's Retry: the workspace read runs again.
  const [reloadKey, setReloadKey] = useState(0)

  useEffect(() => {
    // No workspace fetch for a tab we are already leaving.
    if (!workspaceId || wsLoading || moved) return
    let cancelled = false

    async function fetchData() {
      setLoading(true)
      setError(null)
      try {
        const [orgRes, membersRes] = await Promise.all([
          apiFetch(`/api/v1/workspaces/${workspaceId}?workspace_id=${workspaceId}`),
          apiFetch(`/api/v1/workspaces/${workspaceId}/members?workspace_id=${workspaceId}`),
        ])
        if (!orgRes.ok) { setError("Couldn’t load this workspace"); return }
        const orgData = (await orgRes.json()) as Org
        if (!cancelled) setOrg(orgData)
        if (membersRes.ok) {
          const membersData = (await membersRes.json()) as Member[]
          if (!cancelled) setMembers(membersData)
        }
      } catch {
        if (!cancelled) setError("Couldn’t load settings")
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    fetchData()
    return () => { cancelled = true }
  }, [workspaceId, wsLoading, moved, reloadKey])

  const handleOrgUpdated = useCallback((updated: { name: string; slug: string; preferred_language: string | null }) => {
    setOrg((prev) => prev ? { ...prev, ...updated } : prev)
  }, [])

  const isLoading = wsLoading || loading
  const section = sectionTitles[activeTab]

  function renderContent() {
    // Before the skeleton: a moved section has nothing to load.
    if (moved) return <SectionMoved href={movedHref ?? moved.href} label={moved.label} />

    if (activeTab === "sounds" && !wsLoading) return <NotificationSoundSettings />
    // Personal sections that read nothing from the workspace fetch: no
    // skeleton while it runs, and a failed org load does not hide them.
    if (activeTab === "appearance" && !wsLoading) return <AppearanceSection />
    if (activeTab === "sessions" && !wsLoading) {
      return (
        <SessionsSection
          sessionExpires={session?.expires}
          onSignOut={() => signOut().then(() => { window.location.href = "/login" })}
        />
      )
    }

    if (isLoading) return <SettingsCardsSkeleton />

    if (error) {
      return (
        <PageError
          className="px-0 py-0"
          title={error}
          description="Your account sections (Appearance, Notification sounds, Sessions & tokens) still work."
          onRetry={() => setReloadKey((k) => k + 1)}
        />
      )
    }

    if (activeTab === "profile") {
      const currentMember = members.find((m) => m.user.id === session?.user?.id)
      return (
        <ProfileSection
          userName={session?.user?.name}
          userEmail={session?.user?.email}
          userAvatarUrl={currentMember?.user?.avatar_url}
          role={role}
          workspaceName={org?.name}
          joinedAt={currentMember?.created_at}
        />
      )
    }
    if (activeTab === "privacy" && workspaceId) {
      return <PrivacySection workspaceId={workspaceId} />
    }
    if (activeTab === "access-secrets" && workspaceId) {
      return <AccessSecretsSection workspaceId={workspaceId} role={role} members={members} />
    }
    if (activeTab === "general" && org && workspaceId) {
      return (
        <GeneralSection
          workspaceId={workspaceId}
          orgName={org.name}
          orgSlug={org.slug}
          logoUrl={org.logo_url ?? null}
          preferredLanguage={org.preferred_language}
          agentCount={org._count?.agents ?? 0}
          crewCount={org._count?.crews ?? 0}
          memberCount={org._count?.members ?? 0}
          role={role}
          onUpdated={handleOrgUpdated}
          onLogoChange={(logo_url) => setOrg((prev) => (prev ? { ...prev, logo_url } : prev))}
          onDelete={() => { window.location.href = "/" }}
        />
      )
    }
    return null
  }

  function handleTabChange(tab: string) {
    setActiveTab(tab)
    setPhoneDetail(true)
    // On a phone the pick closes the panel sheet: the section is what comes next.
    closePanelSheet()
  }

  // Phone: back from a section to the list of sections, dropping ?tab= so a
  // reload stays on the list.
  function backToList() {
    setPhoneDetail(false)
    if (typeof window !== "undefined") {
      const url = new URL(window.location.href)
      url.searchParams.delete("tab")
      url.searchParams.delete("card")
      window.history.replaceState(null, "", url.toString())
    }
  }

  // A moved section shows its one-line notice while the redirect lands; it has
  // no head of its own.
  const showHead = section != null && !moved

  return (
    <div className="flex flex-col h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))]">
      {/* Settings was the last page with no sub-bar: its identity lived in the
          global top bar as a "Settings / Profile" breadcrumb, which made it the
          one page whose top bar was not plain "Crewship". The identity belongs
          here, in the same shape Admin uses — page name, then active section. */}
      <SubBar
        icon={SettingsIcon}
        title="Settings"
        section={phoneList ? undefined : section?.title}
        ariaLabel="Settings"
        leading={isMobile && !phoneList ? (
          <Button
            variant="ghost"
            size="icon-sm"
            className="kit-tap -ml-1"
            aria-label="Back to Settings"
            onClick={backToList}
          >
            <ArrowLeft className="h-3.5 w-3.5" />
          </Button>
        ) : undefined}
      />

      <div className="flex flex-1 min-h-0">
      {/* The sections panel (SecondaryPanel): a column on a desktop; on a
          phone the page itself while no section is open (list → detail). */}
      <SettingsNav
        activeTab={phoneList ? "" : activeTab}
        onTabChange={handleTabChange}
        workspaceName={org?.name}
        role={role}
        phoneInline={phoneList}
      />

      {/* Content */}
      {!phoneList && (
      <div className="relative flex-1 min-h-0 overflow-hidden">
        <ScrollArea className="h-full">
          {/* pb-24: room under the last card for the floating Save bar. */}
          <div className="max-w-[820px] mx-auto p-4 pb-24 md:p-6 md:pb-24">
            {/* Section content */}
            <AnimatePresence mode="wait">
              <motion.div
                key={activeTab}
                initial={{ opacity: 0, y: 6 }}
                animate={{ opacity: 1, y: 0 }}
                exit={{ opacity: 0, y: -6 }}
                transition={{ duration: 0.12, ease: "easeOut" }}
                className="space-y-4"
              >
                {showHead && (
                  <SettingsPageHead icon={section.icon} title={section.title} description={section.description} className="mb-5" />
                )}
                {renderContent()}
              </motion.div>
            </AnimatePresence>
          </div>
        </ScrollArea>
        <PageSaveBar />
      </div>
      )}
      </div>
    </div>
  )
}

/** Loading: the shape of what is coming — a card header and its rows, twice. */
function SettingsCardsSkeleton() {
  return (
    <div className="space-y-4" role="status" aria-busy="true" aria-label="Loading settings">
      {[3, 2].map((rows, i) => (
        <div key={i} className="overflow-hidden rounded-card border border-border bg-card">
          <div className="flex items-center gap-3 border-b border-border px-4 py-2.5">
            <Skeleton className="h-8 w-8 rounded-lg" />
            <div className="flex-1 space-y-1.5">
              <Skeleton className="h-3 w-32" />
              <Skeleton className="h-2.5 w-48" />
            </div>
          </div>
          {Array.from({ length: rows }, (_, r) => (
            <div key={r} className="flex items-center gap-4 border-b border-border px-4 py-3 last:border-b-0">
              <Skeleton className="h-3 w-28" />
              <span className="flex-1" />
              <Skeleton className="h-8 w-40" />
            </div>
          ))}
        </div>
      ))}
    </div>
  )
}
