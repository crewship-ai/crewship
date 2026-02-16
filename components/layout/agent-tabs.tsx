"use client"

import Link from "next/link"
import { usePathname } from "next/navigation"
import { cn } from "@/lib/utils"

const tabs = [
  { label: "Overview", href: "" },
  { label: "Chat", href: "/chat" },
  { label: "Sessions", href: "/sessions" },
  { label: "Files", href: "/files" },
  { label: "Runs", href: "/runs" },
  { label: "Logs", href: "/logs" },
  { label: "Settings", href: "/settings" },
  { label: "Skills", href: "/skills" },
  { label: "Credentials", href: "/credentials" },
  { label: "History", href: "/history" },
]

interface AgentTabsProps {
  agentId: string
}

export function AgentTabs({ agentId }: AgentTabsProps) {
  const pathname = usePathname()
  const basePath = `/agents/${agentId}`

  return (
    <div className="flex gap-4 sm:gap-6 overflow-x-auto scrollbar-none px-4 sm:px-6 text-sm">
      {tabs.map((tab) => {
        const tabPath = tab.href ? `${basePath}${tab.href}` : basePath
        const isActive = tab.href === ""
          ? pathname === basePath
          : pathname.startsWith(tabPath)

        return (
          <Link
            key={tab.href}
            href={tabPath}
            className={cn(
              "shrink-0 border-b-2 pb-3 pt-1 transition-colors",
              isActive
                ? "border-primary text-primary font-medium"
                : "border-transparent text-muted-foreground hover:text-foreground hover:border-border"
            )}
          >
            {tab.label}
          </Link>
        )
      })}
    </div>
  )
}
