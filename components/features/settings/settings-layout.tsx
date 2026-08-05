"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { motion, AnimatePresence } from "motion/react"
import { Menu, Settings as SettingsIcon } from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Button } from "@/components/ui/button"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { useAuth } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { useIsMobile } from "@/hooks/use-mobile"
import { apiFetch } from "@/lib/api-fetch"
import { SubBar } from "@/components/layout/sub-bar"
import { SettingsNav, isSettingsSectionVisible } from "./settings-nav"
import { ProfileSection } from "./sections/profile-section"
import { PrivacySection } from "./sections/privacy-section"
import { GeneralSection } from "./sections/general-section"
import { MembersSection } from "./sections/members-section"
import { ConnectionsSection } from "./sections/connections-section"
import { CrewAuditSection } from "./sections/crew-audit-section"
import { AccessSecretsSection } from "./sections/access-secrets-section"
import { SectionMoved } from "./sections/section-moved"

interface Org {
  id: string
  name: string
  slug: string
  preferred_language: string | null
  _count: { crews: number; agents: number; members: number }
}

interface Member {
  id: string
  role: string
  created_at: string
  user: { id: string; email: string; full_name: string | null; avatar_url: string | null }
}

// Section titles for the content area header
const sectionTitles: Record<string, { title: string; description?: string }> = {
  profile: { title: "Profile", description: "Your account details" },
  privacy: { title: "Privacy", description: "Agent memory about you (peer cards, opt-out, deletion)" },
  general: { title: "General", description: "Workspace identity, usage and settings" },
  connections: { title: "Crew links", description: "Cross-crew communication links" },
  members: { title: "Members", description: "Team members and permissions" },
  "access-secrets": {
    title: "Access & Secrets",
    description: "Who may read a stored secret in plaintext, and what each classification means",
  },
  audit: { title: "Audit Log", description: "Track workspace activity" },
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
 * Read alongside the tab, and for the same reason: the ⌘K palette can find a
 * person by name or email, and landing on the roster without saying which of
 * them you picked makes the caller search a second time by eye.
 */
export function initialFocusedMember(search: string): string {
  return new URLSearchParams(search).get("member") ?? ""
}

export function SettingsLayout() {
  const { session, signOut } = useAuth()
  const { workspaceId, role, loading: wsLoading } = useWorkspace()

  const isMobile = useIsMobile()

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
  const [focusedMember, setFocusedMember] = useState(() => initialFocusedMember(search))
  const appliedSearch = useRef(search)

  useEffect(() => {
    if (appliedSearch.current === search) return
    appliedSearch.current = search
    _setActiveTab(initialSettingsTab(search))
    setFocusedMember(initialFocusedMember(search))
  }, [search])

  const [mobileNavOpen, setMobileNavOpen] = useState(false)

  // A deep link can name a section this role cannot see (/settings?tab=audit
  // as a MEMBER). Rendering it gives a blank pane with no nav row to escape
  // from, so fall back to Profile — the one section every role owns. Derived
  // rather than corrected in an effect: an effect would still flash the hidden
  // pane for a frame. While the role is loading nothing is judged, so an
  // OWNER's deep link survives the round trip.
  const activeTab = wsLoading || isSettingsSectionVisible(requestedTab, role) ? requestedTab : "profile"

  // A section that moved forwards immediately — before the workspace fetch,
  // before the role is known, before anything renders — so a stale link costs
  // one frame rather than a page of the wrong content.
  const moved = MOVED_SECTIONS[activeTab]
  const router = useRouter()
  useEffect(() => {
    if (moved) router.replace(moved.href)
  }, [moved, router])

  // The active tab used to be mirrored into the zustand store on every change
  // (plus an initial-set / clear-on-unmount effect) for one reader: the global
  // top bar's "Settings / <tab>" breadcrumb. The sub-bar above reads local
  // state directly, so the store round-trip is gone along with the breadcrumb.
  const setActiveTab = useCallback((tab: string) => {
    _setActiveTab(tab)
    // Keep the URL in sync so the active tab is shareable/bookmarkable and
    // the back button works, without triggering a route navigation.
    if (typeof window !== "undefined") {
      const url = new URL(window.location.href)
      url.searchParams.set("tab", tab)
      window.history.replaceState(null, "", url.toString())
    }
  }, [])

  const [org, setOrg] = useState<Org | null>(null)
  const [members, setMembers] = useState<Member[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [refreshKey, setRefreshKey] = useState(0)

  useEffect(() => {
    // No workspace fetch for a tab we are already leaving.
    if (!workspaceId || moved) return
    let cancelled = false

    async function fetchData() {
      setLoading(true)
      setError(null)
      try {
        const [orgRes, membersRes] = await Promise.all([
          apiFetch(`/api/v1/workspaces/${workspaceId}?workspace_id=${workspaceId}`),
          apiFetch(`/api/v1/workspaces/${workspaceId}/members?workspace_id=${workspaceId}`),
        ])
        if (!orgRes.ok) { setError("Failed to load workspace"); return }
        const orgData = (await orgRes.json()) as Org
        if (!cancelled) setOrg(orgData)
        if (membersRes.ok) {
          const membersData = (await membersRes.json()) as Member[]
          if (!cancelled) setMembers(membersData)
        }
      } catch {
        if (!cancelled) setError("Failed to load settings")
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    fetchData()
    return () => { cancelled = true }
  }, [workspaceId, refreshKey, moved])

  const handleOrgUpdated = useCallback((updated: { name: string; slug: string; preferred_language: string | null }) => {
    setOrg((prev) => prev ? { ...prev, ...updated } : prev)
  }, [])

  const handleRefresh = useCallback(() => {
    setRefreshKey((k) => k + 1)
  }, [])

  const isLoading = wsLoading || loading
  const section = sectionTitles[activeTab]

  function renderContent() {
    // Before the skeleton: a moved section has nothing to load.
    if (moved) return <SectionMoved href={moved.href} label={moved.label} />

    if (isLoading) {
      return (
        <div className="space-y-4">
          <Skeleton className="h-[60px] rounded-lg" />
          <Skeleton className="h-[200px] rounded-lg" />
        </div>
      )
    }

    if (error) {
      return (
        <div className="bg-card border border-destructive/40 rounded-lg p-6">
          <p className="text-body text-destructive">{error}</p>
        </div>
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
          sessionExpires={session?.expires}
          onSignOut={() => signOut().then(() => { window.location.href = "/login" })}
        />
      )
    }
    if (activeTab === "privacy" && workspaceId) {
      return <PrivacySection workspaceId={workspaceId} />
    }
    if (activeTab === "connections" && workspaceId) {
      return <ConnectionsSection workspaceId={workspaceId} />
    }
    if (activeTab === "access-secrets" && workspaceId) {
      return <AccessSecretsSection workspaceId={workspaceId} role={role} members={members} />
    }
    if (activeTab === "audit" && workspaceId) {
      return <CrewAuditSection workspaceId={workspaceId} />
    }
    if (activeTab === "general" && org && workspaceId) {
      return (
        <GeneralSection
          workspaceId={workspaceId}
          orgName={org.name}
          orgSlug={org.slug}
          preferredLanguage={org.preferred_language}
          agentCount={org._count?.agents ?? 0}
          crewCount={org._count?.crews ?? 0}
          memberCount={org._count?.members ?? 0}
          role={role}
          onUpdated={handleOrgUpdated}
          onDelete={() => { window.location.href = "/" }}
        />
      )
    }
    if (activeTab === "members" && workspaceId) {
      return (
        <MembersSection
          members={members}
          workspaceId={workspaceId}
          focusUserId={focusedMember || undefined}
          currentUserId={session?.user?.id}
          callerRole={role ?? undefined}
          onRefresh={handleRefresh}
        />
      )
    }
    return null
  }

  function handleTabChange(tab: string) {
    setActiveTab(tab)
    setMobileNavOpen(false)
  }

  return (
    <div className="flex flex-col h-[calc(100vh-48px)]">
      {/* Settings was the last page with no sub-bar: its identity lived in the
          global top bar as a "Settings / Profile" breadcrumb, which made it the
          one page whose top bar was not plain "Crewship". The identity belongs
          here, in the same shape Admin uses — page name, then active section. */}
      <SubBar
        icon={SettingsIcon}
        title="Settings"
        section={section?.title}
        ariaLabel="Settings"
        leading={
          isMobile ? (
            <Button
              variant="ghost"
              size="icon-sm"
              className="h-7 w-7 -ml-1"
              aria-label="Open settings navigation"
              onClick={() => setMobileNavOpen(true)}
            >
              <Menu className="h-3.5 w-3.5" />
            </Button>
          ) : undefined
        }
      />

      <div className="flex flex-1 min-h-0">
      {/* Desktop sidebar nav */}
      {!isMobile && (
        <SettingsNav
          activeTab={activeTab}
          onTabChange={handleTabChange}
          workspaceName={org?.name}
          role={role}
        />
      )}

      {/* Mobile nav sheet */}
      {isMobile && (
        <Sheet open={mobileNavOpen} onOpenChange={setMobileNavOpen}>
          <SheetContent side="left" className="w-[260px] p-0">
            <SheetHeader className="sr-only">
              <SheetTitle>Settings Navigation</SheetTitle>
            </SheetHeader>
            <SettingsNav
              activeTab={activeTab}
              onTabChange={handleTabChange}
              workspaceName={org?.name}
              role={role}
            />
          </SheetContent>
        </Sheet>
      )}

      {/* Content */}
      <div className="flex-1 min-h-0 overflow-hidden">
        <ScrollArea className="h-full">
          <div className="max-w-3xl mx-auto p-4 md:p-6 space-y-4">
            {/* The mobile nav trigger used to live here, above the content, and
                doubled as the section label. Both jobs moved to the sub-bar. */}

            {/* Section content */}
            <AnimatePresence mode="wait">
              <motion.div
                key={activeTab}
                initial={{ opacity: 0, y: 6 }}
                animate={{ opacity: 1, y: 0 }}
                exit={{ opacity: 0, y: -6 }}
                transition={{ duration: 0.12, ease: "easeOut" }}
              >
                {renderContent()}
              </motion.div>
            </AnimatePresence>
          </div>
        </ScrollArea>
      </div>
      </div>
    </div>
  )
}
