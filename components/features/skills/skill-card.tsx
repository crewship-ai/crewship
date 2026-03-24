"use client"

import Link from "next/link"
import {
  Blocks, Wrench, Star, Download, Users, ShieldCheck,
  Code, Search, Hammer, Server, MessageCircle, Settings,
} from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"

interface SkillData {
  id: string
  name: string
  slug: string
  display_name: string | null
  description: string | null
  version: string | null
  author: string | null
  category: string
  source: string
  icon: string | null
  verification: string | null
  downloads: number | null
  rating_avg: number | null
  rating_count: number | null
  featured: boolean
  tool_count: number | null
}

const SOURCE_STYLES: Record<string, { label: string; className: string }> = {
  BUILTIN: { label: "Built-in", className: "bg-blue-50 text-blue-700 dark:bg-blue-950 dark:text-blue-400" },
  BUNDLED: { label: "Bundled", className: "bg-blue-50 text-blue-700 dark:bg-blue-950 dark:text-blue-400" },
  CUSTOM: { label: "Custom", className: "bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-400" },
  MARKETPLACE: { label: "Marketplace", className: "bg-violet-50 text-violet-700 dark:bg-violet-950 dark:text-violet-400" },
}

const CATEGORY_ICONS: Record<string, React.ElementType> = {
  CODING: Code,
  RESEARCH: Search,
  DEVELOPMENT: Hammer,
  DEVOPS: Server,
  COMMUNICATION: MessageCircle,
  CUSTOM: Settings,
}

export function SkillCard({ skill }: { skill: SkillData }) {
  const sourceCfg = SOURCE_STYLES[skill.source] ?? { label: skill.source, className: "" }
  const CategoryIcon = CATEGORY_ICONS[skill.category] ?? Blocks

  return (
    <Link
      href={`/skills/${skill.id}`}
      className="rounded-[var(--radius)] focus-visible:ring-2 focus-visible:ring-primary focus-visible:ring-offset-2 outline-none"
    >
      <Card className={`hover:border-primary/50 hover:bg-accent/30 hover:shadow-md transition-all duration-150 cursor-pointer h-full border-border/80 shadow-md ${skill.featured ? "ring-1 ring-primary/20" : ""}`}>
        <CardContent className="p-4 sm:p-5">
          <div className="flex items-start gap-3">
            <div className="flex h-10 w-10 items-center justify-center rounded-lg bg-primary/10 shrink-0">
              <CategoryIcon className="h-5 w-5 text-primary" />
            </div>
            <div className="flex-1 min-w-0">
              <div className="flex items-center justify-between gap-2">
                <h3 className="text-body font-semibold truncate">
                  {skill.display_name ?? skill.name}
                </h3>
                <Badge variant="secondary" className={`text-micro shrink-0 ${sourceCfg.className}`}>
                  {sourceCfg.label}
                </Badge>
              </div>
              <p className="text-label text-muted-foreground mt-0.5 line-clamp-2 min-h-[2.5rem]">
                {skill.description || <span className="italic">No description</span>}
              </p>
            </div>
          </div>

          <div className="mt-3 flex items-center gap-2 flex-wrap">
            <Badge variant="outline" className="text-micro gap-1">
              <CategoryIcon className="h-3 w-3" />
              {skill.category.charAt(0) + skill.category.slice(1).toLowerCase()}
            </Badge>
            {skill.version && (
              <Badge variant="outline" className="text-micro text-muted-foreground">
                v{skill.version}
              </Badge>
            )}
            {skill.verification && skill.verification !== "UNVERIFIED" && (
              <Badge variant="outline" className="text-micro gap-1 text-emerald-700 dark:text-emerald-400">
                <ShieldCheck className="h-3 w-3" />
                Verified
              </Badge>
            )}
            {skill.featured && (
              <Badge variant="outline" className="text-micro gap-1 text-amber-600 dark:text-amber-400">
                <Star className="h-3 w-3" />
                Featured
              </Badge>
            )}
          </div>

          <div className="mt-3 pt-3 border-t flex items-center gap-4 text-label text-muted-foreground">
            <span className="flex items-center gap-1">
              <Wrench className="h-3 w-3" />
              {skill.tool_count ?? 0} tools
            </span>
            {skill.author && (
              <span className="flex items-center gap-1 truncate">
                <Users className="h-3 w-3 shrink-0" />
                {skill.author}
              </span>
            )}
            {(skill.downloads ?? 0) > 0 && (
              <span className="flex items-center gap-1 ml-auto">
                <Download className="h-3 w-3" />
                {skill.downloads}
              </span>
            )}
            {skill.rating_avg != null && skill.rating_avg > 0 && (
              <span className="flex items-center gap-1">
                <Star className="h-3 w-3" />
                {skill.rating_avg.toFixed(1)}
              </span>
            )}
          </div>
        </CardContent>
      </Card>
    </Link>
  )
}
