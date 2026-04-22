"use client"

import { useCallback, useMemo } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { Zap, Key, Plug } from "lucide-react"
import { ToolbarStrip, type ToolbarTab } from "@/components/layout/toolbar-strip"
import { SkillsPageClient } from "@/app/(dashboard)/fleet/agents/[agentId]/skills/skills-client"
import { CredentialsPageClient } from "@/app/(dashboard)/fleet/agents/[agentId]/credentials/credentials-client"
import { MCPPageClient } from "@/app/(dashboard)/fleet/agents/[agentId]/mcp/mcp-client"

type Section = "skills" | "credentials" | "mcp"

const SECTION_TABS: ToolbarTab<Section>[] = [
  { id: "skills", label: "Skills", icon: Zap },
  { id: "credentials", label: "Credentials", icon: Key },
  { id: "mcp", label: "MCP", icon: Plug },
]

function parseSection(value: string | null): Section {
  if (value === "credentials" || value === "mcp") return value
  return "skills"
}

export function ToolsPageClient() {
  const searchParams = useSearchParams()
  const router = useRouter()
  const activeSection = useMemo(() => parseSection(searchParams.get("section")), [searchParams])

  const handleChange = useCallback(
    (section: Section) => {
      const params = new URLSearchParams(searchParams.toString())
      params.set("section", section)
      router.replace(`?${params.toString()}`, { scroll: false })
    },
    [router, searchParams],
  )

  return (
    <div className="flex flex-col h-full min-h-0">
      <ToolbarStrip
        tabs={SECTION_TABS}
        activeTab={activeSection}
        onTabChange={handleChange}
        ariaLabel="Agent tools"
      />
      <div className="flex-1 min-h-0 overflow-y-auto">
        {activeSection === "skills" && <SkillsPageClient />}
        {activeSection === "credentials" && <CredentialsPageClient />}
        {activeSection === "mcp" && <MCPPageClient />}
      </div>
    </div>
  )
}
