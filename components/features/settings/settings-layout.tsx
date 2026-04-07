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
import { SettingsNav } from "./settings-nav"
import { ProfileSection } from "./sections/profile-section"
import { GeneralSection } from "./sections/general-section"
import { MembersSection } from "./sections/members-section"
import { RolesSection } from "./sections/roles-section"
import { BillingSection } from "./sections/billing-section"
import { DangerSection } from "./sections/danger-section"
import { CrewsContainersSection } from "./sections/crews-containers-section"
import { ConnectionsSection } from "./sections/connections-section"
import { CrewAuditSection } from "./sections/crew-audit-section"
import { Phase2Section } from "./sections/phase2-section"

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
  crews: { title: "Crews & Containers", description: "Manage crews and their container configuration" },
  connections: { title: "Connections", description: "Cross-crew communication links" },
  audit: { title: "Audit Log", description: "Track workspace activity" },
  general: { title: "General", description: "Workspace identity and preferences" },
  members: { title: "Members", description: "Manage workspace members" },
  roles: { title: "Roles & Permissions", description: "Permission matrix reference" },
  billing: { title: "Billing & Usage", description: "Workspace resource usage" },
  danger: { title: "Danger Zone", description: "Irreversible workspace actions" },
}

export function SettingsLayout() {
  const { session, signOut } = useAuth()
  const { workspaceId, role, loading: wsLoading } = useWorkspace()
  const { abilities } = useAbilities()

  const isMobile = useIsMobile()
  const [activeTab, setActiveTab] = useState("profile")
  const [mobileNavOpen, setMobileNavOpen] = useState(false)

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
          fetch(`/api/v1/workspaces/${workspaceId}?workspace_id=${workspaceId}`),
          fetch(`/api/v1/workspaces/${workspaceId}/members?workspace_id=${workspaceId}`),
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
        <div className="bg-card border border-red-500/20 rounded-lg p-6">
          <p className="text-[13px] text-red-400">{error}</p>
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
    if (activeTab === "crews" && workspaceId) {
      return <CrewsContainersSection workspaceId={workspaceId} />
    }
    if (activeTab === "connections" && workspaceId) {
      return <ConnectionsSection workspaceId={workspaceId} />
    }
    if (activeTab === "audit" && workspaceId) {
      return <CrewAuditSection workspaceId={workspaceId} />
    }
    if (activeTab === "general" && org && workspaceId) {
      return <GeneralSection workspaceId={workspaceId} orgName={org.name} orgSlug={org.slug} preferredLanguage={org.preferred_language} onUpdated={handleOrgUpdated} />
    }
    if (activeTab === "members" && workspaceId) {
      return <MembersSection members={members} workspaceId={workspaceId} canInvite={abilities.can("create", "Member")} onRefresh={handleRefresh} />
    }
    if (activeTab === "roles") {
      return <RolesSection />
    }
    if (activeTab === "billing" && org) {
      return <BillingSection agentCount={org._count?.agents ?? 0} crewCount={org._count?.crews ?? 0} memberCount={org._count?.members ?? 0} workspaceName={org.name} />
    }
    if (activeTab === "danger" && workspaceId) {
      return <DangerSection workspaceId={workspaceId} role={role} />
    }
    return <Phase2Section />
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
          <div className="max-w-3xl mx-auto px-4 sm:px-8 py-6 sm:py-8">
            {/* Mobile nav trigger + section header */}
            <div className="flex items-start gap-3 mb-6">
              {isMobile && (
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8 shrink-0 mt-0.5"
                  onClick={() => setMobileNavOpen(true)}
                >
                  <Menu className="h-4 w-4" />
                </Button>
              )}
              {section && (
                <div>
                  <h2 className="text-[18px] font-semibold text-foreground">{section.title}</h2>
                  {section.description && (
                    <p className="text-[13px] text-muted-foreground/50 mt-1">{section.description}</p>
                  )}
                </div>
              )}
            </div>

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
