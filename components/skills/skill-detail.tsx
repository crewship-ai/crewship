"use client"

import {
  Blocks, Wrench, Users, Download, Star, ShieldCheck, Clock,
  Code, Search, Hammer, Server, MessageCircle, Settings,
  KeyRound, Terminal, Shield, ChevronDown, Globe, Package,
} from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent } from "@/components/ui/card"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"
import type { SkillDetail } from "@/app/(dashboard)/skills/[skillId]/skill-detail-client"

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

function parseJSON<T>(raw: string | null, fallback: T): T {
  if (!raw) return fallback
  try {
    const parsed = JSON.parse(raw)
    return Array.isArray(parsed) ? (parsed as T) : fallback
  } catch {
    return fallback
  }
}

export function SkillDetailView({ skill }: SkillDetailProps) {
  const tags = parseJSON<string[]>(skill.tags, [])
  const credReqs = parseJSON<string[]>(skill.credential_requirements, [])
  const deps = parseJSON<string[]>(skill.dependencies, [])
  const allowedDomains = parseJSON<string[]>(skill.allowed_domains, [])

  const sourceCfg = SOURCE_STYLES[skill.source] ?? { label: skill.source, className: "" }
  const CategoryIcon = CATEGORY_ICONS[skill.category] ?? Blocks

  return (
    <div className="space-y-6">
      {/* Hero */}
      <div className="flex items-start gap-4">
        <div className="flex h-14 w-14 items-center justify-center rounded-xl bg-primary/10 shrink-0">
          <CategoryIcon className="h-7 w-7 text-primary" />
        </div>
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 flex-wrap">
            <h1 className="text-xl font-semibold">
              {skill.display_name ?? skill.name}
            </h1>
            <span className="text-xs font-mono text-muted-foreground">{skill.slug}</span>
            <Badge variant="secondary" className={`text-xs ${sourceCfg.className}`}>
              {sourceCfg.label}
            </Badge>
            {skill.verification && skill.verification !== "UNVERIFIED" && (
              <Badge variant="outline" className="text-xs gap-1 text-emerald-700 dark:text-emerald-400">
                <ShieldCheck className="h-3 w-3" />
                Verified
              </Badge>
            )}
            {skill.featured && (
              <Badge variant="outline" className="text-xs gap-1 text-amber-600 dark:text-amber-400">
                <Star className="h-3 w-3" />
                Featured
              </Badge>
            )}
            {skill.pricing_tier !== "FREE" && (
              <Badge variant="secondary" className="text-xs">{skill.pricing_tier}</Badge>
            )}
          </div>
          {skill.description && (
            <p className="mt-1.5 text-sm text-muted-foreground leading-relaxed">{skill.description}</p>
          )}
          <div className="flex items-center gap-3 mt-2 flex-wrap text-xs text-muted-foreground">
            <span className="flex items-center gap-1">
              <CategoryIcon className="h-3 w-3" />
              {skill.category.charAt(0) + skill.category.slice(1).toLowerCase()}
            </span>
            {skill.version && (
              <span className="font-mono">v{skill.version}</span>
            )}
            {skill.author && (
              <span className="flex items-center gap-1">
                <Users className="h-3 w-3" />
                {skill.author}
              </span>
            )}
            {skill.license && <span>{skill.license}</span>}
            <span className="flex items-center gap-1">
              <Clock className="h-3 w-3" />
              Created {new Date(skill.created_at).toLocaleDateString()}
            </span>
          </div>
        </div>
      </div>

      {/* Stats */}
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-4">
        <Card>
          <CardContent className="p-4">
            <div className="flex items-center gap-2 text-muted-foreground">
              <Users className="h-4 w-4" />
              <span className="text-xs">Agents Using</span>
            </div>
            <p className="text-2xl font-bold mt-1">{skill.agent_count}</p>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="p-4">
            <div className="flex items-center gap-2 text-muted-foreground">
              <Wrench className="h-4 w-4" />
              <span className="text-xs">Tools</span>
            </div>
            <p className="text-2xl font-bold mt-1">{skill.tool_count ?? 0}</p>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="p-4">
            <div className="flex items-center gap-2 text-muted-foreground">
              <Download className="h-4 w-4" />
              <span className="text-xs">Downloads</span>
            </div>
            <p className="text-2xl font-bold mt-1">{skill.downloads}</p>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="p-4">
            <div className="flex items-center gap-2 text-muted-foreground">
              <Star className="h-4 w-4" />
              <span className="text-xs">Rating</span>
            </div>
            <p className="text-2xl font-bold mt-1">
              {skill.rating_avg != null ? skill.rating_avg.toFixed(1) : "—"}
              {skill.rating_count > 0 && (
                <span className="text-xs font-normal text-muted-foreground ml-1">({skill.rating_count})</span>
              )}
            </p>
          </CardContent>
        </Card>
      </div>

      {/* Tags */}
      {tags.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {tags.map((tag) => (
            <Badge key={tag} variant="outline" className="text-xs">
              {tag}
            </Badge>
          ))}
        </div>
      )}

      {/* Credential Requirements */}
      {credReqs.length > 0 && (
        <Card>
          <CardContent className="p-4 space-y-2">
            <div className="flex items-center gap-2">
              <KeyRound className="h-4 w-4 text-muted-foreground" />
              <span className="text-sm font-medium">Credential Requirements</span>
            </div>
            <div className="flex flex-wrap gap-1.5">
              {credReqs.map((req) => (
                <Badge key={req} variant="secondary" className="text-xs font-mono gap-1">
                  <KeyRound className="h-3 w-3" />
                  {req}
                </Badge>
              ))}
            </div>
          </CardContent>
        </Card>
      )}

      {/* MCP Server */}
      {skill.mcp_server_command && (
        <Card>
          <CardContent className="p-4 space-y-3">
            <div className="flex items-center gap-2">
              <Terminal className="h-4 w-4 text-muted-foreground" />
              <span className="text-sm font-medium">MCP Server</span>
            </div>
            <code className="text-xs bg-muted px-3 py-2 rounded-lg block font-mono overflow-x-auto">
              {skill.mcp_server_command}
            </code>
            {(skill.mcp_transport || skill.mcp_server_image) && (
              <div className="flex items-center gap-4 text-xs text-muted-foreground">
                {skill.mcp_transport && (
                  <span className="flex items-center gap-1">
                    Transport: <Badge variant="outline" className="text-xs">{skill.mcp_transport}</Badge>
                  </span>
                )}
                {skill.mcp_server_image && (
                  <span className="flex items-center gap-1 font-mono">{skill.mcp_server_image}</span>
                )}
              </div>
            )}
          </CardContent>
        </Card>
      )}

      {/* Skill Content — collapsible */}
      {skill.content && (
        <Collapsible>
          <Card>
            <CollapsibleTrigger asChild>
              <button
                type="button"
                className="flex w-full items-center justify-between p-4 text-left hover:bg-muted/50 transition-colors rounded-xl"
              >
                <div className="flex items-center gap-2">
                  <Code className="h-4 w-4 text-muted-foreground" />
                  <span className="text-sm font-medium">Skill Content</span>
                </div>
                <ChevronDown className="h-4 w-4 text-muted-foreground transition-transform duration-200 [[data-state=open]_&]:rotate-180" />
              </button>
            </CollapsibleTrigger>
            <CollapsibleContent>
              <CardContent className="px-4 pb-4 pt-0">
                <div className="bg-muted/50 rounded-lg p-3 font-mono text-xs leading-relaxed max-h-[500px] overflow-y-auto whitespace-pre-wrap">
                  {skill.content}
                </div>
              </CardContent>
            </CollapsibleContent>
          </Card>
        </Collapsible>
      )}

      {/* Security & Compliance — collapsible */}
      {(skill.security_score != null || allowedDomains.length > 0) && (
        <Collapsible>
          <Card>
            <CollapsibleTrigger asChild>
              <button
                type="button"
                className="flex w-full items-center justify-between p-4 text-left hover:bg-muted/50 transition-colors rounded-xl"
              >
                <div className="flex items-center gap-2">
                  <Shield className="h-4 w-4 text-muted-foreground" />
                  <span className="text-sm font-medium">Security & Compliance</span>
                  {skill.security_score != null && (
                    <Badge variant="outline" className="text-xs">
                      Score: {skill.security_score}/100
                    </Badge>
                  )}
                </div>
                <ChevronDown className="h-4 w-4 text-muted-foreground transition-transform duration-200 [[data-state=open]_&]:rotate-180" />
              </button>
            </CollapsibleTrigger>
            <CollapsibleContent>
              <CardContent className="px-4 pb-4 pt-0 space-y-3">
                {skill.security_score != null && (
                  <div className="space-y-1">
                    <div className="text-xs text-muted-foreground">Security Score</div>
                    <div className="flex items-center gap-2">
                      <div className="flex-1 h-2 bg-muted rounded-full overflow-hidden">
                        <div
                          className={`h-full rounded-full ${skill.security_score >= 80 ? "bg-emerald-500" : skill.security_score >= 50 ? "bg-amber-500" : "bg-red-500"}`}
                          style={{ width: `${skill.security_score}%` }}
                        />
                      </div>
                      <span className="text-sm font-mono font-medium">{skill.security_score}</span>
                    </div>
                  </div>
                )}
                {allowedDomains.length > 0 && (
                  <div className="space-y-1">
                    <div className="text-xs text-muted-foreground flex items-center gap-1">
                      <Globe className="h-3 w-3" />
                      Allowed Domains
                    </div>
                    <div className="flex flex-wrap gap-1.5">
                      {allowedDomains.map((d) => (
                        <Badge key={d} variant="outline" className="text-xs font-mono">{d}</Badge>
                      ))}
                    </div>
                  </div>
                )}
              </CardContent>
            </CollapsibleContent>
          </Card>
        </Collapsible>
      )}

      {/* Dependencies — collapsible */}
      {deps.length > 0 && (
        <Collapsible>
          <Card>
            <CollapsibleTrigger asChild>
              <button
                type="button"
                className="flex w-full items-center justify-between p-4 text-left hover:bg-muted/50 transition-colors rounded-xl"
              >
                <div className="flex items-center gap-2">
                  <Package className="h-4 w-4 text-muted-foreground" />
                  <span className="text-sm font-medium">Dependencies</span>
                  <span className="text-xs text-muted-foreground">({deps.length})</span>
                </div>
                <ChevronDown className="h-4 w-4 text-muted-foreground transition-transform duration-200 [[data-state=open]_&]:rotate-180" />
              </button>
            </CollapsibleTrigger>
            <CollapsibleContent>
              <CardContent className="px-4 pb-4 pt-0">
                <div className="flex flex-wrap gap-1.5">
                  {deps.map((dep) => (
                    <Badge key={dep} variant="secondary" className="text-xs font-mono">{dep}</Badge>
                  ))}
                </div>
              </CardContent>
            </CollapsibleContent>
          </Card>
        </Collapsible>
      )}

      {/* Changelog — collapsible */}
      {skill.changelog && (
        <Collapsible>
          <Card>
            <CollapsibleTrigger asChild>
              <button
                type="button"
                className="flex w-full items-center justify-between p-4 text-left hover:bg-muted/50 transition-colors rounded-xl"
              >
                <div className="flex items-center gap-2">
                  <Clock className="h-4 w-4 text-muted-foreground" />
                  <span className="text-sm font-medium">Changelog</span>
                </div>
                <ChevronDown className="h-4 w-4 text-muted-foreground transition-transform duration-200 [[data-state=open]_&]:rotate-180" />
              </button>
            </CollapsibleTrigger>
            <CollapsibleContent>
              <CardContent className="px-4 pb-4 pt-0">
                <div className="bg-muted/50 rounded-lg p-3 text-sm leading-relaxed whitespace-pre-wrap">
                  {skill.changelog}
                </div>
              </CardContent>
            </CollapsibleContent>
          </Card>
        </Collapsible>
      )}
    </div>
  )
}
