"use client"

import { useCallback, useEffect, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { Menu } from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Button } from "@/components/ui/button"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { useAuth } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAbilities } from "@/hooks/use-abilities"
import { useIsMobile } from "@/hooks/use-mobile"
import { useAppStore } from "@/lib/store"
import { apiFetch } from "@/lib/api-fetch"
import { SettingsNav } from "./settings-nav"
import { ProfileSection } from "./sections/profile-section"
import { PrivacySection } from "./sections/privacy-section"
import { GeneralSection } from "./sections/general-section"
import { MembersSection } from "./sections/members-section"
import { CrewsContainersSection } from "./sections/crews-containers-section"
import { ConnectionsSection } from "./sections/connections-section"
import { CrewAuditSection } from "./sections/crew-audit-section"
import { AuxStatusSection } from "./sections/aux-status-section"

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
  crews: { title: "Crews & Containers", description: "Manage crews, resources and network policies" },
  "aux-models": { title: "Auxiliary Models", description: "Cheap fast models that power keeper evaluators (PRD §6 F3)" },
  connections: { title: "Connections", description: "Cross-crew communication links" },
  members: { title: "Members", description: "Team members and permissions" },
  audit: { title: "Audit Log", description: "Track workspace activity" },
}

export function SettingsLayout() {
  const { session, signOut } = useAuth()
  const { workspaceId, role, loading: wsLoading } = useWorkspace()
  const { abilities } = useAbilities()

  const isMobile = useIsMobile()
  const setSettingsTab = useAppStore((s) => s.setSettingsTab)
  const [activeTab, _setActiveTab] = useState("profile")
  const [mobileNavOpen, setMobileNavOpen] = useState(false)

  // Sync active tab to global store for toolbar breadcrumb
  const setActiveTab = useCallback((tab: string) => {
    _setActiveTab(tab)
    setSettingsTab(tab)
  }, [setSettingsTab])

  // Set initial tab and cleanup on unmount
  useEffect(() => {
    setSettingsTab(activeTab)
    return () => setSettingsTab(null)
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const [org, setOrg] = useState<Org | null>(null)
  const [members, setMembers] = useState<Member[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [refreshKey, setRefreshKey] = useState(0)

  useEffect(() => {
    if (!workspaceId) return
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
  }, [workspaceId, refreshKey])

  const handleOrgUpdated = useCallback((updated: { name: string; slug: string; preferred_language: string | null }) => {
    setOrg((prev) => prev ? { ...prev, ...updated } : prev)
  }, [])

  const handleRefresh = useCallback(() => {
    setRefreshKey((k) => k + 1)
  }, [])

  const isLoading = wsLoading || loading
  const section = sectionTitles[activeTab]

  function renderContent() {
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
    if (activeTab === "crews" && workspaceId) {
      return <CrewsContainersSection workspaceId={workspaceId} />
    }
    if (activeTab === "aux-models") {
      return <AuxStatusSection />
    }
    if (activeTab === "connections" && workspaceId) {
      return <ConnectionsSection workspaceId={workspaceId} />
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
          currentUserId={session?.user?.id}
          canInvite={abilities.can("create", "Member")}
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
    <div className="flex h-[calc(100vh-48px)]">
      {/* Desktop sidebar nav */}
      {!isMobile && (
        <SettingsNav
          activeTab={activeTab}
          onTabChange={handleTabChange}
          workspaceName={org?.name}
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
            />
          </SheetContent>
        </Sheet>
      )}

      {/* Content */}
      <div className="flex-1 min-h-0 overflow-hidden">
        <ScrollArea className="h-full">
          <div className="max-w-3xl mx-auto p-4 md:p-6 space-y-4">
            {/* Mobile nav trigger */}
            {isMobile && (
              <div>
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-7 gap-1.5 text-muted-foreground text-xs"
                  onClick={() => setMobileNavOpen(true)}
                >
                  <Menu className="h-3.5 w-3.5" />
                  {section?.title ?? "Settings"}
                </Button>
              </div>
            )}

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
  )
}
