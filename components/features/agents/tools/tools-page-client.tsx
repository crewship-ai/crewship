"use client"

import { useCallback, useMemo } from "react"
import { Zap, Key, Plug } from "lucide-react"
import { ToolbarStrip, type ToolbarTab } from "@/components/layout/toolbar-strip"
import { SkillsPageClient } from "@/components/features/agents/tools/skills-pane"
import { CredentialsPageClient } from "@/components/features/agents/tools/credentials-pane"
import { MCPPageClient } from "@/components/features/agents/tools/mcp-pane"
import { useShallowSearchParam } from "@/hooks/use-shallow-search-param"

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
  const [sectionRaw, setSectionRaw] = useShallowSearchParam("section", "skills")
  const activeSection = useMemo(() => parseSection(sectionRaw), [sectionRaw])

  const handleChange = useCallback(
    (section: Section) => {
      setSectionRaw(section)
    },
    [setSectionRaw],
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
