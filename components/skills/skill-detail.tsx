"use client"

import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Separator } from "@/components/ui/separator"

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

  return (
    <div className="space-y-6">
      <div className="flex items-start gap-4">
        <div className="flex h-14 w-14 items-center justify-center rounded-xl bg-muted text-2xl shrink-0">
          {skill.icon ?? "🔧"}
        </div>
        <div className="flex-1">
          <div className="flex items-center gap-2 flex-wrap">
            <h1 className="text-xl font-bold">
              {skill.display_name ?? skill.name}
            </h1>
            <Badge variant="secondary">{skill.source}</Badge>
            {skill.verification && skill.verification !== "UNVERIFIED" && (
              <Badge variant="outline" className="text-emerald-700">
                {skill.verification}
              </Badge>
            )}
          </div>
          {skill.description && (
            <p className="mt-1 text-sm text-muted-foreground">{skill.description}</p>
          )}
          <div className="flex items-center gap-3 mt-2 flex-wrap">
            <Badge variant="outline">{skill.category}</Badge>
            {skill.version && (
              <span className="text-xs text-muted-foreground">v{skill.version}</span>
            )}
            {skill.author && (
              <span className="text-xs text-muted-foreground">by {skill.author}</span>
            )}
            {skill.license && (
              <span className="text-xs text-muted-foreground">{skill.license}</span>
            )}
          </div>
        </div>
      </div>

      <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
        <Card>
          <CardContent className="p-3 text-center">
            <div className="text-lg font-bold">{skill.agent_count}</div>
            <div className="text-xs text-muted-foreground">Agents using</div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="p-3 text-center">
            <div className="text-lg font-bold">{skill.tool_count ?? 0}</div>
            <div className="text-xs text-muted-foreground">Tools</div>
          </CardContent>
        </Card>
        {credReqs.length > 0 && (
          <Card>
            <CardContent className="p-3 text-center">
              <div className="text-lg font-bold">{credReqs.length}</div>
              <div className="text-xs text-muted-foreground">Credentials</div>
            </CardContent>
          </Card>
        )}
      </div>

      {parsedTags.length > 0 && (
        <div>
          <h3 className="text-sm font-medium mb-2">Tags</h3>
          <div className="flex flex-wrap gap-1">
            {parsedTags.map((tag) => (
              <Badge key={tag} variant="outline" className="text-xs">
                {tag}
              </Badge>
            ))}
          </div>
        </div>
      )}

      {credReqs.length > 0 && (
        <div>
          <h3 className="text-sm font-medium mb-2">Credential Requirements</h3>
          <div className="flex flex-wrap gap-1">
            {credReqs.map((req) => (
              <Badge key={req} variant="secondary" className="text-xs font-mono">
                {req}
              </Badge>
            ))}
          </div>
        </div>
      )}

      {skill.mcp_server_command && (
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm">MCP Server</CardTitle>
          </CardHeader>
          <CardContent>
            <code className="text-xs bg-muted px-2 py-1 rounded block">
              {skill.mcp_server_command}
            </code>
            {skill.mcp_transport && (
              <span className="text-xs text-muted-foreground mt-1 block">
                Transport: {skill.mcp_transport}
              </span>
            )}
          </CardContent>
        </Card>
      )}

      {skill.content && (
        <>
          <Separator />
          <div>
            <h3 className="text-sm font-medium mb-2">Skill Content</h3>
            <Card>
              <CardContent className="p-4">
                <pre className="text-xs whitespace-pre-wrap max-h-96 overflow-y-auto">
                  {skill.content}
                </pre>
              </CardContent>
            </Card>
          </div>
        </>
      )}

      <div className="text-xs text-muted-foreground">
        Created {new Date(skill.created_at).toLocaleDateString()} · Updated{" "}
        {new Date(skill.updated_at).toLocaleDateString()}
      </div>
    </div>
  )
}
