"use client"

import { useCallback, useEffect, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { Skeleton } from "@/components/ui/skeleton"
import { ScrollArea } from "@/components/ui/scroll-area"
import { useSession } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAbilities } from "@/hooks/use-abilities"
import { useIsMobile } from "@/hooks/use-mobile"
import { SettingsContextBar, type Scope } from "./settings-context-bar"
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

export function SettingsLayout() {
  const { data: session } = useSession()
  const { workspaceId, role, loading: wsLoading } = useWorkspace()
  const { abilities } = useAbilities()
  const isMobile = useIsMobile()

  const [scope, setScope] = useState<Scope>("user")
  const [activeTab, setActiveTab] = useState("profile")
  const [navCollapsed, setNavCollapsed] = useState(false)

  const [org, setOrg] = useState<Org | null>(null)
  const [members, setMembers] = useState<Member[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [refreshKey, setRefreshKey] = useState(0)

  // Auto-collapse on mobile
  useEffect(() => {
    if (isMobile) setNavCollapsed(true)
  }, [isMobile])

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
        if (!cancelled) {
          setOrg(orgData)
        }
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

  function handleScopeChange(s: Scope) {
    setScope(s)
    setActiveTab(s === "user" ? "profile" : "crews")
  }

  const handleOrgUpdated = useCallback((updated: { name: string; slug: string; preferred_language: string | null }) => {
    setOrg((prev) => prev ? { ...prev, ...updated } : prev)
  }, [])

  const handleRefresh = useCallback(() => {
    setRefreshKey((k) => k + 1)
  }, [])

  const isLoading = wsLoading || loading

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

    // User tabs
    if (activeTab === "profile") {
      return (
        <ProfileSection
          userName={session?.user?.name}
          userEmail={session?.user?.email}
          role={role}
        />
      )
    }

    // Workspace tabs
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
      return (
        <GeneralSection
          workspaceId={workspaceId}
          orgName={org.name}
          orgSlug={org.slug}
          preferredLanguage={org.preferred_language}
          onUpdated={handleOrgUpdated}
        />
      )
    }

    if (activeTab === "members" && workspaceId) {
      return (
        <MembersSection
          members={members}
          workspaceId={workspaceId}
          canInvite={abilities.can("create", "Member")}
          onRefresh={handleRefresh}
        />
      )
    }

    if (activeTab === "roles") {
      return <RolesSection />
    }

    if (activeTab === "billing" && org) {
      return (
        <BillingSection
          agentCount={org._count.agents}
          crewCount={org._count.crews}
          memberCount={org._count.members}
          workspaceName={org.name}
        />
      )
    }

    if (activeTab === "danger" && workspaceId) {
      return <DangerSection workspaceId={workspaceId} role={role} />
    }

    // Phase 2 placeholders
    return <Phase2Section />
  }

  return (
    <div className="flex flex-col h-[calc(100vh-48px)] bg-background">
      {/* Context bar */}
      <SettingsContextBar
        scope={scope}
        onScopeChange={handleScopeChange}
        workspaceName={org?.name}
      />

      {/* Main layout */}
      <div className="flex-1 min-h-0 flex relative">
        {/* Nav */}
        <SettingsNav
          scope={scope}
          activeTab={activeTab}
          onTabChange={setActiveTab}
          collapsed={navCollapsed}
          onCollapsedChange={setNavCollapsed}
          isMobile={isMobile}
        />

        {/* Content */}
        <div className="flex-1 min-h-0 overflow-hidden">
          <ScrollArea className="h-full">
            <div className={`max-w-3xl mx-auto py-6 ${isMobile ? "px-4" : "px-8"}`}>
              <AnimatePresence mode="wait">
                <motion.div
                  key={activeTab}
                  initial={{ opacity: 0, x: -8 }}
                  animate={{ opacity: 1, x: 0 }}
                  exit={{ opacity: 0, x: 8 }}
                  transition={{ duration: 0.15, ease: "easeOut" }}
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
