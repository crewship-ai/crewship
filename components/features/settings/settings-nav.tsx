"use client"

import { useMemo, useState } from "react"
import {
  User, Building, Users,
  Link2, Activity, Shield, KeyRound,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { isManagerTier } from "@/lib/permissions/tiers"
import type { LucideIcon } from "lucide-react"
import {
  SidebarToolbar,
  SidebarSearch,
  SidebarSection,
  SidebarRow,
  SIDEBAR_WIDTH,
} from "@/components/layout/sidebar-kit"

interface NavItem {
  key: string
  label: string
  icon: LucideIcon
  badge?: string
  // Role gate for the row. Absent = visible to everyone.
  //
  // A row is hidden ONLY when the role can neither act nor usefully read the
  // section; a section the role can read but not change stays in the nav and
  // renders read-only. Hiding a readable section costs a user real
  // information ("how many crews does this workspace have?") to save them a
  // disabled button, which is the wrong trade.
  //
  // Gate on lib/permissions/tiers, never on CASL: CASL disagrees with the
  // server's route tiers on crews and members (see that module's header).
  visibleTo?: (role: string | null | undefined) => boolean
  /**
   * Set false for a section whose backing feature is not built yet. Unlike
   * `visibleTo` this is not about the caller — nobody sees it, at any role.
   * Hiding beats shipping a screen that can only ever be empty: an empty
   * state reads as "nothing yet, check back", which is a promise the
   * backend cannot keep.
   */
  enabled?: boolean
}

interface NavSection {
  label: string
  subtitle?: string
  items: NavItem[]
}

const sections: NavSection[] = [
  {
    label: "Account",
    items: [
      { key: "profile", label: "Profile", icon: User },
      // Hidden until peer-card extraction actually exists. The routine runs
      // daily and the endpoints are real, but the extractor wired in
      // production is consolidate.NoopExtractor (cmd/crewship/cmd_start.go)
      // — it returns empty content, which SyncPeerCard skips rather than
      // writes. So the card list can never be non-empty, and "No peer cards
      // on file" tells the user to wait for something that will not come.
      // Flip to true in the same PR that wires the aux-LLM extractor; the
      // section, its endpoints and its tests are all kept working.
      { key: "privacy", label: "Privacy", icon: Shield, enabled: false },
    ],
  },
  {
    label: "Workspace",
    items: [
      // General stays open to everyone: identity, usage counts and the
      // workspace-wide switches are readable at any tier, the forms inside go
      // read-only for non-admins.
      { key: "general", label: "General", icon: Building },
      // What this settings page is FOR: the workspace itself, its people, and
      // the record of what they did. Everything that configures an object
      // lives with that object —
      //   · crew container limits / network policy → /crews → crew → Settings
      //   · notification channels + preferences    → /integrations
      //   · credentials                            → /credentials
      //   · auxiliary models, providers, limits    → /admin
      // A second editor for someone else's object is how the two drift apart;
      // the crew one already had, hiding `allow_private_endpoints` from anyone
      // who edited egress from here.
      //
      // Cross-crew links are a roleCreate mutation with nothing useful to read
      // underneath for a MEMBER. Named "Crew links" and not "Connections":
      // Integrations owns that word for the things Crewship is hooked up to.
      { key: "connections", label: "Crew links", icon: Link2, visibleTo: isManagerTier },
      // The roster is readable by every role; the invite/role controls inside
      // are gated separately.
      { key: "members", label: "Members", icon: Users },
      // Workspace-wide secret policy (PRD-CREDENTIALS-V2-2026 §2.6). MANAGER
      // reads it — they have to know the rules they work under — and the
      // controls inside go read-only for them and for ADMIN, because the
      // reveal switch is OWNER-only server-side. MEMBER and VIEWER cannot
      // read the policy at all (GET is MANAGER+), so the row is hidden
      // rather than rendered over a 403.
      { key: "access-secrets", label: "Access & Secrets", icon: KeyRound, visibleTo: isManagerTier },
      // The audit log is not readable below MANAGER, so the pane would be
      // empty — the one section where hiding beats read-only.
      { key: "audit", label: "Audit Log", icon: Activity, visibleTo: isManagerTier },
    ],
  },
]

/**
 * Whether `role` may see the settings section `key`.
 *
 * Exported so the layout can apply the SAME rule to a `?tab=` deep link —
 * /settings?tab=audit as a MEMBER must not open a pane with no row in the nav
 * to go back to. Unknown keys are not this gate's business (the URL parser
 * already maps them to Profile), so they pass.
 */
export function isSettingsSectionVisible(key: string, role: string | null | undefined): boolean {
  const item = sections.flatMap((s) => s.items).find((i) => i.key === key)
  if (item?.enabled === false) return false
  return item?.visibleTo?.(role) ?? true
}

interface SettingsNavProps {
  activeTab: string
  onTabChange: (tab: string) => void
  workspaceName?: string
  /** Caller's workspace role. The nav owns the per-section rule; the layout
   *  passes the raw role rather than one boolean per gated section. */
  role?: string | null
}

export function SettingsNav({ activeTab, onTabChange, workspaceName, role }: SettingsNavProps) {
  // Universal search doubles as a command-finder here — type "audit" to jump
  // straight to Audit Log. Filters the nav live; Enter opens the first match.
  const [query, setQuery] = useState("")
  const q = query.trim().toLowerCase()

  const filtered = useMemo(
    () =>
      sections
        .map((s) => ({
          ...s,
          items: s.items.filter(
            // Role gate first, so search can never surface a hidden row.
            (i) => isSettingsSectionVisible(i.key, role) && (!q || i.label.toLowerCase().includes(q)),
          ),
        }))
        .filter((s) => s.items.length > 0),
    [q, role],
  )

  const firstMatch = filtered[0]?.items[0]?.key

  return (
    <aside className={cn(SIDEBAR_WIDTH, "shrink-0 bg-sidebar border-r border-sidebar-border flex flex-col")}>
      <SidebarToolbar>
        <SidebarSearch
          value={query}
          onValueChange={setQuery}
          placeholder="Search settings…"
          onKeyDown={(e) => {
            if (e.key === "Enter" && firstMatch) onTabChange(firstMatch)
          }}
        />
      </SidebarToolbar>

      <nav className="flex-1 overflow-y-auto pb-4" aria-label="Settings sections">
        {filtered.map((section) => (
          <SidebarSection
            key={section.label}
            label={section.label}
            actions={
              section.label === "Workspace" && workspaceName ? (
                <span className="ml-1 truncate font-mono text-[10px] normal-case tracking-normal text-sidebar-foreground/35">
                  {workspaceName}
                </span>
              ) : undefined
            }
          >
            {section.items.map((item) => {
              const isActive = item.key === activeTab
              return (
                <SidebarRow
                  key={item.key}
                  selected={isActive}
                  onSelect={() => onTabChange(item.key)}
                  aria-label={item.label}
                >
                  <item.icon className={cn("h-3.5 w-3.5 shrink-0", isActive ? "opacity-100" : "opacity-60")} />
                  <span className="truncate flex-1">{item.label}</span>
                  {item.badge === "P2" && (
                    <span className="ml-auto shrink-0 font-mono text-[10px] text-sidebar-foreground/40">P2</span>
                  )}
                  {item.badge === "OWNER" && (
                    <span className="ml-auto shrink-0 font-mono text-[10px] text-sidebar-foreground/60">Owner</span>
                  )}
                </SidebarRow>
              )
            })}
          </SidebarSection>
        ))}
      </nav>
    </aside>
  )
}
