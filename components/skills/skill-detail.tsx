"use client"

import {
  Blocks, Wrench, Users, Download, Star, ShieldCheck, Clock,
  Code, Search, Hammer, Server, MessageCircle, Settings,
  KeyRound, Terminal,
} from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent } from "@/components/ui/card"

interface SkillDetail {
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
  content: string | null
  credential_requirements: string | null
  mcp_server_command: string | null
  mcp_transport: string | null
  license: string | null
  tags: string | null
  tool_count: number | null
  agent_count: number
  created_at: string
  updated_at: string
}

interface SkillDetailProps {
  skill: SkillDetail
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

export function SkillDetailView({ skill }: SkillDetailProps) {
  const parsedTags: string[] = (() => {
    if (!skill.tags) return []
    try {
      const parsed = JSON.parse(skill.tags)
      return Array.isArray(parsed) ? parsed : []
    } catch {
      return []
    }
  })()

  const credReqs: string[] = (() => {
    if (!skill.credential_requirements) return []
    try {
      const parsed = JSON.parse(skill.credential_requirements)
      return Array.isArray(parsed) ? parsed : []
    } catch {
      return []
    }
  })()

  const sourceCfg = SOURCE_STYLES[skill.source] ?? { label: skill.source, className: "" }
  const CategoryIcon = CATEGORY_ICONS[skill.category] ?? Blocks

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-start gap-4">
        <div className="flex h-14 w-14 items-center justify-center rounded-xl bg-primary/10 shrink-0">
          <CategoryIcon className="h-7 w-7 text-primary" />
        </div>
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 flex-wrap">
            <h1 className="text-title font-bold">
              {skill.display_name ?? skill.name}
            </h1>
            <Badge variant="secondary" className={`text-micro ${sourceCfg.className}`}>
              {sourceCfg.label}
            </Badge>
            {skill.verification && skill.verification !== "UNVERIFIED" && (
              <Badge variant="outline" className="text-micro gap-1 text-emerald-700 dark:text-emerald-400">
                <ShieldCheck className="h-3 w-3" />
                Verified
              </Badge>
            )}
          </div>
          {skill.description && (
            <p className="mt-1.5 text-body text-muted-foreground leading-relaxed">{skill.description}</p>
          )}
          <div className="flex items-center gap-3 mt-2.5 flex-wrap">
            <Badge variant="outline" className="text-micro gap-1">
              <CategoryIcon className="h-3 w-3" />
              {skill.category.charAt(0) + skill.category.slice(1).toLowerCase()}
            </Badge>
            {skill.version && (
              <span className="text-label text-muted-foreground font-mono">v{skill.version}</span>
            )}
            {skill.author && (
              <span className="flex items-center gap-1 text-label text-muted-foreground">
                <Users className="h-3 w-3" />
                {skill.author}
              </span>
            )}
            {skill.license && (
              <span className="text-label text-muted-foreground">{skill.license}</span>
            )}
          </div>
        </div>
      </div>

      {/* Stats Row */}
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
        <Card>
          <CardContent className="p-4 text-center">
            <div className="flex items-center justify-center gap-1.5 text-muted-foreground mb-1">
              <Users className="h-3.5 w-3.5" />
            </div>
            <div className="text-heading font-bold">{skill.agent_count}</div>
            <div className="text-micro text-muted-foreground mt-0.5">Agents using</div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="p-4 text-center">
            <div className="flex items-center justify-center gap-1.5 text-muted-foreground mb-1">
              <Wrench className="h-3.5 w-3.5" />
            </div>
            <div className="text-heading font-bold">{skill.tool_count ?? 0}</div>
            <div className="text-micro text-muted-foreground mt-0.5">Tools</div>
          </CardContent>
        </Card>
        {credReqs.length > 0 && (
          <Card>
            <CardContent className="p-4 text-center">
              <div className="flex items-center justify-center gap-1.5 text-muted-foreground mb-1">
                <KeyRound className="h-3.5 w-3.5" />
              </div>
              <div className="text-heading font-bold">{credReqs.length}</div>
              <div className="text-micro text-muted-foreground mt-0.5">Credentials</div>
            </CardContent>
          </Card>
        )}
      </div>

      {/* Tags */}
      {parsedTags.length > 0 && (
        <div>
          <h3 className="text-micro font-semibold uppercase tracking-wider text-muted-foreground mb-2">Tags</h3>
          <div className="flex flex-wrap gap-1.5">
            {parsedTags.map((tag) => (
              <Badge key={tag} variant="outline" className="text-micro">
                {tag}
              </Badge>
            ))}
          </div>
        </div>
      )}

      {/* Credential Requirements */}
      {credReqs.length > 0 && (
        <div>
          <h3 className="text-micro font-semibold uppercase tracking-wider text-muted-foreground mb-2">Credential Requirements</h3>
          <div className="flex flex-wrap gap-1.5">
            {credReqs.map((req) => (
              <Badge key={req} variant="secondary" className="text-micro font-mono gap-1">
                <KeyRound className="h-3 w-3" />
                {req}
              </Badge>
            ))}
          </div>
        </div>
      )}

      {/* MCP Server */}
      {skill.mcp_server_command && (
        <Card>
          <CardContent className="p-4 space-y-3">
            <div className="flex items-center gap-2 mb-1">
              <Terminal className="h-4 w-4 text-muted-foreground" />
              <span className="text-micro font-semibold uppercase tracking-wider text-muted-foreground">MCP Server</span>
            </div>
            <code className="text-label bg-muted px-3 py-2 rounded-lg block font-mono">
              {skill.mcp_server_command}
            </code>
            {skill.mcp_transport && (
              <div className="flex items-center justify-between text-body">
                <span className="text-muted-foreground">Transport</span>
                <Badge variant="outline" className="text-micro">{skill.mcp_transport}</Badge>
              </div>
            )}
          </CardContent>
        </Card>
      )}

      {/* Skill Content */}
      {skill.content && (
        <Card>
          <CardContent className="p-4 space-y-3">
            <div className="flex items-center gap-2 mb-1">
              <Code className="h-4 w-4 text-muted-foreground" />
              <span className="text-micro font-semibold uppercase tracking-wider text-muted-foreground">Skill Content</span>
            </div>
            <div className="bg-muted/50 rounded-lg p-3 font-mono text-xs leading-relaxed max-h-96 overflow-y-auto whitespace-pre-wrap">
              {skill.content}
            </div>
          </CardContent>
        </Card>
      )}

      {/* Footer metadata */}
      <div className="flex items-center gap-3 text-label text-muted-foreground">
        <Clock className="h-3 w-3" />
        Created {new Date(skill.created_at).toLocaleDateString()} · Updated{" "}
        {new Date(skill.updated_at).toLocaleDateString()}
      </div>
    </div>
  )
}
